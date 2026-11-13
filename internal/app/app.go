package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
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

// Open 依次准备存储、组装业务模块和 HTTP 入口，最后启动后台任务。
// 返回时采集与写入任务已启动；HTTP 监听由调用方负责。
func Open(ctx context.Context, c config.Config, logger *slog.Logger) (*App, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	// 先准备数据库、加密密钥和管理员状态，失败时不启动后台任务。
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

	// 实例配置代次的取消信号由 Registry 统一提供给 Gateway 和采集器。
	root, cancel := context.WithCancel(ctx)
	policy := mihomo.Policy{
		AllowPublic: c.Gateway.AllowPublicControllers, AllowedPorts: c.Gateway.AllowedPorts,
	}
	registry, err := instance.New(root, &storage.Instances{DB: db, AEAD: aead}, policy)
	if err != nil {
		cancel()
		return nil, errors.Join(err, db.Close())
	}
	sessions := auth.New(root, admins, time.Duration(c.Security.SessionHours)*time.Hour)
	proxy := gateway.New(c, registry, logger)

	// 历史查询使用独立读池，采集样本通过 Writer 批量提交。
	reader, err := storage.OpenReader(root, c.Database.Path, c.Database.BusyTimeoutMS)
	if err != nil {
		cancel()
		closeErr := db.Close()
		proxy.Close()
		return nil, errors.Join(err, closeErr)
	}
	telemetryStore := storage.Telemetry{DB: db}
	writer := storage.NewWriter(
		c.Collector.QueueCapacity,
		c.Collector.BatchSize,
		time.Duration(c.Collector.FlushIntervalMS)*time.Millisecond,
		telemetryStore.Apply,
	)
	manager := collector.New(collector.Dependencies{
		Registry: registry, Store: telemetryStore, Writer: writer, Policy: policy, Logger: logger,
		Options: collector.Options{
			MaxInstances:    c.Collector.MaxInstances,
			MaxConnections:  c.Collector.MaxConnections,
			ReadLimit:       c.Gateway.MaxWSMessageBytes,
			SnapshotTimeout: time.Duration(c.Collector.SnapshotTimeoutSeconds) * time.Second,
			MaxInterval:     time.Duration(c.Collector.MaxSnapshotGapMS) * time.Millisecond,
			ReconnectMin:    time.Duration(c.Collector.ReconnectMinMS) * time.Millisecond,
			ReconnectMax:    time.Duration(c.Collector.ReconnectMaxMS) * time.Millisecond,
		},
	})
	// Writer 独立取消，确保采集器退出后仍可排空已接收的样本。
	writerCtx, stopWriter := context.WithCancel(context.WithoutCancel(root))
	a := &App{
		DB: db, reader: reader, cancel: cancel, gateway: proxy,
		managerDone: make(chan struct{}), writerDone: make(chan struct{}),
		maintenanceDone: make(chan struct{}), stopWriter: stopWriter,
	}

	// HTTP 入口只接收已装配的依赖；维护循环和诊断实现放在同包独立文件中。
	a.Handler = httpapi.New(httpapi.Dependencies{
		Config: c, Auth: sessions, Instances: registry, Gateway: proxy, Logger: logger,
		Ready: func(ctx context.Context) error { return storage.Ready(ctx, db) },
		Audit: admins.Audit, Static: web.Static(c.Server.StaticDir), Version: Version, Commit: Commit,
		FrontendCommit: FrontendCommit, BuildTime: BuildTime,
		FrontendEmbedded: web.Embedded && c.Server.StaticDir == "",
		Collector:        manager,
		RuntimeConfig:    runtimeconfig.New(registry, &storage.RuntimeConfigs{DB: db, AEAD: aead}, proxy),
		Analytics: analytics.Service{
			Repository:  storage.Analytics{DB: reader},
			MaxInterval: time.Duration(c.Collector.MaxSnapshotGapMS) * time.Millisecond,
		},
		SystemStatus: a.systemStatus(
			c.Database.Path,
			registry,
			manager,
			writer,
		),
	})

	// 所有依赖装配完成后，按写入器、采集器、维护任务的顺序启动。
	go func() { defer close(a.writerDone); writer.Run(writerCtx) }()
	go func() { defer close(a.managerDone); manager.Run(root) }()
	go a.runMaintenance(
		root,
		telemetryStore,
		storage.Retention{
			ConnectionsDays: c.Retention.ConnectionsDays,
			MinuteDays:      c.Retention.MinuteStatsDays,
			HourlyDays:      c.Retention.HourlyStatsDays,
		},
		logger,
	)
	return a, nil
}

var Version = "0.3.0"

// 发布脚本通过 ldflags 写入两个仓库的不可变版本，开发构建不得冒充正式版本。
var FrontendCommit = ""
var BuildTime = "unknown"
var Commit = "dev"

func (a *App) Close() error {
	a.once.Do(func() {
		// 先停止并等待样本生产者和维护任务，再让 Writer 排空，最后关闭连接和数据库。
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
