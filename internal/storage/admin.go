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
	if err != nil {
		return auth.Admin{}, fmt.Errorf("read administrator (run init first): %w", err)
	}
	return auth.Admin{Username: row.Username, PasswordHash: row.PasswordHash}, nil
}

func (r Admins) Initialize(ctx context.Context, admin auth.Admin) error {
	q := generated.New(r.DB)
	count, err := q.CountAdmins(ctx)
	if err != nil {
		return fmt.Errorf("check initialized account: %w", err)
	}
	if count != 0 {
		return errors.New("administrator already initialized; refusing to overwrite")
	}
	if err := q.InitializeAdmin(ctx, generated.InitializeAdminParams{
		Username: admin.Username, PasswordHash: admin.PasswordHash,
	}); err != nil {
		return fmt.Errorf("initialize administrator: %w", err)
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
