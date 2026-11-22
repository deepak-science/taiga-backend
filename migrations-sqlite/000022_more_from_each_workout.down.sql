DROP TABLE IF EXISTS coros_metadata;

ALTER TABLE source_activities
DROP COLUMN detail_tried_at;

ALTER TABLE source_activities
DROP COLUMN sport_type;

DROP TABLE IF EXISTS workout_laps;

ALTER TABLE workouts
DROP COLUMN avg_vertical_ratio_pct;

ALTER TABLE workouts
DROP COLUMN avg_ground_contact_ms;

ALTER TABLE workouts
DROP COLUMN avg_vertical_oscillation_mm;

ALTER TABLE workouts
DROP COLUMN avg_step_length_m;

ALTER TABLE workouts
DROP COLUMN avg_effort_pace_mps;

ALTER TABLE workouts
DROP COLUMN avg_power_watts;

ALTER TABLE workouts
DROP COLUMN avg_cadence;

ALTER TABLE workouts
DROP COLUMN total_steps;

ALTER TABLE workouts
DROP COLUMN calories;

ALTER TABLE workouts
DROP COLUMN descent_meters;

ALTER TABLE workouts
DROP COLUMN ascent_meters;

ALTER TABLE workouts
DROP COLUMN min_heart_rate;

ALTER TABLE workouts
DROP COLUMN max_heart_rate;

ALTER TABLE workouts
DROP COLUMN avg_heart_rate;

ALTER TABLE workouts
DROP COLUMN timer_seconds;
