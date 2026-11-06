//go:build integration

// Local, ephemeral test fixture only. Never use this command for deployment.
package main

import (
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
	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/mihomo"
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
	defer db.Close()
	password := "p0-smoke-fixture-only"
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := (storage.Admins{DB: db}).Initialize(ctx, auth.Admin{Username: "admin", PasswordHash: hash}); err != nil {
		return err
	}
	aead, err := storage.LoadKey(c.Security.MasterKeyFile)
	if err != nil {
		return err
	}
	registry, err := instance.New(ctx, &storage.Instances{DB: db, AEAD: aead}, mihomo.Policy{})
	if err != nil {
		return err
	}
	// 在 App 启动采集器之前完成夹具播种，避免历史样本与实时写入竞争。

	ids := []string{}
	for i := range 2 {
		fake := fakemihomo.New(fmt.Sprintf("smoke-%d", i), fmt.Sprintf("never-in-browser-secret-%d", i))
		defer fake.Close()
		item, err := registry.Create(ctx, instance.Create{Name: fake.Label, ControllerURL: fake.Server.URL, Secret: fake.Secret})
		if err != nil {
			return err
		}

		// 夹具写入五分钟前的完整分钟，确保与实时采集隔开，跨分钟也能验证孤立点。
		engine := telemetry.New(telemetry.Cursor{InstanceID: item.ID}, 5*time.Second)
		at := time.Now().UTC().Truncate(time.Minute).Add(-5 * time.Minute).UnixMilli()
		for sample := range 2 {
			upload, download := int64(sample*(i+1)*10000), int64(sample*(i+1)*20000)
			// 多个维度各有不同流量，供排行、搜索和排序的浏览器回归使用。
			connections := []telemetry.Connection{}
			for index, host := range []string{"history.example", "build.example", "packages.example"} {
				divisor := int64(2 << index)
				cu, cd := upload/divisor, download/divisor
				connection := telemetry.Connection{ID: fmt.Sprintf("history-%d", index), Upload: &cu, Download: &cd, Chains: []string{"DIRECT"}, Rule: "Domain"}
				connection.Metadata.Host = host
				connection.Metadata.SourceIP = fmt.Sprintf("192.168.1.%d", 10+index)
				connection.Metadata.Process = []string{"browser", "builder", "updater"}[index]
				connections = append(connections, connection)
			}
			batch, next, err := engine.Plan(telemetry.Snapshot{Upload: &upload, Download: &download, Connections: connections}, at+int64(sample)*1000, telemetry.SourceID(fake.Server.URL), "")
			if err != nil {
				return err
			}
			batch.Generation = item.Generation
			if err := (storage.Telemetry{DB: db}).Apply(ctx, []telemetry.Batch{batch}); err != nil {
				return err
			}
			engine = next
		}
		var rows int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM traffic_minute WHERE instance_id=?", item.ID).Scan(&rows); err != nil || rows == 0 {
			return fmt.Errorf("fixture history missing: %w", err)
		}
		ids = append(ids, item.ID)
	}
	candidate := fakemihomo.New("candidate-no-secret", "")
	defer candidate.Close()

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
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"url": c.Server.PublicOrigin, "ids": ids, "candidate_url": candidate.Server.URL}); err != nil {
		return err
	}
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
