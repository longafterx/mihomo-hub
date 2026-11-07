-- name: GetRuntimeConfig :one
SELECT controller_url, yaml_ciphertext, revision, updated_at_ms
FROM runtime_configs WHERE instance_id = ?;

-- name: SaveRuntimeConfig :execrows
INSERT INTO runtime_configs(instance_id, controller_url, yaml_ciphertext, revision, updated_at_ms)
SELECT instances.id, instances.controller_url, ?, ?, ? FROM instances
WHERE instances.id = ? AND instances.controller_url = ? AND instances.deleted_at_ms IS NULL
ON CONFLICT(instance_id) DO UPDATE SET
  controller_url=excluded.controller_url, yaml_ciphertext=excluded.yaml_ciphertext,
  revision=excluded.revision, updated_at_ms=excluded.updated_at_ms;

-- name: DeleteRuntimeConfig :exec
DELETE FROM runtime_configs WHERE instance_id = ?;
