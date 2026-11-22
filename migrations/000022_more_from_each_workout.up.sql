-- What the file states for the whole workout, beside what taiga measures from its records; null where it doesn't say, and for a hand-entered workout.
-- The watch's timer, which stops for pauses; time_seconds is start to end.
ALTER TABLE workouts
ADD COLUMN timer_seconds DOUBLE PRECISION;

ALTER TABLE workouts
ADD COLUMN avg_heart_rate DOUBLE PRECISION;

ALTER TABLE workouts
ADD COLUMN max_heart_rate DOUBLE PRECISION;

ALTER TABLE workouts
ADD COLUMN min_heart_rate DOUBLE PRECISION;

ALTER TABLE workouts
ADD COLUMN ascent_meters DOUBLE PRECISION;

ALTER TABLE workouts
ADD COLUMN descent_meters DOUBLE PRECISION;

ALTER TABLE workouts
ADD COLUMN calories DOUBLE PRECISION;

-- Both feet, for a run or walk as corrected; null for anything else.
ALTER TABLE workouts
ADD COLUMN total_steps DOUBLE PRECISION;

-- Steps a minute for a run or walk, revolutions otherwise, as the series counts it.
ALTER TABLE workouts
ADD COLUMN avg_cadence DOUBLE PRECISION;

ALTER TABLE workouts
ADD COLUMN avg_power_watts DOUBLE PRECISION;

-- COROS's Effort Pace, its grade-adjusted pace, as a speed.
ALTER TABLE workouts
ADD COLUMN avg_effort_pace_mps DOUBLE PRECISION;

ALTER TABLE workouts
ADD COLUMN avg_step_length_m DOUBLE PRECISION;

ALTER TABLE workouts
ADD COLUMN avg_vertical_oscillation_mm DOUBLE PRECISION;

ALTER TABLE workouts
ADD COLUMN avg_ground_contact_ms DOUBLE PRECISION;

ALTER TABLE workouts
ADD COLUMN avg_vertical_ratio_pct DOUBLE PRECISION;

-- The laps the file recorded, as a JSON list (workouts.Lap), rebuilt by reprocessing; no row for a file with none.
CREATE TABLE workout_laps (
  workout_id INTEGER PRIMARY KEY REFERENCES workouts (id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users (id),
  laps BYTEA NOT NULL
);

-- COROS's code for the activity's sport, which its tools need back with the id.
ALTER TABLE source_activities
ADD COLUMN sport_type INTEGER;

-- COROS's own figures for a workout (its activity detail tool), kept for tracking beside what taiga works out; asked of COROS and not derived from the file, so reprocessing leaves it alone.
-- Every figure is null where COROS didn't give it, which depends on the sport.
CREATE TABLE coros_metadata (
  workout_id INTEGER PRIMARY KEY REFERENCES workouts (id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users (id),
  -- COROS's labelId.
  external_id VARCHAR(64) NOT NULL,
  fetched_at TIMESTAMP WITH TIME ZONE NOT NULL,
  -- What COROS said, whole, so a figure not read today can be read later without asking again.
  reply TEXT NOT NULL,
  workout_seconds DOUBLE PRECISION,
  total_seconds DOUBLE PRECISION,
  distance_meters DOUBLE PRECISION,
  avg_pace_s_per_km DOUBLE PRECISION,
  moving_avg_pace_s_per_km DOUBLE PRECISION,
  adjusted_pace_s_per_km DOUBLE PRECISION,
  best_km_seconds DOUBLE PRECISION,
  avg_heart_rate DOUBLE PRECISION,
  avg_cadence DOUBLE PRECISION,
  avg_stride_length_m DOUBLE PRECISION,
  avg_power_watts DOUBLE PRECISION,
  elevation_gain_m DOUBLE PRECISION,
  elevation_loss_m DOUBLE PRECISION,
  calories DOUBLE PRECISION,
  training_load DOUBLE PRECISION,
  aerobic_te DOUBLE PRECISION,
  anaerobic_te DOUBLE PRECISION,
  training_focus TEXT NOT NULL DEFAULT '',
  performance TEXT NOT NULL DEFAULT ''
);
