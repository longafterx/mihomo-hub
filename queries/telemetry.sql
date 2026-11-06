-- name: GetCursor :one
SELECT * FROM collector_cursor WHERE instance_id=?;

-- name: SaveCursor :exec
INSERT INTO collector_cursor(instance_id,epoch_id,sample_id,source_id,upload_total,download_total,at_ms)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET epoch_id=excluded.epoch_id,
sample_id=excluded.sample_id,source_id=excluded.source_id,upload_total=excluded.upload_total,
download_total=excluded.download_total,at_ms=excluded.at_ms;

-- name: GetGeneration :one
SELECT generation FROM instances WHERE id=? AND deleted_at_ms IS NULL AND enabled=1 AND collect_enabled=1;

-- name: SaveCollectorState :exec
INSERT INTO collector_state(instance_id,generation,status,error_code,at_ms) VALUES(?,?,?,?,?)
ON CONFLICT(instance_id) DO UPDATE SET generation=excluded.generation,status=excluded.status,
error_code=excluded.error_code,at_ms=excluded.at_ms;

-- name: InsertEpoch :exec
INSERT INTO instance_epochs(instance_id,epoch_id,started_at_ms,reason) VALUES(?,?,?,?) ON CONFLICT DO NOTHING;

-- name: CloseObservedConnections :exec
UPDATE connection_records SET closed_at_ms=last_seen_at_ms,close_reason='observation_gap'
WHERE instance_id=? AND closed_at_ms IS NULL;

-- name: UpsertMinute :exec
INSERT INTO traffic_minute(instance_id,bucket_ms,total_upload,total_download,attributed_upload,attributed_download,observed_ms,gap)
VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(instance_id,bucket_ms) DO UPDATE SET
 total_upload=traffic_minute.total_upload+excluded.total_upload,
 total_download=traffic_minute.total_download+excluded.total_download,
 attributed_upload=traffic_minute.attributed_upload+excluded.attributed_upload,
 attributed_download=traffic_minute.attributed_download+excluded.attributed_download,
 observed_ms=traffic_minute.observed_ms+excluded.observed_ms,gap=MAX(traffic_minute.gap,excluded.gap);

-- name: UpsertDimensionMinute :exec
INSERT INTO traffic_dimension_minute(instance_id,bucket_ms,dimension,value,upload_bytes,download_bytes)
VALUES(?,?,?,?,?,?) ON CONFLICT(instance_id,bucket_ms,dimension,value) DO UPDATE SET
upload_bytes=traffic_dimension_minute.upload_bytes+excluded.upload_bytes,
download_bytes=traffic_dimension_minute.download_bytes+excluded.download_bytes;

-- name: UpsertConnection :exec
INSERT INTO connection_records(instance_id,lifecycle_id,epoch_id,connection_id,opened_at_ms,first_seen_at_ms,last_seen_at_ms,
closed_at_ms,close_reason,host,proxy,rule,process,src_ip,upload_bytes,download_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(instance_id,lifecycle_id) DO UPDATE SET last_seen_at_ms=excluded.last_seen_at_ms,
closed_at_ms=excluded.closed_at_ms,close_reason=excluded.close_reason,
upload_bytes=excluded.upload_bytes,download_bytes=excluded.download_bytes;

-- name: InsertGap :exec
INSERT INTO traffic_gap_intervals(id,instance_id,epoch_id,from_ms,to_ms,reason,resolution,candidate_upload_bytes,candidate_download_bytes)
VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING;

-- name: RecomputeHour :exec
INSERT OR REPLACE INTO traffic_hour(instance_id,bucket_ms,total_upload,total_download,attributed_upload,attributed_download,observed_ms,gap)
SELECT m.instance_id,sqlc.arg(bucket_ms),SUM(m.total_upload),SUM(m.total_download),SUM(m.attributed_upload),SUM(m.attributed_download),SUM(m.observed_ms),MAX(m.gap)
FROM traffic_minute m WHERE m.instance_id=sqlc.arg(instance_id) AND m.bucket_ms>=sqlc.arg(from_ms) AND m.bucket_ms<sqlc.arg(to_ms) GROUP BY m.instance_id;

-- name: DeleteDimensionHour :exec
DELETE FROM traffic_dimension_hour WHERE instance_id=? AND bucket_ms=?;

-- name: RecomputeDimensionHour :exec
INSERT INTO traffic_dimension_hour(instance_id,bucket_ms,dimension,value,upload_bytes,download_bytes)
SELECT m.instance_id,sqlc.arg(bucket_ms),m.dimension,m.value,SUM(m.upload_bytes),SUM(m.download_bytes)
FROM traffic_dimension_minute m WHERE m.instance_id=sqlc.arg(instance_id) AND m.bucket_ms>=sqlc.arg(from_ms) AND m.bucket_ms<sqlc.arg(to_ms)
GROUP BY m.instance_id,m.dimension,m.value;
