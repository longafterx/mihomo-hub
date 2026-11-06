//go:build integration

// Local, ephemeral test fixture only. Never use this command for deployment.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/longafterx/mihomo-hub/internal/app"
	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/config"
	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/internal/telemetry"
	"github.com/longafterx/mihomo-hub/internal/testutil/fakemihomo"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	static := flag.String("static", "", "path to the unchanged XD static build")
	flag.Parse()
	gin.SetMode(gin.ReleaseMode)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir, err := os.MkdirTemp("", "mihomohub-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	c := config.Default()
	c.Database.Path = filepath.Join(dir, "hub.db")
	c.Security.MasterKeyFile = filepath.Join(dir, "master.key")
	c.Server.StaticDir = *static
	if err := storage.InitKey(c.Security.MasterKeyFile); err != nil {
		return err
	}
	db, err := storage.Open(ctx, c.Database.Path, 5000)
	if err != nil {
		return err
	}
	password := "p0-smoke-fixture-only"
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := (storage.Admins{DB: db}).Initialize(ctx, auth.Admin{Username: "admin", PasswordHash: hash}); err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	c.Server.PublicOrigin = "http://" + l.Addr().String()
	a, err := app.Open(ctx, c, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		l.Close()
		return err
	}
	defer a.Close()
	srv := &http.Server{Handler: a.Handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	client := &http.Client{Timeout: 5 * time.Second}
	var cookie *http.Cookie
	var csrf string
	request := func(path string, body any) (json.RawMessage, error) {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r, err := http.NewRequestWithContext(ctx, "POST", c.Server.PublicOrigin+path, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Origin", c.Server.PublicOrigin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		resp, err := client.Do(r)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("fixture bootstrap status %d", resp.StatusCode)
		}
		for _, item := range resp.Cookies() {
			if item.Name == auth.CookieName {
				cookie = item
			}
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return nil, err
		}
		return envelope.Data, nil
	}
	login, err := request("/api/v1/auth/login", map[string]string{"username": "admin", "password": password})
	if err != nil {
		return err
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login, &session); err != nil {
		return err
	}
	csrf = session.CSRF
	ids := []string{}
	for i := range 2 {
		fake := fakemihomo.New(fmt.Sprintf("smoke-%d", i), fmt.Sprintf("never-in-browser-secret-%d", i))
		defer fake.Close()
		data, err := request("/api/v1/instances", map[string]string{
			"name": fake.Label, "controller_url": fake.Server.URL, "secret": fake.Secret,
		})
		if err != nil {
			return err
		}
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(data, &item); err != nil {
			return err
		}
		// 浏览器测试固定写入上一个完整分钟，避免等待墙上时钟跨桶。
		engine := telemetry.New(telemetry.Cursor{InstanceID: item.ID}, 5*time.Second)
		at := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute).UnixMilli()
		var generation int64
		if err := a.DB.QueryRowContext(ctx, "SELECT generation FROM instances WHERE id=?", item.ID).Scan(&generation); err != nil {
			return err
		}
		// 仅测试夹具：注册表内仍为未采集状态，临时打开存储写入门以播种历史。
		if _, err := a.DB.ExecContext(ctx, "UPDATE instances SET collect_enabled=1 WHERE id=?", item.ID); err != nil {
			return err
		}
		for sample := range 2 {
			upload, download := int64(sample*(i+1)*10000), int64(sample*(i+1)*20000)
			cu, cd := upload/2, download/2
			connection := telemetry.Connection{ID: "shared-history", Upload: &cu, Download: &cd, Chains: []string{"DIRECT"}, Rule: "Domain"}
			connection.Metadata.Host = "history.example"
			batch, next, err := engine.Plan(telemetry.Snapshot{Upload: &upload, Download: &download, Connections: []telemetry.Connection{connection}}, at+int64(sample)*1000, telemetry.SourceID(fake.Server.URL), "")
			if err != nil {
				return err
			}
			batch.Generation = generation
			if err := (storage.Telemetry{DB: a.DB}).Apply(ctx, []telemetry.Batch{batch}); err != nil {
				return err
			}
			engine = next
		}
		if _, err := a.DB.ExecContext(ctx, "UPDATE instances SET collect_enabled=0 WHERE id=?", item.ID); err != nil {
			return err
		}
		var rows int
		if err := a.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM traffic_minute WHERE instance_id=?", item.ID).Scan(&rows); err != nil || rows == 0 {
			return fmt.Errorf("fixture history missing: %w", err)
		}
		ids = append(ids, item.ID)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"url": c.Server.PublicOrigin, "ids": ids}); err != nil {
		return err
	}
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
