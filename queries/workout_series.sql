-- name: UpsertWorkoutSeries :exec
INSERT INTO
  workout_series (workout_id, user_id, series)
VALUES
  ($1, $2, $3)
ON CONFLICT (workout_id) DO UPDATE
SET
  series = excluded.series;

-- name: GetWorkoutSeries :one
SELECT
  series
FROM
  workout_series
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: DeleteWorkoutSeries :exec
DELETE FROM workout_series
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: UpsertWorkoutLaps :exec
INSERT INTO
  workout_laps (workout_id, user_id, laps)
VALUES
  ($1, $2, $3)
ON CONFLICT (workout_id) DO UPDATE
SET
  laps = excluded.laps;

-- name: GetWorkoutLaps :one
SELECT
  laps
FROM
  workout_laps
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: DeleteWorkoutLaps :exec
DELETE FROM workout_laps
WHERE
  workout_id = $1
  AND user_id = $2;
