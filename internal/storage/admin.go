package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/storage/generated"
)

type Admins struct{ DB *sql.DB }

func (r Admins) Admin(ctx context.Context) (auth.Admin, error) {
	row, err := generated.New(r.DB).GetAdmin(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Admin{}, auth.ErrSetupRequired
	}
	if err != nil {
		return auth.Admin{}, fmt.Errorf("read administrator: %w", err)
	}
	return auth.Admin{Username: row.Username, PasswordHash: row.PasswordHash}, nil
}

func (r Admins) Initialize(ctx context.Context, admin auth.Admin) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin administrator setup: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	// 主键冲突时不更新，数据库保证并发首次设置只有一个请求成功。
	rows, err := q.InitializeAdmin(ctx, generated.InitializeAdminParams{
		Username: admin.Username, PasswordHash: admin.PasswordHash,
	})
	if err != nil {
		return fmt.Errorf("initialize administrator: %w", err)
	}
	if rows == 0 {
		return auth.ErrAlreadyInitialized
	}
	// 账号和审计一起提交，避免记录失败时留下无法确认的半完成状态。
	if err := q.WriteAudit(ctx, generated.WriteAuditParams{
		Action: "auth.setup", Result: "success", AtMs: time.Now().UTC().UnixMilli(),
	}); err != nil {
		return fmt.Errorf("audit administrator setup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit administrator setup: %w", err)
	}
	return nil
}

func (r Admins) Audit(ctx context.Context, action, target, result string) error {
	err := generated.New(r.DB).WriteAudit(ctx, generated.WriteAuditParams{
		Action: action, TargetID: sql.NullString{String: target, Valid: target != ""},
		Result: result, AtMs: time.Now().UTC().UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}
