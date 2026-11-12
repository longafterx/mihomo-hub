-- +goose Up
-- 采集数据：时间戳和时间桶起点使用 Unix 毫秒，流量使用字节，observed_ms 使用毫秒时长。
-- 本文件保留历史结构；006 迁移将移除独立周期表、采集状态表及游标的 sample_id。
-- 子表通过实例 UUID 关联；彻底删除实例时级联清理，保留历史的软删除不会触发级联。
-- 配置代次：实例配置变更后递增，阻止变更前排队的旧采样写入新配置对应的数据。
ALTER TABLE instances ADD COLUMN generation INTEGER NOT NULL DEFAULT 1;

-- 累计计数周期：记录首次采集、Controller 变化或计数回退产生的新周期，区分不同计数基线。
CREATE TABLE instance_epochs (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE, -- 所属实例 UUID。
  epoch_id TEXT NOT NULL, -- Hub 为本次计数周期生成的 UUID。
  started_at_ms INTEGER NOT NULL, -- 新周期首个采样的接收时间。
  reason TEXT NOT NULL, -- 周期起因：first_seen、controller_changed 或 counter_reset。
  PRIMARY KEY(instance_id, epoch_id)
) STRICT;

-- 采集游标：每个实例仅保留最近一次已提交采样，供重启或重连后恢复基线。
-- 与流量、连接摘要同事务提交，防止落库失败后仍推进计数位置。
CREATE TABLE collector_cursor (
  instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE, -- 所属实例 UUID；每实例一行。
  epoch_id TEXT NOT NULL, -- 最近一次采样所属的累计计数周期。
  sample_id TEXT NOT NULL, -- 最近一次已提交采样的 UUID。
  source_id TEXT NOT NULL, -- Controller 地址的 SHA-256 摘要，用于识别采集目标变化。
  upload_total INTEGER NOT NULL, -- 最近快照中的 Mihomo 累计上传字节数，用于后续差分。
  download_total INTEGER NOT NULL, -- 最近快照中的 Mihomo 累计下载字节数，用于后续差分。
  at_ms INTEGER NOT NULL -- 最近一次已提交采样的接收时间，用于拒绝重复或乱序采样。
) STRICT;

-- 最近采集状态：持久化每实例最后提交的状态，不是完整的状态变更历史。
CREATE TABLE collector_state (
  instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE, -- 所属实例 UUID；每实例一行。
  generation INTEGER NOT NULL, -- 写入状态时的实例配置代次。
  status TEXT NOT NULL, -- 最后提交的采集状态，如 online、degraded。
  error_code TEXT NOT NULL, -- 最近的采集错误码；无错误时为空字符串。
  at_ms INTEGER NOT NULL -- 此状态对应的采样或错误发生时间。
) STRICT;

-- 分钟流量总览：保存累计计数差分、可归因流量和观测覆盖时长，供趋势与覆盖率查询。
-- 总量与可归因量分别保存，不能假定连接采样能覆盖全部流量。
CREATE TABLE traffic_minute (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE, -- 所属实例 UUID。
  bucket_ms INTEGER NOT NULL, -- 分钟桶起点，对齐到 60000 毫秒的整数倍。
  total_upload INTEGER NOT NULL DEFAULT 0, -- 分配到此桶的实例上传增量。
  total_download INTEGER NOT NULL DEFAULT 0, -- 分配到此桶的实例下载增量。
  attributed_upload INTEGER NOT NULL DEFAULT 0, -- 通过已观测连接归因的上传增量。
  attributed_download INTEGER NOT NULL DEFAULT 0, -- 通过已观测连接归因的下载增量。
  observed_ms INTEGER NOT NULL DEFAULT 0, -- 桶内连续有效观测的累计时长，不等同于桶的总时长。
  gap INTEGER NOT NULL DEFAULT 0, -- 1 表示桶中标记了采集缺口或统计不一致，0 表示未标记。
  PRIMARY KEY(instance_id, bucket_ms)
) STRICT;

-- 小时流量总览：由分钟总览聚合，提供更长保留期的趋势查询，不是另一套独立采集数据。
CREATE TABLE traffic_hour (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE, -- 所属实例 UUID。
  bucket_ms INTEGER NOT NULL, -- 小时桶起点，对齐到 3600000 毫秒的整数倍。
  total_upload INTEGER NOT NULL, -- 该小时内分钟桶的总上传增量之和。
  total_download INTEGER NOT NULL, -- 该小时内分钟桶的总下载增量之和。
  attributed_upload INTEGER NOT NULL, -- 该小时内分钟桶的可归因上传增量之和。
  attributed_download INTEGER NOT NULL, -- 该小时内分钟桶的可归因下载增量之和。
  observed_ms INTEGER NOT NULL, -- 该小时内分钟桶的有效观测时长之和。
  gap INTEGER NOT NULL, -- 该小时任一已聚合分钟桶标记缺口时为 1。
  PRIMARY KEY(instance_id, bucket_ms)
) STRICT;

-- 连接生命周期摘要：持续更新每段已观测连接的计数和元数据，不保存每秒原始快照。
-- 同一 Mihomo 连接 ID 在计数重置、ID 复用或观测中断后可能对应多段生命周期。
CREATE TABLE connection_records (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE, -- 所属实例 UUID。
  lifecycle_id TEXT NOT NULL, -- Hub 生成的生命周期 UUID，避免上游连接 ID 复用导致历史覆盖。
  epoch_id TEXT NOT NULL, -- 此段生命周期开始时所属的累计计数周期。
  connection_id TEXT NOT NULL, -- Mihomo 原始连接 ID，不单独作为历史主键。
  opened_at_ms INTEGER, -- Mihomo 上报的连接开始时间；无法解析或缺失时为 NULL。
  first_seen_at_ms INTEGER NOT NULL, -- Hub 首次观测到此段生命周期的时间。
  last_seen_at_ms INTEGER NOT NULL, -- Hub 最近一次观测到此段生命周期的时间。
  closed_at_ms INTEGER, -- Hub 记录的结束时间；NULL 表示尚未记录结束，不保证远端仍然活跃。
  close_reason TEXT NOT NULL DEFAULT '', -- 结束原因，如 disappeared、observation_gap、counter_reset_or_id_reuse。
  host TEXT NOT NULL, -- 首次观测的目标域名；缺失时为 (unknown)。
  proxy TEXT NOT NULL, -- 首次观测时代理链的第一个条目；缺失时为 (unknown)。
  rule TEXT NOT NULL, -- 首次观测的规则类型与规则内容，以冒号拼接。
  process TEXT NOT NULL, -- 首次观测的来源进程；缺失时为 (unknown)。
  src_ip TEXT NOT NULL, -- 首次观测的来源 IP；缺失时为 (unknown)。
  upload_bytes INTEGER NOT NULL, -- 最后一次快照中该连接的累计上传量，可能包含接入前的流量。
  download_bytes INTEGER NOT NULL, -- 最后一次快照中该连接的累计下载量，可能包含接入前的流量。
  PRIMARY KEY(instance_id, lifecycle_id)
) STRICT;
-- 按实例和最近观测时间倒序查询历史连接。
CREATE INDEX idx_connections_seen ON connection_records(instance_id,last_seen_at_ms DESC);
-- 快速定位未结束记录，便于观测中断时统一结束上一段生命周期。
CREATE INDEX idx_connections_open ON connection_records(instance_id) WHERE closed_at_ms IS NULL;

-- 分钟维度流量：按域名、代理、规则、进程和来源 IP 分别聚合，用于用量排行。
-- 每份连接增量会分别计入五个维度，因此不能将不同维度的记录相加作为总流量。
CREATE TABLE traffic_dimension_minute (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE, -- 所属实例 UUID。
  bucket_ms INTEGER NOT NULL, -- 分钟桶起点，对齐到 60000 毫秒的整数倍。
  dimension TEXT NOT NULL, -- 维度名称：host、proxy、rule、process 或 src_ip。
  value TEXT NOT NULL, -- 该维度的具体值，如域名、代理名称或来源 IP。
  upload_bytes INTEGER NOT NULL, -- 此分钟内归因到该维度值的上传增量。
  download_bytes INTEGER NOT NULL, -- 此分钟内归因到该维度值的下载增量。
  PRIMARY KEY(instance_id,bucket_ms,dimension,value)
) STRICT;

-- 小时维度流量：由分钟维度数据聚合，供更长时间范围的用量排行查询。
CREATE TABLE traffic_dimension_hour (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE, -- 所属实例 UUID。
  bucket_ms INTEGER NOT NULL, -- 小时桶起点，对齐到 3600000 毫秒的整数倍。
  dimension TEXT NOT NULL, -- 与分钟表相同的五种维度名称。
  value TEXT NOT NULL, -- 对应维度的具体值，聚合时保持不变。
  upload_bytes INTEGER NOT NULL, -- 此小时内该维度值的分钟上传增量之和。
  download_bytes INTEGER NOT NULL, -- 此小时内该维度值的分钟下载增量之和。
  PRIMARY KEY(instance_id,bucket_ms,dimension,value)
) STRICT;

-- 采集缺口与不确定增量：解释断线、重启、计数回退或采样不一致导致的数据边界。
-- candidate 流量只作为待确认值保存，不分配进精确的分钟总量和维度统计。
CREATE TABLE traffic_gap_intervals (
  id TEXT PRIMARY KEY, -- 缺口记录 UUID。
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE, -- 所属实例 UUID。
  epoch_id TEXT NOT NULL, -- 记录缺口时采用的累计计数周期。
  from_ms INTEGER NOT NULL, -- 缺口起点；通常为上次已提交采样的时间。
  to_ms INTEGER NOT NULL, -- 缺口终点；与起点相等时表示单个边界事件。
  reason TEXT NOT NULL, -- 缺口原因，如 sampling_gap、counter_reset、stream_interrupted。
  resolution TEXT NOT NULL, -- 处理标记：unknown、uncertain_epoch_and_time 或 inconsistent_sample。
  candidate_upload_bytes INTEGER, -- 缺口两端的候选上传差值；无法比较计数基线时为 NULL。
  candidate_download_bytes INTEGER -- 缺口两端的候选下载差值；无法比较计数基线时为 NULL。
) STRICT;
-- 按实例和时间范围查询采集缺口。
CREATE INDEX idx_gaps_time ON traffic_gap_intervals(instance_id,from_ms,to_ms);

-- 保留策略索引：按时间分批删除过期记录，避免清理时扫描整个业务表。
CREATE INDEX idx_minute_retention ON traffic_minute(bucket_ms);
CREATE INDEX idx_hour_retention ON traffic_hour(bucket_ms);
CREATE INDEX idx_dimension_minute_retention ON traffic_dimension_minute(bucket_ms);
CREATE INDEX idx_dimension_hour_retention ON traffic_dimension_hour(bucket_ms);
-- 仅索引已关闭连接；保留策略不会清理尚未记录结束时间的连接。
CREATE INDEX idx_connection_retention ON connection_records(closed_at_ms) WHERE closed_at_ms IS NOT NULL;
CREATE INDEX idx_gap_retention ON traffic_gap_intervals(to_ms);

-- +goose Down
DROP TABLE traffic_gap_intervals;
DROP TABLE traffic_dimension_hour;
DROP TABLE traffic_dimension_minute;
DROP TABLE connection_records;
DROP TABLE traffic_hour;
DROP TABLE traffic_minute;
DROP TABLE collector_state;
DROP TABLE collector_cursor;
DROP TABLE instance_epochs;
ALTER TABLE instances DROP COLUMN generation;
