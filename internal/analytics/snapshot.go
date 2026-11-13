package analytics

// Snapshot 是一次一致读取的原始历史数据，不包含覆盖率或未归属流量等派生统计。
type Snapshot struct {
	Instances   []HistoryInstance
	Buckets     []TrafficBucket
	Observed    map[string]int64
	Top         []Top
	Connections []Connection
	Gaps        []Gap
}

// HistoryInstance 保存历史身份与留存事实；已删除实例仍参与历史统计。
type HistoryInstance struct {
	ID, Name            string
	Deleted             bool
	CreatedAt           int64
	FirstRetainedMinute *int64
	FirstRetainedHour   *int64
	LastSnapshot        *int64
	GapCount            int64
}

// TrafficBucket 保存分钟或小时桶的原始计数，跨实例汇总由统计模块完成。
type TrafficBucket struct {
	InstanceID                           string
	Bucket                               int64
	Upload, Download                     int64
	AttributedUpload, AttributedDownload int64
	ObservedMS                           int64
	HasGap                               bool
}
