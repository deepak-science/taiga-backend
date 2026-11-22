-- name: CreateStretch :one
INSERT INTO
  stretches (
    user_id,
    name,
    sport,
    source_workout_id,
    meters,
    points
  )
VALUES
  ($1, $2, $3, $4, $5, $6)
RETURNING
  *;

-- name: GetStretch :one
SELECT
  *
FROM
  stretches
WHERE
  id = $1
  AND user_id = $2;

-- name: ListStretchesByUser :many
SELECT
  *
FROM
  stretches
WHERE
  user_id = $1
ORDER BY
  id;

-- name: ListStretchesByUserAndSport :many
SELECT
  *
FROM
  stretches
WHERE
  user_id = $1
  AND sport = $2
ORDER BY
  id;

-- name: RenameStretch :one
UPDATE stretches
SET
  name = $3
WHERE
  id = $1
  AND user_id = $2
RETURNING
  *;

-- name: DeleteStretch :exec
DELETE FROM stretches
WHERE
  id = $1
  AND user_id = $2;

-- name: CreateStretchEffort :exec
INSERT INTO
  stretch_efforts (
    stretch_id,
    workout_id,
    user_id,
    start_offset_seconds,
    end_offset_seconds,
    duration_seconds,
    start_meters,
    distance_meters,
    walked_fraction
  )
VALUES
  ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: DeleteStretchEffortsByStretch :exec
-- SQLite doesn't enforce ON DELETE CASCADE without a per-connection pragma, so a deleted stretch clears its own efforts.
DELETE FROM stretch_efforts
WHERE
  stretch_id = $1
  AND user_id = $2;

-- name: DeleteStretchEffortsByWorkout :exec
DELETE FROM stretch_efforts
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: ListStretchEffortsByUser :many
-- Every effort with when its workout started, newest first; the stretch list sums them per stretch.
SELECT
  stretch_efforts.*,
  workouts.start_time,
  workouts.name AS workout_name
FROM
  stretch_efforts
  JOIN workouts ON workouts.id = stretch_efforts.workout_id
WHERE
  stretch_efforts.user_id = $1
ORDER BY
  workouts.start_time DESC,
  stretch_efforts.start_offset_seconds;

-- name: ListStretchEffortsByStretch :many
SELECT
  stretch_efforts.*,
  workouts.start_time,
  workouts.name AS workout_name
FROM
  stretch_efforts
  JOIN workouts ON workouts.id = stretch_efforts.workout_id
WHERE
  stretch_efforts.stretch_id = $1
  AND stretch_efforts.user_id = $2
ORDER BY
  workouts.start_time DESC,
  stretch_efforts.start_offset_seconds;

-- name: ListStretchEffortsByWorkout :many
-- A workout's passes in the order it ran them, with each stretch's name.
SELECT
  stretch_efforts.*,
  stretches.name,
  stretches.meters
FROM
  stretch_efforts
  JOIN stretches ON stretches.id = stretch_efforts.stretch_id
WHERE
  stretch_efforts.workout_id = $1
  AND stretch_efforts.user_id = $2
ORDER BY
  stretch_efforts.start_offset_seconds;

-- name: ListStretchEffortsOverWorkoutStretches :many
-- Every effort over the stretches one workout passes, its own among them, with each stretch's name.
-- One statement, so a pass and the efforts it's ranked among are read together.
SELECT
  stretch_efforts.*,
  stretches.name,
  stretches.meters
FROM
  stretch_efforts
  JOIN stretches ON stretches.id = stretch_efforts.stretch_id
WHERE
  stretch_efforts.user_id = sqlc.arg(user_id)
  AND stretch_efforts.stretch_id IN (
    SELECT
      own.stretch_id
    FROM
      stretch_efforts AS own
    WHERE
      own.workout_id = sqlc.arg(workout_id)
      AND own.user_id = sqlc.arg(user_id)
  )
ORDER BY
  stretch_efforts.start_offset_seconds;

-- name: ListStretchEffortsByRoute :many
-- The efforts of a route's runs, newest run first and each run's passes in the order it made them.
SELECT
  stretch_efforts.*,
  workouts.start_time,
  workouts.name AS workout_name
FROM
  stretch_efforts
  JOIN workout_routes ON workout_routes.workout_id = stretch_efforts.workout_id
  JOIN workouts ON workouts.id = stretch_efforts.workout_id
WHERE
  workout_routes.route_id = $1
  AND stretch_efforts.user_id = $2
ORDER BY
  workouts.start_time DESC,
  stretch_efforts.start_offset_seconds;

-- name: ListWorkoutTracksByUserAndSport :many
-- The workouts a new stretch is matched against: tracked (so scored, with GPS) and not a copy of another.
SELECT
  workout_tracks.workout_id,
  workout_tracks.track
FROM
  workout_tracks
  JOIN workouts ON workouts.id = workout_tracks.workout_id
WHERE
  workout_tracks.user_id = $1
  AND workouts.sport = $2
  AND workouts.duplicate_of IS NULL
ORDER BY
  workouts.start_time,
  workouts.id;

-- name: ClearStretchSourceWorkout :exec
-- A deleted workout leaves the stretches cut from it standing, without the pointer back.
UPDATE stretches
SET
  source_workout_id = NULL
WHERE
  source_workout_id = $1
  AND user_id = $2;
