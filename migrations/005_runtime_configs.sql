-- +goose Up
-- 实例导入配置：每实例保存一份完整 YAML，作为 DNS 编辑和热加载的基准，不写回远程磁盘。
-- 完整 YAML 可能含订阅和认证信息，只保存使用实例专属 AAD 的密文。
-- 导入和 DNS 应用成功后更新版本；删除实例时，即使选择保留历史也清除此配置。
CREATE TABLE runtime_configs (
  instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE, -- 所属实例 UUID；每实例至多一份配置。
  controller_url TEXT NOT NULL, -- 导入时的 Controller 地址，防止实例改址后误用旧设备的配置。
  yaml_ciphertext BLOB NOT NULL, -- AES-256-GCM 加密的完整 YAML，AAD 为 runtime-config: 加实例 UUID。
  revision INTEGER NOT NULL, -- 配置版本号；每次保存递增，通过期望版本比较防止并发覆盖。
  updated_at_ms INTEGER NOT NULL -- 最近一次保存配置的时间，使用 Unix 毫秒。
);

-- +goose Down
DROP TABLE runtime_configs;
