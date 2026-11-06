//go:build integration

package storage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/longafterx/mihomo-hub/internal/analytics"
	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/internal/telemetry"
)

func TestHistoricalQueriesIsolationCoverageAndRetainedDeletion(t *testing.T) {
	t.Parallel()
	db, path := telemetryDB(t)
	store := storage.Telemetry{DB: db}
	for _, id := range []string{"one", "two"} {
		e := telemetry.New(telemetry.Cursor{InstanceID: id}, 5*time.Second)
		b, e := plan(t, e, 60000, 100)
		mustApply(t, store, b)
		b, _ = plan(t, e, 61000, 120)
		mustApply(t, store, b)
	}
	reader, err := storage.OpenReader(t.Context(), path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	repo := storage.Analytics{DB: reader, MaxInterval: 5 * time.Second}
	query := analytics.Query{Kind: "summary", From: 60000, To: 120000, Step: "1m", Limit: 10}
	result, err := repo.Query(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	totals := result.Data.(analytics.Totals)
	if totals.Upload != 40 || totals.AttributedUpload != 20 || *totals.UnattributedUpload != 20 {
		t.Fatal(totals)
	}
	if len(result.Meta.Coverage) != 2 || result.Meta.Coverage[0].ObservedMS != 1000 || !result.Meta.HasGap {
		t.Fatal(result.Meta)
	}
	query.IDs = []string{"one"}
	query.Kind = "traffic"
	result, err = repo.Query(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	points := result.Data.([]analytics.Point)
	if len(points) != 1 || points[0].Upload != 20 || points[0].ObservedMS != 1000 {
		t.Fatal(points)
	}
	query.Kind = "top"
	query.Dimension = "host"
	result, err = repo.Query(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	top := result.Data.([]analytics.Top)
	if len(top) != 1 || top[0].Upload != 10 {
		t.Fatal(top)
	}
	query.Kind = "connections"
	result, err = repo.Query(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	records := result.Data.([]analytics.Connection)
	if len(records) != 1 || records[0].Upload != 60 || records[0].InstanceID != "one" {
		t.Fatal(records)
	}
	query.Kind = "gaps"
	result, err = repo.Query(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	gaps := result.Data.([]analytics.Gap)
	if len(gaps) != 2 || gaps[0].To != nil || result.Meta.GapCount != 2 {
		t.Fatal("open gap absent", gaps)
	}
	if _, err := db.Exec("UPDATE instances SET deleted_at_ms=62000,enabled=0,collect_enabled=0,controller_url='',secret_ciphertext=x'' WHERE id='one'"); err != nil {
		t.Fatal(err)
	}
	query.Kind = "coverage"
	result, err = repo.Query(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Meta.Coverage[0].Deleted {
		t.Fatal("retained tombstone inaccessible")
	}
	query.Kind = "summary"
	query.Step = "1h"
	query.From = 0
	query.To = 3600000
	result, err = repo.Query(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if result.Data.(analytics.Totals).Upload != 20 {
		t.Fatal("hour aggregation", result)
	}
	query.IDs = []string{"unknown"}
	if _, err := repo.Query(t.Context(), query); !errors.Is(err, analytics.ErrNotFound) {
		t.Fatal(err)
	}
}
