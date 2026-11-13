package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/longafterx/mihomo-hub/internal/storage"
)

// runMaintenance 定期清理过期历史并执行被动 WAL checkpoint，退出后通知 Close。
func (a *App) runMaintenance(
	ctx context.Context,
	store storage.Telemetry,
	retention storage.Retention,
	logger *slog.Logger,
) {
	defer close(a.maintenanceDone)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := store.Prune(cleanupCtx, now, retention)
			if err == nil {
				_, err = a.DB.ExecContext(cleanupCtx, "PRAGMA wal_checkpoint(PASSIVE)")
			}
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Error("retention cleanup failed", "error_code", "storage_error")
			}
		}
	}
}
