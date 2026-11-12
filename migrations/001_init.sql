-- +goose Up
-- 基础数据：时间戳统一使用 Unix 毫秒；数据库只保存密码哈希和 Controller 密钥密文。
-- 本文件保留初始结构以兼容旧库升级；006 迁移将移除两个启用字段，统一使用删除标记。
-- 实例注册表：保存连接配置，以 UUID 关联采集历史；保留历史时通过删除标记保留身份。
CREATE TABLE instances (
  id TEXT PRIMARY KEY, -- 实例 UUID；更名不改变历史归属，同名重新接入也不会复用历史身份。
  name TEXT NOT NULL UNIQUE, -- 显示名称；此处为初始全局唯一约束，003 迁移改为仅未删除实例唯一。
  controller_url TEXT NOT NULL, -- Mihomo Controller 地址；删除并保留历史时清空。
  secret_ciphertext BLOB NOT NULL, -- AES-256-GCM 加密的 Controller 密钥，使用实例 UUID 作为 AAD。
  enabled INTEGER NOT NULL CHECK (enabled IN (0,1)), -- 管理状态：1 启用、0 停用；接入后统一启用。
  collect_enabled INTEGER NOT NULL CHECK (collect_enabled IN (0,1)), -- 采集状态：1 启用、0 停用；接入后统一启用。
  created_at_ms INTEGER NOT NULL, -- 实例接入 Hub 的时间。
  updated_at_ms INTEGER NOT NULL, -- 最近一次实例配置变更的时间。
  deleted_at_ms INTEGER -- 删除并保留历史的时间；NULL 表示未删除。
);

-- 单管理员凭据：首次网页设置时写入，登录只校验密码；Session 不存入数据库。
CREATE TABLE admin (
  id INTEGER PRIMARY KEY CHECK (id = 1), -- 固定为 1，保证最多只有一个管理员。
  username TEXT NOT NULL, -- 内部身份标识；登录页面无需输入用户名。
  password_hash TEXT NOT NULL -- 带随机盐的 Argon2id 密码哈希，不保存明文密码。
);

-- 操作审计：记录认证、实例管理和维护等操作，不保存请求正文或访问密钥。
-- target_id 不设外键，确保实例彻底删除后仍保留审计记录。
CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY, -- 审计记录编号，由 SQLite 分配。
  actor TEXT NOT NULL, -- 操作者标识，当前写入 admin。
  action TEXT NOT NULL, -- 动作名称，如 auth.login、instance.create、gateway.PATCH。
  target_id TEXT, -- 目标实例 UUID；认证等无实例目标的操作为 NULL。
  result TEXT NOT NULL, -- success、failure 或 attempt；attempt 仅表示尝试，不代表执行成功。
  at_ms INTEGER NOT NULL -- 操作记录时间。
);

-- 数据库就绪探针：/readyz 在事务中更新并读取此行，随后回滚，验证读写能力而不累积探活数据。
CREATE TABLE readiness_probe (
  id INTEGER PRIMARY KEY CHECK (id = 1), -- 固定探针行编号。
  value INTEGER NOT NULL -- 探针更新使用的计数值；正常探活事务回滚后不变。
);
INSERT INTO readiness_probe VALUES (1, 0);

-- +goose Down
DROP TABLE readiness_probe;
DROP TABLE audit_events;
DROP TABLE admin;
DROP TABLE instances;
