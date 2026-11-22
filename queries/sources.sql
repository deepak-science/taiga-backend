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

-- name: ReplaceSourceClient :one
-- Only in place of the registration that was read: no row means another request replaced it first.
UPDATE source_clients
SET
  registration = $2
WHERE
  id = $1
  AND registration = sqlc.arg(was)
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
    fetched_at,
    sport_type
  )
VALUES
  ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT DO NOTHING;

-- name: RecordSourceFileRequest :exec
INSERT INTO
  source_file_requests (user_id, kind, external_id, files, requested_at)
VALUES
  ($1, $2, $3, $4, $5);

-- name: DeleteSourceFileRequestsBefore :exec
DELETE FROM source_file_requests
WHERE
  user_id = $1
  AND kind = $2
  AND requested_at <= $3;

-- name: CountSourceFilesSince :one
SELECT
  CAST(COALESCE(SUM(files), 0) AS INTEGER) AS files
FROM
  source_file_requests
WHERE
  user_id = $1
  AND kind = $2
  AND requested_at > $3;

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

-- name: ListSourceActivitiesWithoutMetadata :many
-- The source's activities whose workout has none of the source's own figures yet: those never tried first, newest first, then the ones whose reply couldn't be read, longest since first, so a run of unreadable ones can't keep the rest from being asked for.
-- An activity recorded in parts is matched by its first file.
SELECT
  source_activities.external_id,
  source_activities.sport_type,
  workouts.id AS workout_id
FROM
  source_activities
  JOIN workouts ON workouts.activity_file_id = source_activities.activity_file_id
  AND workouts.user_id = source_activities.user_id
WHERE
  source_activities.user_id = sqlc.arg(user_id)
  AND source_activities.kind = sqlc.arg(kind)
  AND source_activities.sport_type IS NOT NULL
  AND NOT EXISTS (
    SELECT
      1
    FROM
      coros_metadata
    WHERE
      coros_metadata.workout_id = workouts.id
  )
ORDER BY
  (source_activities.detail_tried_at IS NOT NULL),
  source_activities.detail_tried_at,
  source_activities.start_time DESC,
  workouts.id DESC
LIMIT
  sqlc.arg(at_most);

-- name: UpsertCOROSMetadata :exec
INSERT INTO
  coros_metadata (
    workout_id,
    user_id,
    external_id,
    fetched_at,
    reply,
    workout_seconds,
    total_seconds,
    distance_meters,
    avg_pace_s_per_km,
    moving_avg_pace_s_per_km,
    adjusted_pace_s_per_km,
    best_km_seconds,
    avg_heart_rate,
    avg_cadence,
    avg_stride_length_m,
    avg_power_watts,
    elevation_gain_m,
    elevation_loss_m,
    calories,
    training_load,
    aerobic_te,
    anaerobic_te,
    training_focus,
    performance
  )
VALUES
  (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7,
    $8,
    $9,
    $10,
    $11,
    $12,
    $13,
    $14,
    $15,
    $16,
    $17,
    $18,
    $19,
    $20,
    $21,
    $22,
    $23,
    $24
  )
ON CONFLICT (workout_id) DO UPDATE
SET
  external_id = excluded.external_id,
  fetched_at = excluded.fetched_at,
  reply = excluded.reply,
  workout_seconds = excluded.workout_seconds,
  total_seconds = excluded.total_seconds,
  distance_meters = excluded.distance_meters,
  avg_pace_s_per_km = excluded.avg_pace_s_per_km,
  moving_avg_pace_s_per_km = excluded.moving_avg_pace_s_per_km,
  adjusted_pace_s_per_km = excluded.adjusted_pace_s_per_km,
  best_km_seconds = excluded.best_km_seconds,
  avg_heart_rate = excluded.avg_heart_rate,
  avg_cadence = excluded.avg_cadence,
  avg_stride_length_m = excluded.avg_stride_length_m,
  avg_power_watts = excluded.avg_power_watts,
  elevation_gain_m = excluded.elevation_gain_m,
  elevation_loss_m = excluded.elevation_loss_m,
  calories = excluded.calories,
  training_load = excluded.training_load,
  aerobic_te = excluded.aerobic_te,
  anaerobic_te = excluded.anaerobic_te,
  training_focus = excluded.training_focus,
  performance = excluded.performance;

-- name: GetCOROSMetadata :one
SELECT
  *
FROM
  coros_metadata
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: MarkSourceDetailTried :exec
UPDATE source_activities
SET
  detail_tried_at = $4
WHERE
  user_id = $1
  AND kind = $2
  AND external_id = $3;

-- name: SetSourceActivitySportType :exec
-- For an activity dealt with before the sport code was kept.
UPDATE source_activities
SET
  sport_type = $4
WHERE
  user_id = $1
  AND kind = $2
  AND external_id = $3
  AND sport_type IS NULL;

-- name: DeleteCOROSMetadata :exec
DELETE FROM coros_metadata
WHERE
  workout_id = $1
  AND user_id = $2;
