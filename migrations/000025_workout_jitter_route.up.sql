-- The route a jittery run follows despite its drift, and that route's length, which totals count it at; set by route detection, and null for any other run.
-- No foreign key, as with duplicate_of: route detection rewrites the mark on every pass, so a deleted route never outlives the next one.
ALTER TABLE workouts
ADD COLUMN jitter_route_id INTEGER;

ALTER TABLE workouts
ADD COLUMN jitter_route_miles DOUBLE PRECISION;
