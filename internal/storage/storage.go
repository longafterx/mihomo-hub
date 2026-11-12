package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/longafterx/mihomo-hub/migrations"
	"github.com/pressly/goose/v3"
	// Register the database/sql driver once, at the storage composition boundary.
	_ "modernc.org/sqlite"
)

const SchemaVersion = 6

func Open(ctx context.Context, path string, busyMS int) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("create database file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close database file: %w", err)
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout("+strconv.Itoa(busyMS)+")")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Serialize registry/auth, telemetry and maintenance writes on one connection.
	// Analytics uses an independent read-only pool.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := migrate(ctx, db); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.Files,
		goose.WithDisableGlobalRegistry(true),
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		return fmt.Errorf("initialize migrations: %w", err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > SchemaVersion {
		return errors.New("database schema is newer than this binary; downgrade refused")
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func Ready(ctx context.Context, db *sql.DB) error {
	// A rollback exercises the write path without adding persistent probe traffic.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin readiness probe: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE readiness_probe SET value = value + 1 WHERE id = 1"); err != nil {
		return fmt.Errorf("write readiness probe: %w", err)
	}
	var value int
	if err := tx.QueryRowContext(ctx, "SELECT value FROM readiness_probe WHERE id = 1").Scan(&value); err != nil {
		return fmt.Errorf("read readiness probe: %w", err)
	}
	return nil
}
