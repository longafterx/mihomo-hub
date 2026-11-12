-- name: AnalyticsInstances :many
SELECT id,name,deleted_at_ms,created_at_ms,updated_at_ms FROM instances
WHERE (sqlc.arg(ids)='[]' OR id IN (SELECT value FROM json_each(sqlc.arg(ids)))) ORDER BY id;

-- name: AnalyticsMinute :many
SELECT * FROM traffic_minute WHERE instance_id IN (SELECT value FROM json_each(sqlc.arg(ids)))
AND bucket_ms>=sqlc.arg(from_ms) AND bucket_ms<sqlc.arg(to_ms) ORDER BY bucket_ms,instance_id;

-- name: AnalyticsHour :many
SELECT * FROM traffic_hour WHERE instance_id IN (SELECT value FROM json_each(sqlc.arg(ids)))
AND bucket_ms>=sqlc.arg(from_ms) AND bucket_ms<sqlc.arg(to_ms) ORDER BY bucket_ms,instance_id;

-- name: TopMinute :many
SELECT value,CAST(SUM(upload_bytes) AS INTEGER) AS upload_bytes,CAST(SUM(download_bytes) AS INTEGER) AS download_bytes
FROM traffic_dimension_minute WHERE instance_id IN (SELECT value FROM json_each(sqlc.arg(ids)))
AND bucket_ms>=sqlc.arg(from_ms) AND bucket_ms<sqlc.arg(to_ms) AND dimension=sqlc.arg(dimension)
GROUP BY value ORDER BY SUM(upload_bytes)*1.0+SUM(download_bytes) DESC,value LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: TopHour :many
SELECT value,CAST(SUM(upload_bytes) AS INTEGER) AS upload_bytes,CAST(SUM(download_bytes) AS INTEGER) AS download_bytes
FROM traffic_dimension_hour WHERE instance_id IN (SELECT value FROM json_each(sqlc.arg(ids)))
AND bucket_ms>=sqlc.arg(from_ms) AND bucket_ms<sqlc.arg(to_ms) AND dimension=sqlc.arg(dimension)
GROUP BY value ORDER BY SUM(upload_bytes)*1.0+SUM(download_bytes) DESC,value LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: HistoryConnections :many
SELECT * FROM connection_records WHERE instance_id IN (SELECT value FROM json_each(sqlc.arg(ids)))
AND last_seen_at_ms>=sqlc.arg(from_ms) AND first_seen_at_ms<sqlc.arg(to_ms)
ORDER BY last_seen_at_ms DESC,instance_id,lifecycle_id LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: HistoryGaps :many
SELECT * FROM traffic_gap_intervals WHERE instance_id IN (SELECT value FROM json_each(sqlc.arg(ids)))
AND (to_ms>sqlc.arg(from_ms) OR (from_ms=to_ms AND from_ms>=sqlc.arg(from_ms))) AND from_ms<sqlc.arg(to_ms)
ORDER BY from_ms DESC,id LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountGaps :one
SELECT COUNT(*) FROM traffic_gap_intervals WHERE instance_id=sqlc.arg(instance_id)
AND (to_ms>sqlc.arg(from_ms) OR (from_ms=to_ms AND from_ms>=sqlc.arg(from_ms))) AND from_ms<sqlc.arg(to_ms);

-- name: HistoryRange :one
SELECT CAST(COALESCE((SELECT MIN(bucket_ms) FROM traffic_minute WHERE traffic_minute.instance_id=sqlc.arg(instance_id)),-1) AS INTEGER) AS minute_from,
CAST(COALESCE((SELECT MIN(bucket_ms) FROM traffic_hour WHERE traffic_hour.instance_id=sqlc.arg(instance_id)),-1) AS INTEGER) AS hour_from,
CAST(COALESCE((SELECT at_ms FROM collector_cursor WHERE collector_cursor.instance_id=sqlc.arg(instance_id)),-1) AS INTEGER) AS last_snapshot;

-- name: ObservedMinute :many
SELECT instance_id,CAST(SUM(observed_ms) AS INTEGER) AS observed_ms FROM traffic_minute
WHERE instance_id IN (SELECT value FROM json_each(sqlc.arg(ids)))
AND bucket_ms>=sqlc.arg(from_ms) AND bucket_ms<sqlc.arg(to_ms) GROUP BY instance_id;

-- name: ObservedHour :many
SELECT instance_id,CAST(SUM(observed_ms) AS INTEGER) AS observed_ms FROM traffic_hour
WHERE instance_id IN (SELECT value FROM json_each(sqlc.arg(ids)))
AND bucket_ms>=sqlc.arg(from_ms) AND bucket_ms<sqlc.arg(to_ms) GROUP BY instance_id;
