//go:build integration

package storage_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/storage"
)

func TestMigrationsAndReadiness(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "hub.db")
	for range 2 {
		db, err := storage.Open(t.Context(), path, 5000)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Ready(t.Context(), db); err != nil {
			t.Fatal(err)
		}
		var version int
		if err := db.QueryRow("SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != storage.SchemaVersion {
			t.Fatalf("schema = %d", version)
		}
		var mode string
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal = %s, error = %v", mode, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := storage.Ready(t.Context(), db); err == nil {
			t.Fatal("closed database reports ready")
		}
	}
}

func TestConnectionPragmasAndFutureSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := storage.Open(t.Context(), path, 1234)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(3)
	connections := []*sql.Conn{}
	for range 3 {
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
		var fk, busy int
		if err := conn.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if fk != 1 || busy != 1234 {
			t.Fatalf("connection pragmas fk=%d busy=%d", fk, busy)
		}
	}
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO goose_db_version(version_id,is_applied) VALUES(999,1)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := storage.Open(t.Context(), path, 5000); err == nil {
		reopened.Close()
		t.Fatal("future schema was accepted")
	}
}
