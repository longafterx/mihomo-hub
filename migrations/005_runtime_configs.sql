-- +goose Up
-- 完整 YAML 可能含订阅和认证信息，只保存使用实例专属 AAD 的密文。
CREATE TABLE runtime_configs (
  instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
  controller_url TEXT NOT NULL,
  yaml_ciphertext BLOB NOT NULL,
  revision INTEGER NOT NULL,
  updated_at_ms INTEGER NOT NULL
);

-- +goose Down
DROP TABLE runtime_configs;
