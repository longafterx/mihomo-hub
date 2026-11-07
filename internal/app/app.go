package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/longafterx/mihomo-hub/internal/analytics"
	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/collector"
	"github.com/longafterx/mihomo-hub/internal/config"
	"github.com/longafterx/mihomo-hub/internal/gateway"
	"github.com/longafterx/mihomo-hub/internal/httpapi"
	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/mihomo"
	"github.com/longafterx/mihomo-hub/internal/runtimeconfig"
	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/internal/web"
)

type App struct {
	Handler         http.Handler
	DB              *sql.DB
	cancel          context.CancelFunc
	gateway         *gateway.Gateway
	reader          *sql.DB
	managerDone     chan struct{}
	writerDone      chan struct{}
	maintenanceDone chan struct{}
	stopWriter      context.CancelFunc
	once            sync.Once
	closeErr        error
}

func Open(ctx context.Context, c config.Config, logger *slog.Logger) (*App, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	db, err := storage.Open(ctx, c.Database.Path, c.Database.BusyTimeoutMS)
	if err != nil {
		return nil, err
	}
	aead, err := storage.PrepareKey(ctx, db, c.Security.MasterKeyFile)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	admins := storage.Admins{DB: db}
	// 空数据库进入网页密码设置；数据库读取失败仍应阻止启动。
	if _, err := admins.Admin(ctx); err != nil && !errors.Is(err, auth.ErrSetupRequired) {
		return nil, errors.Join(err, db.Close())
	}
	root, cancel := context.WithCancel(ctx)
	registry, err := instance.New(root, &storage.Instances{DB: db, AEAD: aead}, mihomo.Policy{
		AllowPublic: c.Gateway.AllowPublicControllers, AllowedPorts: c.Gateway.AllowedPorts,
	})
	if err != nil {
		cancel()
		return nil, errors.Join(err, db.Close())
	}
	sessions := auth.New(root, admins, time.Duration(c.Security.SessionHours)*time.Hour)
	proxy := gateway.New(c, registry, logger)
	reader, err := storage.OpenReader(root, c.Database.Path, c.Database.BusyTimeoutMS)
	if err != nil {
		cancel()
		closeErr := db.Close()
		proxy.Close()
		return nil, errors.Join(err, closeErr)
	}
	telemetryStore := storage.Telemetry{DB: db}
	writer := storage.NewWriter(c.Collector.QueueCapacity, c.Collector.BatchSize,
		time.Duration(c.Collector.FlushIntervalMS)*time.Millisecond, telemetryStore.Apply)
	manager := collector.New(collector.Dependencies{Registry: registry, Store: telemetryStore, Writer: writer,
		Policy: mihomo.Policy{
			AllowPublic:  c.Gateway.AllowPublicControllers,
			AllowedPorts: c.Gateway.AllowedPorts,
		}, Logger: logger,
		Options: collector.Options{MaxInstances: c.Collector.MaxInstances, MaxConnections: c.Collector.MaxConnections,
			ReadLimit: c.Gateway.MaxWSMessageBytes, SnapshotTimeout: time.Duration(c.Collector.SnapshotTimeoutSeconds) * time.Second,
			MaxInterval:  time.Duration(c.Collector.MaxSnapshotGapMS) * time.Millisecond,
			ReconnectMin: time.Duration(c.Collector.ReconnectMinMS) * time.Millisecond,
			ReconnectMax: time.Duration(c.Collector.ReconnectMaxMS) * time.Millisecond},
	})
	writerCtx, stopWriter := context.WithCancel(context.WithoutCancel(root))
	a := &App{DB: db, reader: reader, cancel: cancel, gateway: proxy, managerDone: make(chan struct{}),
		writerDone: make(chan struct{}), maintenanceDone: make(chan struct{}), stopWriter: stopWriter}
	go func() { defer close(a.writerDone); writer.Run(writerCtx) }()
	go func() { defer close(a.managerDone); manager.Run(root) }()
	go func() {
		defer close(a.maintenanceDone)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-root.Done():
				return
			case now := <-ticker.C:
				ctx, cancel := context.WithTimeout(root, 5*time.Second)
				err := telemetryStore.Prune(ctx, now, storage.Retention{ConnectionsDays: c.Retention.ConnectionsDays,
					MinuteDays: c.Retention.MinuteStatsDays, HourlyDays: c.Retention.HourlyStatsDays})
				if err == nil {
					_, err = db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
				}
				cancel()
				if err != nil && root.Err() == nil {
					logger.Error("retention cleanup failed", "error_code", "storage_error")
				}
			}
		}
	}()
	handler := httpapi.New(httpapi.Dependencies{
		Config: c, Auth: sessions, Instances: registry, Gateway: proxy, Logger: logger,
		Ready: func(ctx context.Context) error { return storage.Ready(ctx, db) },
		Audit: admins.Audit, Static: web.Static(c.Server.StaticDir), Version: Version, Commit: Commit,
		FrontendCommit: FrontendCommit, BuildTime: BuildTime, FrontendEmbedded: web.Embedded && c.Server.StaticDir == "",
		Collector:     manager,
		RuntimeConfig: runtimeconfig.New(registry, &storage.RuntimeConfigs{DB: db, AEAD: aead}, proxy),
		Analytics: analytics.Service{
			Repository: storage.Analytics{DB: reader, MaxInterval: time.Duration(c.Collector.MaxSnapshotGapMS) * time.Millisecond},
		},
		SystemStatus: func(ctx context.Context) (any, error) {
			var databaseSize int64
			for _, path := range []string{c.Database.Path, c.Database.Path + "-wal", c.Database.Path + "-shm"} {
				info, err := os.Stat(path)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return nil, err
				}
				databaseSize += info.Size()
			}
			states := map[string]collector.Status{}
			for _, item := range registry.List() {
				if item.Enabled && item.CollectEnabled {
					states[item.ID] = manager.Status(item.ID, item.Generation)
				} else {
					states[item.ID] = collector.Status{Status: "disabled"}
				}
			}
			return map[string]any{
				"version":               Version,
				"schema_version":        storage.SchemaVersion,
				"database_bytes":        databaseSize,
				"writer":                writer.Metrics(),
				"collectors":            states,
				"write_pool_wait_count": db.Stats().WaitCount,
				"write_pool_wait_ms":    db.Stats().WaitDuration.Milliseconds(),
			}, nil
		},
	})
	a.Handler = handler
	return a, nil
}

var Version = "0.3.0"

// 发布脚本通过 ldflags 写入两个仓库的不可变版本，开发构建不得冒充正式版本。
var FrontendCommit = ""
var BuildTime = "unknown"
var Commit = "dev"

func (a *App) Close() error {
	a.once.Do(func() {
		a.cancel()
		<-a.managerDone
		<-a.maintenanceDone
		a.stopWriter()
		<-a.writerDone
		a.gateway.Close()
		a.closeErr = errors.Join(a.reader.Close(), a.DB.Close())
	})
	return a.closeErr
}
