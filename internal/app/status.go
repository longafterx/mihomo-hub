package app

import (
	"context"
	"errors"
	"os"

	"github.com/longafterx/mihomo-hub/internal/collector"
	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/storage"
)

// systemStatus 装配只读诊断回调；采集状态按实例当前配置代次读取。
func (a *App) systemStatus(
	databasePath string,
	registry *instance.Service,
	manager *collector.Manager,
	writer *storage.Writer,
) func(context.Context) (any, error) {
	return func(_ context.Context) (any, error) {
		var databaseSize int64
		// WAL 与共享内存也是 SQLite 占用的一部分；文件尚未创建时跳过。
		for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
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
			states[item.ID] = manager.Status(item.ID, item.Generation)
		}
		return map[string]any{
			"version":               Version,
			"schema_version":        storage.SchemaVersion,
			"database_bytes":        databaseSize,
			"writer":                writer.Metrics(),
			"collectors":            states,
			"write_pool_wait_count": a.DB.Stats().WaitCount,
			"write_pool_wait_ms":    a.DB.Stats().WaitDuration.Milliseconds(),
		}, nil
	}
}
