package storage

import (
	"context"
	"crypto/cipher"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/storage/generated"
	"modernc.org/sqlite"
)

type Instances struct {
	DB   *sql.DB
	AEAD cipher.AEAD
}

func (r *Instances) ListRetained(ctx context.Context) ([]instance.Retained, error) {
	rows, err := generated.New(r.DB).ListRetainedInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("query retained instances: %w", err)
	}
	items := make([]instance.Retained, 0, len(rows))
	for _, row := range rows {
		items = append(items, instance.Retained{ID: row.ID, Name: row.Name, DeletedAtMS: row.DeletedAtMs.Int64})
	}
	return items, nil
}

func (r *Instances) List(ctx context.Context) ([]instance.Instance, error) {
	rows, err := generated.New(r.DB).ListInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("query instances: %w", err)
	}
	items := make([]instance.Instance, 0, len(rows))
	for _, row := range rows {
		secret, err := Decrypt(r.AEAD, row.ID, row.SecretCiphertext)
		if err != nil {
			return nil, err
		}
		items = append(items, instance.Instance{
			ID: row.ID, Name: row.Name, ControllerURL: row.ControllerUrl, Secret: secret,
			Enabled: true, CollectEnabled: true,
			CreatedAtMS: row.CreatedAtMs, UpdatedAtMS: row.UpdatedAtMs,
			Generation: row.Generation,
		})
	}
	return items, nil
}

func (r *Instances) Save(ctx context.Context, item instance.Instance, action string) error {
	encrypted, err := Encrypt(r.AEAD, item.ID, item.Secret)
	if err != nil {
		return err
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin instance write: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	err = q.SaveInstance(ctx, generated.SaveInstanceParams{
		ID: item.ID, Name: item.Name, ControllerUrl: item.ControllerURL, SecretCiphertext: encrypted,
		CreatedAtMs: item.CreatedAtMS, UpdatedAtMs: item.UpdatedAtMS,
		Generation: item.Generation,
	})
	if err != nil {
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code() == 2067 {
			return instance.ErrConflict
		}
		return fmt.Errorf("save instance: %w", err)
	}
	if err := auditTx(ctx, q, action, item.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Instances) Delete(ctx context.Context, id, history string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin instance deletion: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	if err := q.CloseObservedConnections(ctx, id); err != nil {
		return err
	}
	// 保留历史也必须清除导入 YAML，它可能包含订阅密钥等设备配置凭据。
	if err := q.DeleteRuntimeConfig(ctx, id); err != nil {
		return fmt.Errorf("delete imported config: %w", err)
	}
	if history == "retain" {
		now := time.Now().UTC().UnixMilli()
		err = q.RetainInstance(ctx, generated.RetainInstanceParams{
			ID: id, UpdatedAtMs: now, DeletedAtMs: sql.NullInt64{Int64: now, Valid: true},
		})
	} else {
		var affected int64
		affected, err = q.DeleteInstance(ctx, id)
		if err == nil && affected == 0 {
			return instance.ErrNotFound
		}
	}
	if err != nil {
		return fmt.Errorf("delete instance: %w", err)
	}
	if err := auditTx(ctx, q, "instance.delete."+history, id); err != nil {
		return err
	}
	return tx.Commit()
}

func auditTx(ctx context.Context, q *generated.Queries, action, target string) error {
	err := q.WriteAudit(ctx, generated.WriteAuditParams{
		Action: action, TargetID: sql.NullString{String: target, Valid: target != ""},
		Result: "success", AtMs: time.Now().UTC().UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
