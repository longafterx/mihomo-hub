-- +goose Up
-- 采集状态只在内存维护；周期边界继续由游标、连接和缺口中的 epoch_id 标识。
-- 两张辅助表没有业务读取，删除它们不影响流量统计、连接历史或采集恢复。
DROP TABLE collector_state;
DROP TABLE instance_epochs;

-- 采样通过已提交时间戳防重，不再为每次采样保存额外 UUID。
ALTER TABLE collector_cursor DROP COLUMN sample_id;

-- 接入即启用管理和采集，统一通过 deleted_at_ms 判断实例是否仍在管理中。
-- API 中的两个旧布尔字段仍保持兼容，但不再重复持久化。
ALTER TABLE instances DROP COLUMN enabled;
ALTER TABLE instances DROP COLUMN collect_enabled;

-- +goose Down
-- 已移除的辅助状态无法无损还原；降级时应恢复升级前的完整备份。
SELECT mihomohub_forward_only_migration();
