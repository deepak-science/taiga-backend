-- A named piece of ground the user picked from one of their workouts, timed across every workout that passes over it (workouts.Stretch).
CREATE TABLE stretches (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users (id),
  name TEXT NOT NULL,
  -- Only workouts of this sport are matched against it.
  sport TEXT NOT NULL,
  -- The workout it was cut from, kept as a note of where it came from; cleared when that workout is deleted.
  source_workout_id INTEGER,
  meters REAL NOT NULL,
  -- [lat, lon] pairs as JSON, first at the start.
  points BLOB NOT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE INDEX idx_stretches_user_id ON stretches (user_id);

-- One row for each time a workout covers a stretch end to end; derived, rebuilt when the workout is reprocessed or the stretch is made.
CREATE TABLE stretch_efforts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  stretch_id INTEGER NOT NULL REFERENCES stretches (id) ON DELETE CASCADE,
  workout_id INTEGER NOT NULL REFERENCES workouts (id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users (id),
  start_offset_seconds REAL NOT NULL,
  end_offset_seconds REAL NOT NULL,
  duration_seconds REAL NOT NULL,
  -- Where along the workout the pass begins, and how much ground it covered.
  start_meters REAL NOT NULL,
  distance_meters REAL NOT NULL,
  -- The share of the pass walked; NULL when the workout can't tell walking from running.
  walked_fraction REAL
);

CREATE INDEX idx_stretch_efforts_stretch_id ON stretch_efforts (stretch_id);

CREATE INDEX idx_stretch_efforts_workout_id ON stretch_efforts (workout_id);

CREATE INDEX idx_stretch_efforts_user_id ON stretch_efforts (user_id);
