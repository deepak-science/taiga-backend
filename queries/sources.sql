-- name: GetSourceClient :one
SELECT
  *
FROM
  source_clients
WHERE
  kind = $1
  AND issuer = $2
  AND redirect_uri = $3;

-- name: GetSourceClientByID :one
SELECT
  *
FROM
  source_clients
WHERE
  id = $1;

-- name: CreateSourceClient :one
-- Two first connections at once both register; the second to land keeps the first's row.
INSERT INTO
  source_clients (kind, issuer, redirect_uri, registration)
VALUES
  ($1, $2, $3, $4)
ON CONFLICT (kind, issuer, redirect_uri) DO UPDATE
SET
  kind = EXCLUDED.kind
RETURNING
  *;

-- name: UpsertSourceConnection :one
-- Connecting again replaces the tokens and clears the fault, and carries on from where the syncs had got to.
INSERT INTO
  source_connections (
    user_id,
    kind,
    client_id,
    credentials,
    synced_through
  )
VALUES
  ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, kind) DO UPDATE
SET
  client_id = EXCLUDED.client_id,
  credentials = EXCLUDED.credentials,
  last_error = '',
  needs_reconnect = FALSE
RETURNING
  *;

-- name: ListSourceConnections :many
SELECT
  *
FROM
  source_connections
WHERE
  user_id = $1
ORDER BY
  id;

-- name: GetSourceConnection :one
SELECT
  *
FROM
  source_connections
WHERE
  id = $1
  AND user_id = $2;

-- name: GetSourceConnectionByKind :one
SELECT
  *
FROM
  source_connections
WHERE
  user_id = $1
  AND kind = $2;

-- name: DeleteSourceConnection :execrows
DELETE FROM source_connections
WHERE
  id = $1
  AND user_id = $2;

-- name: SetSourceCredentials :exec
UPDATE source_connections
SET
  credentials = $3
WHERE
  id = $1
  AND user_id = $2;

-- name: FinishSourceSync :exec
UPDATE source_connections
SET
  synced_through = $3,
  last_synced_at = $4,
  last_error = '',
  needs_reconnect = FALSE
WHERE
  id = $1
  AND user_id = $2;

-- name: FailSourceSync :exec
UPDATE source_connections
SET
  last_error = $3,
  needs_reconnect = $4
WHERE
  id = $1
  AND user_id = $2;

-- name: GetSourceActivity :one
SELECT
  *
FROM
  source_activities
WHERE
  user_id = $1
  AND kind = $2
  AND external_id = $3;

-- name: CreateSourceActivity :exec
INSERT INTO
  source_activities (
    user_id,
    kind,
    external_id,
    activity_file_id,
    start_time,
    files,
    fetched_at
  )
VALUES
  ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT DO NOTHING;

-- name: CountSourceFilesSince :one
SELECT
  CAST(COALESCE(SUM(files), 0) AS INTEGER) AS files
FROM
  source_activities
WHERE
  user_id = $1
  AND kind = $2
  AND fetched_at > $3;

-- name: CountSourceActivities :one
SELECT
  COUNT(*)
FROM
  source_activities
WHERE
  user_id = $1
  AND kind = $2;

-- name: EnqueueSyncSource :exec
-- One pending sync per user (idx_jobs_pending).
INSERT INTO
  jobs (user_id, kind, run_at)
VALUES
  ($1, 'sync_source', $2)
ON CONFLICT DO NOTHING;

-- name: EnqueueSourceSyncsDue :execrows
-- Every connection that can sync. One whose last sync died waits for a retry through /jobs or POST /sources/{id}/sync, so an outage doesn't leave a dead job every sweep.
INSERT INTO
  jobs (user_id, kind, run_at)
SELECT
  source_connections.user_id,
  'sync_source',
  sqlc.arg(run_at)
FROM
  source_connections
WHERE
  NOT source_connections.needs_reconnect
  AND NOT EXISTS (
    SELECT
      1
    FROM
      jobs
    WHERE
      jobs.user_id = source_connections.user_id
      AND jobs.kind = 'sync_source'
      AND jobs.state = 'dead'
  )
ON CONFLICT DO NOTHING;

-- name: DeleteSyncSourceJobs :exec
DELETE FROM jobs
WHERE
  user_id = $1
  AND kind = 'sync_source'
  AND state IN ('queued', 'dead');
