-- What the file states for the whole workout, beside what taiga measures from its records; null where it doesn't say, and for a hand-entered workout.
-- The watch's timer, which stops for pauses; time_seconds is start to end.
ALTER TABLE workouts
ADD COLUMN timer_seconds REAL;

ALTER TABLE workouts
ADD COLUMN avg_heart_rate REAL;

ALTER TABLE workouts
ADD COLUMN max_heart_rate REAL;

ALTER TABLE workouts
ADD COLUMN min_heart_rate REAL;

ALTER TABLE workouts
ADD COLUMN ascent_meters REAL;

ALTER TABLE workouts
ADD COLUMN descent_meters REAL;

ALTER TABLE workouts
ADD COLUMN calories REAL;

-- Both feet, for a run or walk as corrected; null for anything else.
ALTER TABLE workouts
ADD COLUMN total_steps REAL;

-- Steps a minute for a run or walk, revolutions otherwise, as the series counts it.
ALTER TABLE workouts
ADD COLUMN avg_cadence REAL;

ALTER TABLE workouts
ADD COLUMN avg_power_watts REAL;

-- COROS's Effort Pace, its grade-adjusted pace, as a speed.
ALTER TABLE workouts
ADD COLUMN avg_effort_pace_mps REAL;

ALTER TABLE workouts
ADD COLUMN avg_step_length_m REAL;

ALTER TABLE workouts
ADD COLUMN avg_vertical_oscillation_mm REAL;

ALTER TABLE workouts
ADD COLUMN avg_ground_contact_ms REAL;

ALTER TABLE workouts
ADD COLUMN avg_vertical_ratio_pct REAL;

-- The laps the file recorded, as a JSON list (workouts.Lap), rebuilt by reprocessing; no row for a file with none.
CREATE TABLE workout_laps (
  workout_id INTEGER PRIMARY KEY REFERENCES workouts (id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users (id),
  laps BLOB NOT NULL
);

-- COROS's code for the activity's sport, which its tools need back with the id.
ALTER TABLE source_activities
ADD COLUMN sport_type INTEGER;

-- When COROS's own figures for it were last asked for and couldn't be read, so the ones not yet tried go first.
ALTER TABLE source_activities
ADD COLUMN detail_tried_at DATETIME;

-- COROS's own figures for a workout (its activity detail tool), kept for tracking beside what taiga works out; asked of COROS and not derived from the file, so reprocessing leaves it alone.
-- Every figure is null where COROS didn't give it, which depends on the sport.
CREATE TABLE coros_metadata (
  workout_id INTEGER PRIMARY KEY REFERENCES workouts (id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users (id),
  -- COROS's labelId.
  external_id VARCHAR(64) NOT NULL,
  fetched_at DATETIME NOT NULL,
  -- What COROS said, whole, so a figure not read today can be read later without asking again.
  reply TEXT NOT NULL,
  workout_seconds REAL,
  total_seconds REAL,
  distance_meters REAL,
  avg_pace_s_per_km REAL,
  moving_avg_pace_s_per_km REAL,
  adjusted_pace_s_per_km REAL,
  best_km_seconds REAL,
  avg_heart_rate REAL,
  avg_cadence REAL,
  avg_stride_length_m REAL,
  avg_power_watts REAL,
  elevation_gain_m REAL,
  elevation_loss_m REAL,
  calories REAL,
  training_load REAL,
  aerobic_te REAL,
  anaerobic_te REAL,
  training_focus TEXT NOT NULL DEFAULT '',
  performance TEXT NOT NULL DEFAULT ''
);
