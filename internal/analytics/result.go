package analytics

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// BuildResult 根据已校验的查询和历史快照生成统计结果；不访问数据库，也不修改快照。
// 当前时间显式传入，使持续缺口判断可以独立验证。
func BuildResult(
	p Query,
	snapshot Snapshot,
	now time.Time,
	maxInterval time.Duration,
) (Result, error) {
	if p.To <= p.From || StepMS(p.Step) == 0 {
		return Result{}, ErrInvalid
	}
	if len(p.IDs) > 0 && len(snapshot.Instances) != len(p.IDs) {
		return Result{}, ErrNotFound
	}
	// 全局历史查询可以包含超过 256 个身份，限制仅适用于显式选择。
	if len(p.IDs) > 256 {
		return Result{}, fmt.Errorf("%w: select at most 256 historical instances", ErrInvalid)
	}
	observed := map[string]int64{}
	for id, n := range snapshot.Observed {
		observed[id] = n
	}
	points := map[int64]*Point{}
	summary := Totals{}
	step := StepMS(p.Step)
	for _, r := range snapshot.Buckets {
		n := observed[r.InstanceID]
		if err := sumInto(&n, r.ObservedMS); err != nil {
			return Result{}, err
		}
		observed[r.InstanceID] = n
		if err := addTotals(&summary, r); err != nil {
			return Result{}, err
		}
		bucket := r.Bucket / step * step
		point := points[bucket]
		if point == nil {
			point = &Point{Bucket: bucket}
			points[bucket] = point
		}
		if err := addTotals(&point.Totals, r); err != nil {
			return Result{}, err
		}
		if err := sumInto(&point.ObservedMS, r.ObservedMS); err != nil {
			return Result{}, err
		}
		point.HasGap = point.HasGap || r.HasGap
	}
	finishTotals(&summary)
	meta := Meta{
		Source: "mihomo_connections_ws",
		MetricDefinition: "observed_counter_deltas; connection attribution is sampled; " +
			"recovery candidates excluded; coverage is time, not accuracy",
		From: p.From, To: p.To, Step: p.Step,
		Coverage: []Coverage{},
	}
	ongoing := []Gap{}
	at := now.UnixMilli()
	for _, item := range snapshot.Instances {
		meta.GapCount += item.GapCount
		c := Coverage{
			InstanceID: item.ID, Name: item.Name, Deleted: item.Deleted,
			ObservedMS: observed[item.ID], WindowMS: p.To - p.From,
			FirstRetainedMinute: item.FirstRetainedMinute,
			FirstRetainedHour:   item.FirstRetainedHour,
			LastSnapshot:        item.LastSnapshot,
		}
		c.SampleCoverage = float64(c.ObservedMS) / float64(c.WindowMS)
		c.HasGap = item.GapCount > 0 || c.ObservedMS < c.WindowMS
		meta.HasGap = meta.HasGap || c.HasGap
		meta.Coverage = append(meta.Coverage, c)
		// 停止管理的实例不再产生持续缺口；从未采样的实例从创建时间起计算。
		if item.Deleted {
			continue
		}
		start := item.CreatedAt
		if item.LastSnapshot != nil {
			start = *item.LastSnapshot
		}
		stale := at-start > maxInterval.Milliseconds()
		overlaps := start < p.To && at >= p.From
		if stale && overlaps {
			ongoing = append(ongoing, Gap{
				ID: "ongoing/" + item.ID, InstanceID: item.ID, From: start,
				Reason: "awaiting_snapshot", Resolution: "unknown",
			})
		}
	}
	meta.GapCount += int64(len(ongoing))
	result := Result{Meta: meta}
	switch p.Kind {
	case "summary":
		result.Data = summary
	case "coverage":
		result.Data = meta.Coverage
	case "traffic":
		result.Data = trafficPoints(p, points, len(snapshot.Instances))
	case "top":
		result.Data = append([]Top{}, snapshot.Top...)
	case "connections":
		result.Data = append([]Connection{}, snapshot.Connections...)
		result.Meta.MetricDefinition = "connection lifecycle cumulative counters, " +
			"not bytes within the query window; selected by observation overlap"
	case "gaps":
		result.Data = gapPage(p, ongoing, snapshot.Gaps)
	}
	result.Meta.HasGap = result.Meta.HasGap || !summary.Consistent
	return result, nil
}

func sumInto(dst *int64, n int64) error {
	if n < 0 || *dst > math.MaxInt64-n {
		return errors.New("analytics counter overflow")
	}
	*dst += n
	return nil
}

func addTotals(dst *Totals, r TrafficBucket) error {
	for _, v := range []struct {
		dst *int64
		n   int64
	}{
		{dst: &dst.Upload, n: r.Upload},
		{dst: &dst.Download, n: r.Download},
		{dst: &dst.AttributedUpload, n: r.AttributedUpload},
		{dst: &dst.AttributedDownload, n: r.AttributedDownload},
	} {
		if err := sumInto(v.dst, v.n); err != nil {
			return err
		}
	}
	return nil
}

func finishTotals(t *Totals) {
	// 归属计数超过总量时标为不一致，避免返回负的未归属流量。
	t.Consistent = t.AttributedUpload <= t.Upload && t.AttributedDownload <= t.Download
	if t.AttributedUpload <= t.Upload {
		n := t.Upload - t.AttributedUpload
		t.UnattributedUpload = &n
	}
	if t.AttributedDownload <= t.Download {
		n := t.Download - t.AttributedDownload
		t.UnattributedDownload = &n
	}
}

func trafficPoints(p Query, points map[int64]*Point, instances int) []Point {
	out := []Point{}
	step := StepMS(p.Step)
	for bucket := p.From / step * step; bucket < p.To; bucket += step {
		point := points[bucket]
		if point == nil {
			point = &Point{Bucket: bucket}
		}
		finishTotals(&point.Totals)
		// 空桶补零，但覆盖不足必须保留缺口标记；多实例按各自观测时长累计。
		expected := (min(bucket+step, p.To) - max(bucket, p.From)) * int64(instances)
		point.HasGap = point.HasGap || point.ObservedMS < expected || !point.Consistent
		out = append(out, *point)
	}
	return out
}

func gapPage(p Query, ongoing, persisted []Gap) []Gap {
	// 先合并持久化缺口与持续缺口，再统一排序、分页，避免持续缺口挤乱页边界。
	out := append(ongoing, persisted...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].From == out[j].From {
			return out[i].ID < out[j].ID
		}
		return out[i].From > out[j].From
	})
	start := min(p.Offset, len(out))
	return out[start:min(start+p.Limit, len(out))]
}
