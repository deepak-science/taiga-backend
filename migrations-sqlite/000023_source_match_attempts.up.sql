-- A workout a source's list was searched for by its start and didn't have, so the walk back through the source's history doesn't look for it again.
CREATE TABLE source_match_attempts (
  workout_id INTEGER NOT NULL REFERENCES workouts (id) ON DELETE CASCADE,
  kind VARCHAR(32) NOT NULL,
  user_id INTEGER NOT NULL REFERENCES users (id),
  tried_at DATETIME NOT NULL,
  PRIMARY KEY (workout_id, kind)
);
