-- name: UpsertWorkoutWeather :exec
INSERT INTO
  workout_weather (
    workout_id,
    source,
    user_id,
    fetched_at,
    latitude,
    longitude,
    asked_start,
    asked_end,
    start_temperature_c,
    start_dew_point_c,
    start_humidity_pct,
    start_wind_speed_mps,
    start_wind_direction_deg,
    start_cloud_cover_pct,
    end_temperature_c,
    end_dew_point_c,
    end_humidity_pct,
    end_wind_speed_mps,
    end_wind_direction_deg,
    end_cloud_cover_pct,
    precipitation_mm
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
    $21
  )
ON CONFLICT (workout_id, source) DO UPDATE
SET
  fetched_at = excluded.fetched_at,
  latitude = excluded.latitude,
  longitude = excluded.longitude,
  asked_start = excluded.asked_start,
  asked_end = excluded.asked_end,
  start_temperature_c = excluded.start_temperature_c,
  start_dew_point_c = excluded.start_dew_point_c,
  start_humidity_pct = excluded.start_humidity_pct,
  start_wind_speed_mps = excluded.start_wind_speed_mps,
  start_wind_direction_deg = excluded.start_wind_direction_deg,
  start_cloud_cover_pct = excluded.start_cloud_cover_pct,
  end_temperature_c = excluded.end_temperature_c,
  end_dew_point_c = excluded.end_dew_point_c,
  end_humidity_pct = excluded.end_humidity_pct,
  end_wind_speed_mps = excluded.end_wind_speed_mps,
  end_wind_direction_deg = excluded.end_wind_direction_deg,
  end_cloud_cover_pct = excluded.end_cloud_cover_pct,
  precipitation_mm = excluded.precipitation_mm;

-- name: ListWorkoutWeather :many
SELECT
  *
FROM
  workout_weather
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: DeleteWorkoutWeather :exec
DELETE FROM workout_weather
WHERE
  workout_id = $1
  AND user_id = $2;

-- name: EnqueueFetchWeather :exec
INSERT INTO
  jobs (user_id, kind, workout_id, run_at)
VALUES
  ($1, 'fetch_weather', $2, $3)
ON CONFLICT DO NOTHING;

-- name: EnqueueWeatherDue :execrows
-- Every user's tracked workouts that ended by the cutoff and have no reading from the source, newest first.
-- One whose last fetch died waits for a retry through /jobs, so an outage doesn't queue it again every sweep; one with a fetch pending is left out so it doesn't use up the limit.
INSERT INTO
  jobs (user_id, kind, workout_id, run_at)
SELECT
  workouts.user_id,
  'fetch_weather',
  workouts.id,
  sqlc.arg(run_at)
FROM
  workouts
  JOIN workout_tracks ON workout_tracks.workout_id = workouts.id
WHERE
  workouts.end_time <= sqlc.arg(ended_by)
  AND NOT EXISTS (
    SELECT
      1
    FROM
      workout_weather
    WHERE
      workout_weather.workout_id = workouts.id
      AND workout_weather.source = sqlc.arg(source)
  )
  AND NOT EXISTS (
    SELECT
      1
    FROM
      jobs AS held
    WHERE
      held.kind = 'fetch_weather'
      AND held.workout_id = workouts.id
      AND held.state IN ('queued', 'running', 'dead')
  )
ORDER BY
  workouts.start_time DESC
LIMIT
  sqlc.arg(at_most)
ON CONFLICT DO NOTHING;

-- name: ListWorkoutWeatherByUser :many
SELECT
  *
FROM
  workout_weather
WHERE
  user_id = $1
ORDER BY
  workout_id,
  source;
