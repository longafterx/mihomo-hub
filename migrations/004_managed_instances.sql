-- +goose Up
-- 接入 Hub 的存量实例统一进入管理和采集状态；删除墓碑仍保持停用。
UPDATE instances SET enabled=1, collect_enabled=1, generation=generation+1,
  updated_at_ms=CAST(strftime('%s','now') AS INTEGER)*1000
WHERE deleted_at_ms IS NULL AND (enabled=0 OR collect_enabled=0);

-- +goose Down
-- 无法恢复升级前每个实例的选择，降级应恢复升级前备份。
SELECT mihomohub_forward_only_migration();
