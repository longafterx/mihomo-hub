-- +goose NO TRANSACTION
-- +goose Up
-- SQLite 的列级 UNIQUE 不能直接删除。迁移期间尚未启动采集器；在同一连接
-- 暂停外键检查后事务重建父表，避免 DROP TABLE 级联删除已保留的历史。
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;
CREATE TABLE instances_rebuilt (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  controller_url TEXT NOT NULL,
  secret_ciphertext BLOB NOT NULL,
  enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
  collect_enabled INTEGER NOT NULL CHECK (collect_enabled IN (0,1)),
  created_at_ms INTEGER NOT NULL,
  updated_at_ms INTEGER NOT NULL,
  deleted_at_ms INTEGER,
  generation INTEGER NOT NULL DEFAULT 1
);
INSERT INTO instances_rebuilt SELECT id,name,controller_url,secret_ciphertext,
  enabled,collect_enabled,created_at_ms,updated_at_ms,deleted_at_ms,generation FROM instances;
DROP TABLE instances;
ALTER TABLE instances_rebuilt RENAME TO instances;
-- 历史身份始终由 UUID 决定；只有未删除实例占用名称。
CREATE UNIQUE INDEX idx_active_instance_name ON instances(name) WHERE deleted_at_ms IS NULL;
COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
-- 同名历史无法无损还原为全局 UNIQUE；应用只支持备份恢复，不支持降级。
SELECT mihomohub_forward_only_migration();
