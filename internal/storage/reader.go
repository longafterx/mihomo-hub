package storage

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"strconv"
)

func OpenReader(ctx context.Context, path string, busyMS int) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := url.Values{"mode": []string{"ro"}}
	q.Add("_pragma", "busy_timeout("+strconv.Itoa(busyMS)+")")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
