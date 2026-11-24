-- name: GetUserAdmin :one
SELECT
  email,
  email_verified,
  is_admin
FROM
  users
WHERE
  id = $1;

-- name: SetUserAdmin :execrows
UPDATE users
SET
  is_admin = $2
WHERE
  id = $1;

-- name: TouchUserSeen :exec
UPDATE users
SET
  last_seen_at = $2
WHERE
  id = $1;

-- name: ListAccountsForAdmin :many
-- Counts and states only: an admin sees how much each account has, never what.
SELECT
  users.id,
  users.email,
  users.display_name,
  users.email_verified,
  users.is_admin,
  users.created_at,
  users.last_seen_at,
  CAST(users.password IS NOT NULL AS BOOLEAN) AS has_password,
  (
    SELECT
      COUNT(*)
    FROM
      workouts
    WHERE
      workouts.user_id = users.id
  ) AS workouts,
  (
    SELECT
      COUNT(*)
    FROM
      activity_file
    WHERE
      activity_file.user_id = users.id
  ) AS files,
  (
    SELECT
      COUNT(*)
    FROM
      activity_file
    WHERE
      activity_file.user_id = users.id
      AND NOT EXISTS (
        SELECT
          1
        FROM
          workouts
        WHERE
          workouts.activity_file_id = activity_file.id
      )
  ) AS files_without_workout,
  (
    SELECT
      COUNT(*)
    FROM
      cli_sessions
    WHERE
      cli_sessions.user_id = users.id
  ) AS cli_sessions
FROM
  users
ORDER BY
  users.id;

-- name: ListAllIdentityIssuers :many
SELECT
  user_id,
  issuer
FROM
  user_identities
ORDER BY
  user_id,
  created_at,
  id;

-- name: DeleteCliSessionsByUser :execrows
DELETE FROM cli_sessions
WHERE
  user_id = $1;

-- name: ListJobsForAdmin :many
-- Every user's newest jobs; an empty state, kind or a user_id of 0 doesn't filter.
SELECT
  jobs.*,
  users.email
FROM
  jobs
  JOIN users ON users.id = jobs.user_id
WHERE
  (
    CAST(sqlc.arg(state) AS TEXT) = ''
    OR jobs.state = CAST(sqlc.arg(state) AS TEXT)
  )
  AND (
    CAST(sqlc.arg(kind) AS TEXT) = ''
    OR jobs.kind = CAST(sqlc.arg(kind) AS TEXT)
  )
  AND (
    CAST(sqlc.arg(user_id) AS INTEGER) = 0
    OR jobs.user_id = CAST(sqlc.arg(user_id) AS INTEGER)
  )
ORDER BY
  jobs.id DESC
LIMIT
  sqlc.arg(row_limit);

-- name: GetJobForAdmin :one
SELECT
  *
FROM
  jobs
WHERE
  id = $1;

-- name: RequeueDeadJobForAdmin :one
UPDATE jobs
SET
  state = 'queued',
  attempts = 0,
  run_at = $2,
  updated_at = $2
WHERE
  id = $1
  AND state = 'dead'
RETURNING
  *;

-- name: DeleteIdleJob :execrows
-- A running job is left to its worker, which would only record a result for a row that's gone.
DELETE FROM jobs
WHERE
  id = $1
  AND state IN ('queued', 'dead');

-- name: EnqueueAllWorkouts :execrows
-- Every user's file-backed workouts, current or not; one already queued is skipped by idx_jobs_pending.
INSERT INTO
  jobs (user_id, kind, workout_id, run_at)
SELECT
  user_id,
  'process_workout',
  id,
  $1
FROM
  workouts
WHERE
  activity_file_id IS NOT NULL
ON CONFLICT DO NOTHING;

-- name: CountStaleWorkouts :one
SELECT
  COUNT(*)
FROM
  workouts
WHERE
  activity_file_id IS NOT NULL
  AND processor_version <> $1;

-- name: CountDeploymentTotals :one
SELECT
  (
    SELECT
      COUNT(*)
    FROM
      users
  ) AS accounts,
  (
    SELECT
      COUNT(*)
    FROM
      workouts
  ) AS workouts,
  (
    SELECT
      COUNT(*)
    FROM
      activity_file
  ) AS files;

-- name: ListSourceConnectionsForAdmin :many
-- Never the credentials column: an admin sees a connection's state, not its tokens.
SELECT
  source_connections.id,
  source_connections.user_id,
  users.email,
  source_connections.kind,
  source_connections.client_id,
  source_connections.synced_through,
  source_connections.last_synced_at,
  source_connections.last_error,
  source_connections.needs_reconnect,
  source_connections.created_at,
  (
    SELECT
      COUNT(*)
    FROM
      source_activities
    WHERE
      source_activities.user_id = source_connections.user_id
      AND source_activities.kind = source_connections.kind
  ) AS activities,
  (
    SELECT
      CAST(COALESCE(SUM(files), 0) AS INTEGER)
    FROM
      source_file_requests
    WHERE
      source_file_requests.user_id = source_connections.user_id
      AND source_file_requests.kind = source_connections.kind
      AND source_file_requests.requested_at > sqlc.arg(since)
  ) AS files_since
FROM
  source_connections
  JOIN users ON users.id = source_connections.user_id
ORDER BY
  source_connections.id;

-- name: ListSourceClientsForAdmin :many
-- Never the registration column, which holds the client's secret.
SELECT
  id,
  kind,
  issuer,
  redirect_uri,
  created_at
FROM
  source_clients
ORDER BY
  id;
