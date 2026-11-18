-- name: UpsertWorkoutTrack :exec
INSERT INTO
  workout_tracks (workout_id, user_id, track)
VALUES
  ($1, $2, $3)
ON CONFLICT (workout_id) DO UPDATE
SET
  track = excluded.track;

-- name: DeleteWorkoutTrack :exec
DELETE FROM workout_tracks
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: ListWorkoutTracksByUser :many
-- Oldest first, the order route detection assigns runs in; routes only group runs of one sport.
-- A duplicate is left out even before reprocessing drops its track, since the same job marks it.
SELECT
  workout_tracks.workout_id,
  workout_tracks.track,
  workouts.sport
FROM
  workout_tracks
  JOIN workouts ON workouts.id = workout_tracks.workout_id
WHERE
  workout_tracks.user_id = $1
  AND workouts.duplicate_of IS NULL
ORDER BY
  workouts.start_time,
  workouts.id;

-- name: GetWorkoutTrack :one
SELECT
  track
FROM
  workout_tracks
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: CreateRoute :one
INSERT INTO
  routes (user_id, representative_workout_id)
VALUES
  ($1, $2)
RETURNING
  *;

-- name: SetRouteRepresentative :exec
UPDATE routes
SET
  representative_workout_id = $3
WHERE
  id = $1
  AND user_id = $2;

-- name: RenameRoute :one
UPDATE routes
SET
  name = $3
WHERE
  id = $1
  AND user_id = $2
RETURNING
  *;

-- name: DeleteRoute :exec
DELETE FROM routes
WHERE
  id = $1
  AND user_id = $2;

-- name: GetRoute :one
SELECT
  *
FROM
  routes
WHERE
  id = $1
  AND user_id = $2;

-- name: ListRoutesByUser :many
SELECT
  *
FROM
  routes
WHERE
  user_id = $1
ORDER BY
  id;

-- name: ClearRouteRepresentative :exec
-- SQLite doesn't enforce ON DELETE SET NULL without a per-connection pragma, so a deleted workout clears itself.
UPDATE routes
SET
  representative_workout_id = NULL
WHERE
  representative_workout_id = $1
  AND user_id = $2;

-- name: AddWorkoutRoute :exec
INSERT INTO
  workout_routes (workout_id, route_id, user_id)
VALUES
  ($1, $2, $3);

-- name: DeleteWorkoutRoutesByUser :exec
DELETE FROM workout_routes
WHERE
  user_id = $1;

-- name: DeleteWorkoutRoute :exec
DELETE FROM workout_routes
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: ListWorkoutRoutesByUser :many
SELECT
  *
FROM
  workout_routes
WHERE
  user_id = $1;

-- name: GetWorkoutRouteID :one
SELECT
  route_id
FROM
  workout_routes
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: EnqueueDetectRoutes :exec
-- One pending detection per user (idx_jobs_pending), however many workouts change before it runs.
INSERT INTO
  jobs (user_id, kind, run_at)
VALUES
  ($1, 'detect_routes', $2)
ON CONFLICT DO NOTHING;

-- name: MoveWorkoutRoutes :exec
UPDATE workout_routes
SET
  route_id = sqlc.arg(to_route_id)
WHERE
  route_id = sqlc.arg(from_route_id)
  AND user_id = sqlc.arg(user_id);

-- name: UpsertRouteOverride :exec
INSERT INTO
  route_overrides (workout_id, user_id, route_id, with_group)
VALUES
  ($1, $2, $3, $4)
ON CONFLICT (workout_id) DO UPDATE
SET
  route_id = excluded.route_id,
  with_group = excluded.with_group
WHERE
  route_overrides.user_id = excluded.user_id;

-- name: GetRouteOverride :one
SELECT
  *
FROM
  route_overrides
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: ListRouteOverridesByUser :many
SELECT
  *
FROM
  route_overrides
WHERE
  user_id = $1
ORDER BY
  workout_id;

-- name: DeleteRouteOverride :exec
DELETE FROM route_overrides
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: DeleteRouteOverridesByRoute :exec
-- SQLite doesn't enforce ON DELETE CASCADE without a per-connection pragma, so a deleted route clears its own.
DELETE FROM route_overrides
WHERE
  route_id = $1
  AND user_id = $2;

-- name: RetargetRouteOverrides :exec
UPDATE route_overrides
SET
  route_id = sqlc.arg(to_route_id)
WHERE
  route_id = sqlc.arg(from_route_id)
  AND user_id = sqlc.arg(user_id);

-- name: DeleteMergeOverridesByRoute :exec
-- Undoing a route's merges also drops the ones held by runs that aren't being grouped just now (excluded, say), or they'd bring the merge back later.
DELETE FROM route_overrides
WHERE
  route_id = $1
  AND user_id = $2
  AND with_group = TRUE;
