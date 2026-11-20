-- name: CreateDeviceAuthorization :one
INSERT INTO
  device_authorizations (
    device_code_hash,
    user_code,
    client_name,
    created_at,
    expires_at
  )
VALUES
  ($1, $2, $3, $4, $5)
RETURNING
  *;

-- name: CountDeviceAuthorizations :one
SELECT
  COUNT(*)
FROM
  device_authorizations;

-- name: DeleteExpiredDeviceAuthorizations :exec
DELETE FROM device_authorizations
WHERE
  expires_at <= $1;

-- name: GetDeviceAuthorizationByHash :one
SELECT
  *
FROM
  device_authorizations
WHERE
  device_code_hash = $1;

-- name: GetPendingDeviceAuthorization :one
-- What the approval page shows: one nobody has decided yet and that hasn't run out.
SELECT
  *
FROM
  device_authorizations
WHERE
  user_code = $1
  AND user_id IS NULL
  AND denied = FALSE
  AND expires_at > $2;

-- name: TouchDeviceAuthorization :exec
UPDATE device_authorizations
SET
  last_polled_at = $2
WHERE
  id = $1;

-- name: ApproveDeviceAuthorization :execrows
UPDATE device_authorizations
SET
  user_id = $3
WHERE
  user_code = $1
  AND user_id IS NULL
  AND denied = FALSE
  AND expires_at > $2;

-- name: DenyDeviceAuthorization :execrows
UPDATE device_authorizations
SET
  denied = TRUE
WHERE
  user_code = $1
  AND user_id IS NULL
  AND denied = FALSE
  AND expires_at > $2;

-- name: DeleteDeviceAuthorization :exec
DELETE FROM device_authorizations
WHERE
  id = $1;

-- name: CreateCliSession :one
INSERT INTO
  cli_sessions (
    user_id,
    name,
    token_hash,
    created_at,
    last_used_at,
    expires_at
  )
VALUES
  ($1, $2, $3, $4, $4, $5)
RETURNING
  *;

-- name: GetCliSessionByHash :one
SELECT
  *
FROM
  cli_sessions
WHERE
  token_hash = $1;

-- name: TouchCliSession :exec
UPDATE cli_sessions
SET
  last_used_at = $2,
  expires_at = $3
WHERE
  id = $1;

-- name: ListCliSessionsByUser :many
SELECT
  *
FROM
  cli_sessions
WHERE
  user_id = $1
ORDER BY
  last_used_at DESC,
  id DESC;

-- name: DeleteCliSession :execrows
DELETE FROM cli_sessions
WHERE
  id = $1
  AND user_id = $2;

-- name: DeleteCliSessionByHash :exec
DELETE FROM cli_sessions
WHERE
  token_hash = $1;

-- name: DeleteLapsedCliSessions :exec
DELETE FROM cli_sessions
WHERE
  expires_at <= $1;
