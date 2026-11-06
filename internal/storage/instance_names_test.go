//go:build integration

package storage_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/internal/telemetry"
	"github.com/longafterx/mihomo-hub/migrations"
	"github.com/pressly/goose/v3"
)

func TestActiveNameMigrationPreservesHistory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "schema2.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	old := fstest.MapFS{}
	for _, name := range []string{"001_init.sql", "002_telemetry.sql"} {
		data, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: data}
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, old, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"archived", "active"} {
		if _, err := db.Exec(`INSERT INTO instances VALUES(?,?,?,x'010203',1,1,1,2,NULL,7)`, id, id, "http://127.0.0.1:9090"); err != nil {
			t.Fatal(err)
		}
		e := telemetry.New(telemetry.Cursor{InstanceID: id}, 5*time.Second)
		baseline, e := plan(t, e, 60000, 100)
		baseline.Generation = 7
		mustApply(t, storage.Telemetry{DB: db}, baseline)
		batch, _ := plan(t, e, 61000, 120)
		batch.Generation = 7
		mustApply(t, storage.Telemetry{DB: db}, batch)
	}
	if _, err := db.Exec(`UPDATE instances SET deleted_at_ms=62000,enabled=0,collect_enabled=0,controller_url='',secret_ciphertext=x'' WHERE id='archived'`); err != nil {
		t.Fatal(err)
	}
	tables := []string{"instance_epochs", "collector_cursor", "collector_state", "traffic_minute", "traffic_hour", "connection_records", "traffic_dimension_minute", "traffic_dimension_hour", "traffic_gap_intervals"}
	counts := map[string]int64{}
	for _, table := range tables {
		counts[table] = scalar(t, db, "SELECT COUNT(*) FROM "+table)
		if counts[table] == 0 {
			t.Fatalf("fixture did not seed %s", table)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(t.Context(), path, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range tables {
		if got := scalar(t, db, "SELECT COUNT(*) FROM "+table); got != counts[table] {
			t.Fatalf("migration changed %s: %d -> %d", table, counts[table], got)
		}
	}
	if scalar(t, db, "PRAGMA foreign_keys") != 1 {
		t.Fatal("foreign keys not restored")
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	hasViolation := rows.Next()
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if hasViolation {
		t.Fatal("migration broke historical references")
	}
	if scalar(t, db, "SELECT SUM(total_upload) FROM traffic_minute") != 40 {
		t.Fatal("historical bytes lost")
	}
	if scalar(t, db, "SELECT generation FROM instances WHERE id='active'") != 7 {
		t.Fatal("generation lost")
	}
	var ciphertext string
	if err := db.QueryRow("SELECT hex(secret_ciphertext) FROM instances WHERE id='active'").Scan(&ciphertext); err != nil || ciphertext != "010203" {
		t.Fatal("ciphertext changed", err)
	}
	insert := `INSERT INTO instances(id,name,controller_url,secret_ciphertext,enabled,collect_enabled,created_at_ms,updated_at_ms) VALUES(?,?,'http://127.0.0.1:9090',x'',1,1,1,2)`
	if _, err := db.Exec(insert, "replacement", "archived"); err != nil {
		t.Fatal("retained name unavailable", err)
	}
	if _, err := db.Exec(insert, "duplicate", "active"); err == nil {
		t.Fatal("active name is no longer unique")
	}
	if scalar(t, db, "SELECT COUNT(*) FROM traffic_minute WHERE instance_id='replacement'") != 0 {
		t.Fatal("new instance inherited old history")
	}
	if _, err := db.Exec("DELETE FROM instances WHERE id='archived'"); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if scalar(t, db, "SELECT COUNT(*) FROM "+table+" WHERE instance_id='archived'") != 0 {
			t.Fatalf("cascade broken for %s", table)
		}
		if scalar(t, db, "SELECT COUNT(*) FROM "+table+" WHERE instance_id='active'") == 0 {
			t.Fatalf("cascade crossed identities for %s", table)
		}
	}
}
