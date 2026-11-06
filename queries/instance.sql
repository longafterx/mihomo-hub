-- name: ListInstances :many
SELECT id, name, controller_url, secret_ciphertext, enabled, collect_enabled,
       created_at_ms, updated_at_ms, generation
FROM instances WHERE deleted_at_ms IS NULL ORDER BY created_at_ms, id;

-- name: SaveInstance :exec
INSERT INTO instances
  (id, name, controller_url, secret_ciphertext, enabled, collect_enabled, created_at_ms, updated_at_ms, generation)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, controller_url=excluded.controller_url,
  secret_ciphertext=excluded.secret_ciphertext, enabled=excluded.enabled,
  collect_enabled=excluded.collect_enabled, updated_at_ms=excluded.updated_at_ms, generation=excluded.generation;

-- name: DeleteInstance :exec
DELETE FROM instances WHERE id = ?;

-- name: RetainInstance :exec
UPDATE instances SET enabled=0, collect_enabled=0, controller_url='', secret_ciphertext=x'',
  deleted_at_ms=?, updated_at_ms=? WHERE id=?;

-- name: WriteAudit :exec
INSERT INTO audit_events(actor, action, target_id, result, at_ms) VALUES ('admin', ?, ?, ?, ?);

-- name: GetAdmin :one
SELECT username, password_hash FROM admin WHERE id=1;

-- name: InitializeAdmin :exec
INSERT INTO admin(id, username, password_hash) VALUES (1, ?, ?);

-- name: CountAdmins :one
SELECT COUNT(*) FROM admin;
