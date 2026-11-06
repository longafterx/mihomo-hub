-- +goose Up
ALTER TABLE instances ADD COLUMN generation INTEGER NOT NULL DEFAULT 1;
CREATE TABLE instance_epochs (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  epoch_id TEXT NOT NULL,
  started_at_ms INTEGER NOT NULL,
  reason TEXT NOT NULL,
  PRIMARY KEY(instance_id, epoch_id)
) STRICT;
CREATE TABLE collector_cursor (
  instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
  epoch_id TEXT NOT NULL,
  sample_id TEXT NOT NULL,
  source_id TEXT NOT NULL,
  upload_total INTEGER NOT NULL,
  download_total INTEGER NOT NULL,
  at_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE collector_state (
  instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
  generation INTEGER NOT NULL,
  status TEXT NOT NULL,
  error_code TEXT NOT NULL,
  at_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE traffic_minute (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  bucket_ms INTEGER NOT NULL,
  total_upload INTEGER NOT NULL DEFAULT 0,
  total_download INTEGER NOT NULL DEFAULT 0,
  attributed_upload INTEGER NOT NULL DEFAULT 0,
  attributed_download INTEGER NOT NULL DEFAULT 0,
  observed_ms INTEGER NOT NULL DEFAULT 0,
  gap INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(instance_id, bucket_ms)
) STRICT;
CREATE TABLE traffic_hour (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  bucket_ms INTEGER NOT NULL,
  total_upload INTEGER NOT NULL,
  total_download INTEGER NOT NULL,
  attributed_upload INTEGER NOT NULL,
  attributed_download INTEGER NOT NULL,
  observed_ms INTEGER NOT NULL,
  gap INTEGER NOT NULL,
  PRIMARY KEY(instance_id, bucket_ms)
) STRICT;
CREATE TABLE connection_records (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  lifecycle_id TEXT NOT NULL,
  epoch_id TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  opened_at_ms INTEGER,
  first_seen_at_ms INTEGER NOT NULL,
  last_seen_at_ms INTEGER NOT NULL,
  closed_at_ms INTEGER,
  close_reason TEXT NOT NULL DEFAULT '',
  host TEXT NOT NULL,
  proxy TEXT NOT NULL,
  rule TEXT NOT NULL,
  process TEXT NOT NULL,
  src_ip TEXT NOT NULL,
  upload_bytes INTEGER NOT NULL,
  download_bytes INTEGER NOT NULL,
  PRIMARY KEY(instance_id, lifecycle_id)
) STRICT;
CREATE INDEX idx_connections_seen ON connection_records(instance_id,last_seen_at_ms DESC);
CREATE INDEX idx_connections_open ON connection_records(instance_id) WHERE closed_at_ms IS NULL;
CREATE TABLE traffic_dimension_minute (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  bucket_ms INTEGER NOT NULL,
  dimension TEXT NOT NULL,
  value TEXT NOT NULL,
  upload_bytes INTEGER NOT NULL,
  download_bytes INTEGER NOT NULL,
  PRIMARY KEY(instance_id,bucket_ms,dimension,value)
) STRICT;
CREATE TABLE traffic_dimension_hour (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  bucket_ms INTEGER NOT NULL,
  dimension TEXT NOT NULL,
  value TEXT NOT NULL,
  upload_bytes INTEGER NOT NULL,
  download_bytes INTEGER NOT NULL,
  PRIMARY KEY(instance_id,bucket_ms,dimension,value)
) STRICT;
CREATE TABLE traffic_gap_intervals (
  id TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  epoch_id TEXT NOT NULL,
  from_ms INTEGER NOT NULL,
  to_ms INTEGER NOT NULL,
  reason TEXT NOT NULL,
  resolution TEXT NOT NULL,
  candidate_upload_bytes INTEGER,
  candidate_download_bytes INTEGER
) STRICT;
CREATE INDEX idx_gaps_time ON traffic_gap_intervals(instance_id,from_ms,to_ms);

CREATE INDEX idx_minute_retention ON traffic_minute(bucket_ms);
CREATE INDEX idx_hour_retention ON traffic_hour(bucket_ms);
CREATE INDEX idx_dimension_minute_retention ON traffic_dimension_minute(bucket_ms);
CREATE INDEX idx_dimension_hour_retention ON traffic_dimension_hour(bucket_ms);
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
