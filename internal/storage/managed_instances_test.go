//go:build integration

package storage_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/migrations"
	"github.com/pressly/goose/v3"
)

func TestManagedInstancesMigration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "schema3.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	old := fstest.MapFS{}
	for _, name := range []string{"001_init.sql", "002_telemetry.sql", "003_active_instance_names.sql"} {
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
	for _, id := range []string{"paused", "active", "archived"} {
		_, err := db.Exec(`INSERT INTO instances VALUES(?,?,?,x'010203',1,1,1,2,NULL,7)`, id, id, "http://127.0.0.1:9090")
		if err != nil {
			t.Fatal(err)
		}
		seedLegacyTelemetry(t, db, id)
	}
	if _, err := db.Exec(`UPDATE instances SET enabled=0,collect_enabled=0 WHERE id='paused'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instances SET enabled=0,collect_enabled=0,deleted_at_ms=62000,controller_url='',secret_ciphertext=x'' WHERE id='archived'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(t.Context(), path, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tt := range []struct {
		name       string
		active     bool
		generation int64
		ciphertext string
	}{
		{name: "paused", active: true, generation: 8, ciphertext: "010203"},
		{name: "active", active: true, generation: 7, ciphertext: "010203"},
		{name: "archived", generation: 7, ciphertext: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var active bool
			var generation int64
			var ciphertext string
			err := db.QueryRow(
				`SELECT deleted_at_ms IS NULL,generation,hex(secret_ciphertext) FROM instances WHERE id=?`, tt.name,
			).Scan(&active, &generation, &ciphertext)
			if err != nil {
				t.Fatal(err)
			}
			identityMatch := generation == tt.generation && ciphertext == tt.ciphertext
			if active != tt.active || !identityMatch {
				t.Fatalf("migration changed unexpected fields: active=%t generation=%d", active, generation)
			}
			var total int64
			if err := db.QueryRow("SELECT SUM(total_upload) FROM traffic_minute WHERE instance_id=?", tt.name).Scan(&total); err != nil || total != 20 {
				t.Fatalf("history changed: total=%d err=%v", total, err)
			}
		})
	}
	if scalar(t, db, "SELECT COUNT(*) FROM instances WHERE deleted_at_ms IS NOT NULL") != 1 {
		t.Fatal("retained tombstone lost")
	}
	if err := storage.Ready(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}
