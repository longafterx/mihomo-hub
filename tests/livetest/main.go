// Command livetest reads Controller credentials from stdin, uses only the
// read-only /connections stream, and removes its temporary history on exit.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/longafterx/mihomo-hub/internal/app"
	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/collector"
	"github.com/longafterx/mihomo-hub/internal/config"
	"github.com/longafterx/mihomo-hub/internal/storage"
)

type target struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Secret string `json:"secret"`
}
type report struct {
	Name         string `json:"name"`
	Samples      uint64 `json:"samples_before_restart"`
	SamplesAfter uint64 `json:"samples_after_restart"`
	RecoveryGap  bool   `json:"recovery_gap"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "live validation failed:", err)
		os.Exit(1)
	}
}
func run() error {
	gin.SetMode(gin.ReleaseMode)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var targets []target
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&targets); err != nil {
		return errors.New("invalid target input")
	}
	if len(targets) < 2 {
		return errors.New("need at least two targets")
	}
	dir, err := os.MkdirTemp("", "mihomohub-live-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	c := config.Default()
	c.Database.Path = filepath.Join(dir, "hub.db")
	c.Security.MasterKeyFile = filepath.Join(dir, "master.key")
	c.Collector.FlushIntervalMS = 25
	if err := storage.InitKey(c.Security.MasterKeyFile); err != nil {
		return err
	}
	password := rand.Text()
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	db, err := storage.Open(ctx, c.Database.Path, 5000)
	if err != nil {
		return err
	}
	err = (storage.Admins{DB: db}).Initialize(ctx, auth.Admin{Username: "live-test", PasswordHash: hash})
	closeErr := db.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	c.Server.PublicOrigin = "http://" + listener.Addr().String()
	a, err := app.Open(ctx, c, logger)
	if err != nil {
		return err
	}
	defer func() { a.Close() }()
	server := httptest.NewUnstartedServer(a.Handler)
	server.Listener = listener
	server.Start()
	defer func() { server.Close() }()
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	csrf := ""
	request := func(method, path string, body any, dst any) error {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("Origin", c.Server.PublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		res, err := client.Do(req)
		if err != nil {
			return errors.New("platform request failed")
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return fmt.Errorf("platform status %d", res.StatusCode)
		}
		return json.NewDecoder(res.Body).Decode(dst)
	}
	login := func() error {
		var out struct {
			Data struct {
				CSRF string `json:"csrf_token"`
			} `json:"data"`
		}
		if err := request("POST", "/api/v1/auth/login", map[string]string{

			"password": password,
		}, &out); err != nil {
			return err
		}
		csrf = out.Data.CSRF
		return nil
	}
	if err := login(); err != nil {
		return err
	}
	ids := []string{}
	reports := []report{}
	for _, target := range targets {
		var out struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := request("POST", "/api/v1/instances", map[string]any{
			"name":            target.Name,
			"controller_url":  target.URL,
			"secret":          target.Secret,
			"collect_enabled": true,
		}, &out); err != nil {
			return err
		}
		ids = append(ids, out.Data.ID)
		reports = append(reports, report{Name: target.Name})
	}
	sample := func(id string) (collector.Status, error) {
		var out struct {
			Data collector.Status `json:"data"`
		}
		err := request("GET", "/api/v1/instances/"+id+"/status", nil, &out)
		return out.Data, err
	}
	await := func(minimum uint64, after bool) error {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			all := true
			for i, id := range ids {
				state, err := sample(id)
				if err != nil {
					return err
				}
				if state.SamplesCommitted < minimum || state.Status != "online" {
					all = false
				}
				if after {
					reports[i].SamplesAfter = state.SamplesCommitted
				} else {
					reports[i].Samples = state.SamplesCommitted
				}
			}
			if all {
				return nil
			}
			select {
			case <-ctx.Done():
				return errors.New("collector did not reach sampling target")
			case <-ticker.C:
			}
		}
	}
	if err := await(10, false); err != nil {
		return err
	}
	if err := a.Close(); err != nil {
		return err
	}
	server.Close()
	listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	c.Server.PublicOrigin = "http://" + listener.Addr().String()
	a, err = app.Open(ctx, c, logger)
	if err != nil {
		return err
	}
	server = httptest.NewUnstartedServer(a.Handler)
	server.Listener = listener
	server.Start()
	csrf = ""
	if err := login(); err != nil {
		return err
	}
	if err := await(5, true); err != nil {
		return err
	}
	for i, id := range ids {
		var n int
		if err := a.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM traffic_gap_intervals WHERE instance_id=? AND reason='process_or_collector_restart' AND candidate_upload_bytes IS NOT NULL", id).Scan(&n); err != nil {
			return err
		}
		reports[i].RecoveryGap = n > 0
		if n == 0 {
			return errors.New("missing restart recovery gap")
		}
	}
	var diagnostics struct {
		Data struct {
			Writer storage.WriterMetrics `json:"writer"`
		} `json:"data"`
	}
	if err := request("GET", "/api/v1/system/status", nil, &diagnostics); err != nil {
		return err
	}
	if diagnostics.Data.Writer.WriteFailures != 0 || diagnostics.Data.Writer.DroppedSamples != 0 {
		return errors.New("live writer failed or dropped samples")
	}
	if err := a.Close(); err != nil {
		return err
	}
	server.Close()
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"instances":                         reports,
		"read_only_upstream":                true,
		"temporary_history_removed_on_exit": true,
		"writer":                            diagnostics.Data.Writer,
	})
}
