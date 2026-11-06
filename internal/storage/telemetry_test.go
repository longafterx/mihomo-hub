//go:build integration

package storage_test

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/internal/telemetry"
)

func telemetryDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := storage.Open(t.Context(), path, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, id := range []string{"one", "two"} {
		if _, err := db.Exec(`INSERT INTO instances(id,name,controller_url,secret_ciphertext,enabled,collect_enabled,created_at_ms,updated_at_ms) VALUES(?,?,?,x'',1,1,0,0)`, id, id, "http://127.0.0.1:9090"); err != nil {
			t.Fatal(err)
		}
	}
	return db, path
}
func plan(t *testing.T, e *telemetry.Engine, at, total int64) (telemetry.Batch, *telemetry.Engine) {
	t.Helper()
	u, d := total, total*2
	cu, cd := total/2, total
	s := telemetry.Snapshot{Upload: &u, Download: &d, Connections: []telemetry.Connection{{ID: "shared", Upload: &cu, Download: &cd}}}
	b, next, err := e.Plan(s, at, "controller", "")
	if err != nil {
		t.Fatal(err)
	}
	b.Generation = 1
	return b, next
}
func mustApply(t *testing.T, s storage.Telemetry, b ...telemetry.Batch) {
	t.Helper()
	if err := s.Apply(t.Context(), b); err != nil {
		t.Fatal(err)
	}
}
func scalar(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTelemetryAtomicReplayAndGeneration(t *testing.T) {
	t.Parallel()
	db, _ := telemetryDB(t)
	s := storage.Telemetry{DB: db}
	e := telemetry.New(telemetry.Cursor{InstanceID: "one"}, 5*time.Second)
	baseline, e := plan(t, e, 60000, 100)
	mustApply(t, s, baseline)
	b, e := plan(t, e, 61000, 120)
	if _, err := db.Exec(`CREATE TRIGGER fail_cursor BEFORE UPDATE ON collector_cursor BEGIN SELECT RAISE(ABORT,'injected cursor failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(t.Context(), []telemetry.Batch{b}); err == nil {
		t.Fatal("injected failure accepted")
	}
	if n := scalar(t, db, "SELECT SUM(total_upload) FROM traffic_minute WHERE instance_id='one'"); n != 0 {
		t.Fatal("partial aggregate committed", n)
	}
	cursor, err := s.LoadCursor(t.Context(), "one")
	if err != nil || cursor.At != 60000 {
		t.Fatal(cursor, err)
	}
	if _, err := db.Exec("DROP TRIGGER fail_cursor"); err != nil {
		t.Fatal(err)
	}
	mustApply(t, s, b, b, baseline)
	if n := scalar(t, db, "SELECT SUM(total_upload) FROM traffic_hour"); n != 20 {
		t.Fatal("non-idempotent hour", n)
	}
	b, _ = plan(t, e, 62000, 140)
	b.Generation = 0
	mustApply(t, s, b)
	if n := scalar(t, db, "SELECT SUM(total_upload) FROM traffic_hour"); n != 20 {
		t.Fatal("old generation committed", n)
	}
	e2 := telemetry.New(telemetry.Cursor{InstanceID: "two"}, 5*time.Second)
	b, _ = plan(t, e2, 61000, 900)
	mustApply(t, s, b)
	if n := scalar(t, db, "SELECT COUNT(*) FROM connection_records WHERE connection_id='shared'"); n != 2 {
		t.Fatal("cross-instance connection collision", n)
	}
}

func TestSQLiteIntegerOverflowRollsBack(t *testing.T) {
	t.Parallel()
	db, _ := telemetryDB(t)
	s := storage.Telemetry{DB: db}
	e := telemetry.New(telemetry.Cursor{InstanceID: "one"}, 5*time.Second)
	b, e := plan(t, e, 60000, 100)
	mustApply(t, s, b)
	if _, err := db.Exec("UPDATE traffic_minute SET total_upload=?", int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	b, _ = plan(t, e, 61000, 120)
	if err := s.Apply(t.Context(), []telemetry.Batch{b}); err == nil {
		t.Fatal("overflow converted to REAL")
	}
	cursor, err := s.LoadCursor(t.Context(), "one")
	if err != nil || cursor.At != 60000 {
		t.Fatal("overflow advanced cursor", cursor, err)
	}
}

func TestOnlineBackupReadPoolAndRetention(t *testing.T) {
	t.Parallel()
	db, path := telemetryDB(t)
	s := storage.Telemetry{DB: db}
	e := telemetry.New(telemetry.Cursor{InstanceID: "one"}, 5*time.Second)
	b, e := plan(t, e, 60000, 100)
	mustApply(t, s, b)
	b, _ = plan(t, e, 61000, 120)
	mustApply(t, s, b)
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatal("WAL not present", err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(t.Context(), backup); err == nil {
		t.Fatal("backup overwrote destination")
	}
	info, err := os.Stat(backup)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions", err)
	}
	restored, err := storage.Open(t.Context(), backup, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if n := scalar(t, restored, "SELECT SUM(total_upload) FROM traffic_minute"); n != 20 {
		t.Fatal("WAL not backed up", n)
	}
	reader, err := storage.OpenReader(t.Context(), path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var conns []*sql.Conn
	for range 4 {
		conn, err := reader.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		if _, err := conn.ExecContext(t.Context(), "DELETE FROM instances"); err == nil {
			t.Fatal("reader permitted writes")
		}
	}
	for _, conn := range conns {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	now := time.UnixMilli(61 * 24 * time.Hour.Milliseconds())
	if _, err := db.Exec("UPDATE connection_records SET closed_at_ms=last_seen_at_ms WHERE instance_id='one'"); err != nil {
		t.Fatal(err)
	}
	e2 := telemetry.New(telemetry.Cursor{InstanceID: "two"}, 5*time.Second)
	b, _ = plan(t, e2, 61000, 900)
	mustApply(t, s, b)
	if err := s.Prune(t.Context(), now, storage.Retention{ConnectionsDays: 14, MinuteDays: 60, HourlyDays: 365}); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, db, "SELECT COUNT(*) FROM traffic_minute"); n != 0 {
		t.Fatal("minute retention", n)
	}
	if n := scalar(t, db, "SELECT COUNT(*) FROM traffic_hour"); n != 2 {
		t.Fatal("hour retention", n)
	}
	if n := scalar(t, db, "SELECT COUNT(*) FROM connection_records"); n != 1 {
		t.Fatal("active retention", n)
	}
}

func TestBusyFailureDoesNotAdvanceCursor(t *testing.T) {
	t.Parallel()
	db, path := telemetryDB(t)
	s := storage.Telemetry{DB: db}
	other, err := storage.Open(t.Context(), path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE instances SET updated_at_ms=1"); err != nil {
		t.Fatal(err)
	}
	b, _ := plan(t, telemetry.New(telemetry.Cursor{InstanceID: "one"}, 5*time.Second), 60000, 100)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := s.Apply(ctx, []telemetry.Batch{b}); err == nil {
		t.Fatal("locked writer succeeded")
	}
	c, err := s.LoadCursor(t.Context(), "one")
	if err != nil || c.At != 0 {
		t.Fatal("busy advanced cursor", c, err)
	}
}

func TestRetentionBoundaryAndMultipleSmallBatches(t *testing.T) {
	t.Parallel()
	db, _ := telemetryDB(t)
	cutoff := int64(60000)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for n := range 2001 {
		if _, err := tx.Exec("INSERT INTO traffic_dimension_minute VALUES('one',?,'host',?,1,1)", cutoff-1, n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec("INSERT INTO traffic_dimension_minute VALUES('one',?,'host','boundary',1,1)", cutoff); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(cutoff).Add(60 * 24 * time.Hour)
	if err := (storage.Telemetry{DB: db}).Prune(t.Context(), now, storage.Retention{ConnectionsDays: 14, MinuteDays: 60, HourlyDays: 365}); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, db, "SELECT COUNT(*) FROM traffic_dimension_minute"); n != 1 {
		t.Fatal("retention boundary or backlog", n)
	}
}

func TestPurgeCascadesAllHistory(t *testing.T) {
	t.Parallel()
	db, _ := telemetryDB(t)
	store := storage.Telemetry{DB: db}
	for _, id := range []string{"one", "two"} {
		b, _ := plan(t, telemetry.New(telemetry.Cursor{InstanceID: id}, 5*time.Second), 60000, 100)
		mustApply(t, store, b)
	}
	if err := (&storage.Instances{DB: db}).Delete(t.Context(), "one", "purge"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"instance_epochs", "collector_cursor", "collector_state", "traffic_minute", "traffic_hour", "connection_records", "traffic_gap_intervals"} {
		if n := scalar(t, db, "SELECT COUNT(*) FROM "+table+" WHERE instance_id='one'"); n != 0 {
			t.Fatal(table, n)
		}
		if n := scalar(t, db, "SELECT COUNT(*) FROM "+table+" WHERE instance_id='two'"); n != 1 {
			t.Fatal("other instance affected", table, n)
		}
	}
}
