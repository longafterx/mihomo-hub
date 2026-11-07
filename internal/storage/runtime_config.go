package storage

import (
	"context"
	"crypto/cipher"
	"database/sql"
	"errors"
	"fmt"

	"github.com/longafterx/mihomo-hub/internal/runtimeconfig"
	"github.com/longafterx/mihomo-hub/internal/storage/generated"
)

type RuntimeConfigs struct {
	DB   *sql.DB
	AEAD cipher.AEAD
}

func (r *RuntimeConfigs) Get(ctx context.Context, id string) (runtimeconfig.Record, error) {
	row, err := generated.New(r.DB).GetRuntimeConfig(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return runtimeconfig.Record{}, nil
	}
	if err != nil {
		return runtimeconfig.Record{}, fmt.Errorf("read imported config: %w", err)
	}
	// 独立用途的 AAD 防止将 Controller Secret 密文与配置密文互换。
	text, err := Decrypt(r.AEAD, "runtime-config:"+id, row.YamlCiphertext)
	if err != nil {
		return runtimeconfig.Record{}, err
	}
	return runtimeconfig.Record{YAML: text, ControllerURL: row.ControllerUrl, Revision: row.Revision, UpdatedAtMS: row.UpdatedAtMs}, nil
}

func (r *RuntimeConfigs) Save(ctx context.Context, id string, record runtimeconfig.Record, expected int64) error {
	encrypted, err := Encrypt(r.AEAD, "runtime-config:"+id, record.YAML)
	if err != nil {
		return err
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin imported config write: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	previous, err := q.GetRuntimeConfig(ctx, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check imported config revision: %w", err)
	}
	if previous.Revision != expected {
		return runtimeconfig.ErrConflict
	}
	count, err := q.SaveRuntimeConfig(ctx, generated.SaveRuntimeConfigParams{
		ID: id, ControllerUrl: record.ControllerURL, YamlCiphertext: encrypted,
		Revision: record.Revision, UpdatedAtMs: record.UpdatedAtMS,
	})
	if err != nil {
		return fmt.Errorf("save imported config: %w", err)
	}
	if count != 1 {
		return runtimeconfig.ErrConflict
	}
	if err := auditTx(ctx, q, "runtime_config.save", id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit imported config: %w", err)
	}
	return nil
}
