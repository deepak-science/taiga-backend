-- The workout this one is another recording of, from a richer source; reprocessing keeps a duplicate out of best efforts, routes and stats.
-- No foreign key: when the original is deleted, duplicate detection sees the dangling id and reprocesses this one so it counts again.
ALTER TABLE workouts
ADD COLUMN duplicate_of INTEGER;
