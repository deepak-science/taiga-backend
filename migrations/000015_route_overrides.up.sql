-- The user's say over which route a run is on, kept apart from workout_routes so that rebuilding routes applies it again instead of losing it.
CREATE TABLE route_overrides (
  workout_id INTEGER PRIMARY KEY REFERENCES workouts (id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users (id),
  -- The route the run belongs on; NULL keeps it off every route.
  route_id INTEGER REFERENCES routes (id) ON DELETE CASCADE,
  -- TRUE for a merge: the runs detection groups with this one come along. FALSE moves this run alone.
  with_group BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE INDEX idx_route_overrides_user_id ON route_overrides (user_id);
