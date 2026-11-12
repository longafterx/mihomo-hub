-- +goose NO TRANSACTION
-- +goose Up
-- SQLite 的列级 UNIQUE 不能直接删除。迁移期间尚未启动采集器；在同一连接
-- 暂停外键检查后事务重建父表，避免 DROP TABLE 级联删除已保留的历史。
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;
-- 实例注册表的临时重建版本：保留全部字段和 UUID，仅移除名称的全局唯一约束。
-- 迁移结束后重命名为 instances；时间戳单位均为 Unix 毫秒。
CREATE TABLE instances_rebuilt (
  id TEXT PRIMARY KEY, -- 实例 UUID，供所有采集历史通过 instance_id 关联。
  name TEXT NOT NULL, -- 实例显示名称；由后面的部分唯一索引约束未删除实例名称。
  controller_url TEXT NOT NULL, -- Mihomo Controller 地址；保留历史的已删除实例为空字符串。
  secret_ciphertext BLOB NOT NULL, -- 使用实例 UUID 作为 AAD 的 Controller 密钥密文；软删除后清空。
  enabled INTEGER NOT NULL CHECK (enabled IN (0,1)), -- 1 表示启用管理，0 表示停用；接入后统一启用。
  collect_enabled INTEGER NOT NULL CHECK (collect_enabled IN (0,1)), -- 1 表示启用采集，0 表示停用；接入后统一启用。
  created_at_ms INTEGER NOT NULL, -- 实例接入 Hub 的时间。
  updated_at_ms INTEGER NOT NULL, -- 最近一次实例配置变更的时间。
  deleted_at_ms INTEGER, -- 删除并保留历史的时间；NULL 表示未删除。
  generation INTEGER NOT NULL DEFAULT 1 -- 配置代次；变更后递增，使旧连接和旧采样失效。
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
