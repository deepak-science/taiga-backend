-- What to call the workout in a list, e.g. "Madison afternoon run", built by processing from where and when it started and its sport; a hand-entered workout is just a "Workout".
ALTER TABLE workouts
ADD COLUMN name VARCHAR(200) NOT NULL DEFAULT 'Workout';

-- The town it started in or beside, from its first GPS fix; empty without one, or out of reach of any town.
ALTER TABLE workouts
ADD COLUMN place VARCHAR(128) NOT NULL DEFAULT '';

-- The IANA zone at the start, from the same fix; empty without one.
ALTER TABLE workouts
ADD COLUMN timezone VARCHAR(64) NOT NULL DEFAULT '';

-- The local offset at the start: the zone's, or else the one the file states; null when neither is known.
ALTER TABLE workouts
ADD COLUMN utc_offset_seconds INTEGER;
