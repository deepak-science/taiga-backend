-- What the weather was over a workout, one row per source that has answered (weather.Sources); fetched from outside, so reprocessing leaves it alone.
CREATE TABLE workout_weather (
  workout_id INTEGER NOT NULL REFERENCES workouts (id) ON DELETE CASCADE,
  source VARCHAR(32) NOT NULL,
  user_id INTEGER NOT NULL REFERENCES users (id),
  fetched_at TIMESTAMP WITH TIME ZONE NOT NULL,
  -- What the source was asked: the rounded coordinates, and the workout's span as it was then.
  latitude DOUBLE PRECISION NOT NULL,
  longitude DOUBLE PRECISION NOT NULL,
  asked_start TIMESTAMP WITH TIME ZONE NOT NULL,
  asked_end TIMESTAMP WITH TIME ZONE NOT NULL,
  start_temperature_c DOUBLE PRECISION NOT NULL,
  start_dew_point_c DOUBLE PRECISION NOT NULL,
  start_humidity_pct DOUBLE PRECISION NOT NULL,
  start_wind_speed_mps DOUBLE PRECISION NOT NULL,
  start_wind_direction_deg DOUBLE PRECISION NOT NULL,
  start_cloud_cover_pct DOUBLE PRECISION NOT NULL,
  end_temperature_c DOUBLE PRECISION NOT NULL,
  end_dew_point_c DOUBLE PRECISION NOT NULL,
  end_humidity_pct DOUBLE PRECISION NOT NULL,
  end_wind_speed_mps DOUBLE PRECISION NOT NULL,
  end_wind_direction_deg DOUBLE PRECISION NOT NULL,
  end_cloud_cover_pct DOUBLE PRECISION NOT NULL,
  precipitation_mm DOUBLE PRECISION NOT NULL,
  PRIMARY KEY (workout_id, source)
);
