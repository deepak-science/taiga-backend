-- name: GetWorkout :one
SELECT
  *
FROM
  workouts
WHERE
  id = $1
  AND user_id = $2
LIMIT
  1;

-- name: ListWorkoutsByUser :many
SELECT
  *
FROM
  workouts
WHERE
  user_id = $1
ORDER BY
  start_time DESC;

-- name: CreateWorkout :one
INSERT INTO
  workouts (
    distance_miles,
    time_seconds,
    speed_mph,
    pace_min_per_mile,
    start_time,
    end_time,
    activity_file_id,
    user_id,
    processor_version,
    sport,
    sub_sport,
    excluded_reason,
    source,
    name,
    place,
    timezone,
    utc_offset_seconds
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
    $17
  )
RETURNING
  *;

-- name: UpdateWorkout :one
UPDATE workouts
SET
  distance_miles = $3,
  time_seconds = $4,
  speed_mph = $5,
  pace_min_per_mile = $6,
  start_time = $7,
  end_time = $8,
  activity_file_id = $9,
  updated_at = CURRENT_TIMESTAMP
WHERE
  id = $1
  AND user_id = $2
RETURNING
  *;

-- name: DeleteWorkout :exec
DELETE FROM workouts
WHERE
  id = $1
  AND user_id = $2;

-- name: GetWorkoutsByActivityFile :many
SELECT
  *
FROM
  workouts
WHERE
  activity_file_id = $1
  AND user_id = $2
ORDER BY
  start_time DESC;

-- name: UpdateWorkoutDerived :one
-- Everything reprocessing rebuilds from the activity file, and the corrections it applied over it.
UPDATE workouts
SET
  distance_miles = $3,
  time_seconds = $4,
  speed_mph = $5,
  pace_min_per_mile = $6,
  start_time = $7,
  end_time = $8,
  processor_version = $9,
  sport = $10,
  sub_sport = $11,
  excluded_reason = $12,
  source = $13,
  sport_override = $14,
  user_excluded_reason = $15,
  name = $16,
  place = $17,
  timezone = $18,
  utc_offset_seconds = $19,
  updated_at = CURRENT_TIMESTAMP
WHERE
  id = $1
  AND user_id = $2
RETURNING
  *;

-- name: ListWorkoutsByUserInWindow :many
-- Starting in [window_from, window_to); callers pass far-off bounds for an open end, so the same SQL runs on SQLite.
SELECT
  *
FROM
  workouts
WHERE
  user_id = sqlc.arg(user_id)
  AND start_time >= sqlc.arg(window_from)
  AND start_time < sqlc.arg(window_to)
ORDER BY
  start_time DESC,
  id DESC
LIMIT
  sqlc.arg(page_limit)
OFFSET
  sqlc.arg(page_offset);

-- name: FirstWorkoutStartInWindow :one
-- The earliest start in [window_from, window_to), excluded workouts included since they're still listed; no rows when the window is empty.
SELECT
  start_time
FROM
  workouts
WHERE
  user_id = sqlc.arg(user_id)
  AND start_time >= sqlc.arg(window_from)
  AND start_time < sqlc.arg(window_to)
ORDER BY
  start_time
LIMIT
  1;

-- name: ListCountedWorkoutsInWindow :many
-- What a total needs of each counted workout starting in [window_from, window_to), oldest first; an excluded workout stays out of stats.
-- Bucketing by local week, month or year happens in Go, since the two databases share no date functions.
SELECT
  id,
  name,
  sport,
  start_time,
  distance_miles,
  time_seconds
FROM
  workouts
WHERE
  user_id = sqlc.arg(user_id)
  AND excluded_reason = ''
  AND start_time >= sqlc.arg(window_from)
  AND start_time < sqlc.arg(window_to)
ORDER BY
  start_time,
  id;

-- name: SetWorkoutDuplicateOf :exec
UPDATE workouts
SET
  duplicate_of = $3,
  updated_at = CURRENT_TIMESTAMP
WHERE
  id = $1
  AND user_id = $2;

-- name: SetWorkoutFigures :one
-- What the file states for the whole workout; part of what reprocessing rebuilds.
UPDATE workouts
SET
  timer_seconds = $3,
  avg_heart_rate = $4,
  max_heart_rate = $5,
  min_heart_rate = $6,
  ascent_meters = $7,
  descent_meters = $8,
  calories = $9,
  total_steps = $10,
  avg_cadence = $11,
  avg_power_watts = $12,
  avg_effort_pace_mps = $13,
  avg_step_length_m = $14,
  avg_vertical_oscillation_mm = $15,
  avg_ground_contact_ms = $16,
  avg_vertical_ratio_pct = $17
WHERE
  id = $1
  AND user_id = $2
RETURNING
  *;
