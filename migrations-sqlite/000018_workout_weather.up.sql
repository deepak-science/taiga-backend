-- What the weather was over a workout, one row per source that has answered (weather.Sources); fetched from outside, so reprocessing leaves it alone.
CREATE TABLE workout_weather (
  workout_id INTEGER NOT NULL REFERENCES workouts (id) ON DELETE CASCADE,
  source VARCHAR(32) NOT NULL,
  user_id INTEGER NOT NULL REFERENCES users (id),
  fetched_at DATETIME NOT NULL,
  -- What the source was asked: the rounded coordinates, and the workout's span as it was then.
  latitude REAL NOT NULL,
  longitude REAL NOT NULL,
  asked_start DATETIME NOT NULL,
  asked_end DATETIME NOT NULL,
  start_temperature_c REAL NOT NULL,
  start_dew_point_c REAL NOT NULL,
  start_humidity_pct REAL NOT NULL,
  start_wind_speed_mps REAL NOT NULL,
  start_wind_direction_deg REAL NOT NULL,
  start_cloud_cover_pct REAL NOT NULL,
  end_temperature_c REAL NOT NULL,
  end_dew_point_c REAL NOT NULL,
  end_humidity_pct REAL NOT NULL,
  end_wind_speed_mps REAL NOT NULL,
  end_wind_direction_deg REAL NOT NULL,
  end_cloud_cover_pct REAL NOT NULL,
  precipitation_mm REAL NOT NULL,
  PRIMARY KEY (workout_id, source)
);
