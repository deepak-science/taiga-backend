-- This deployment as a source's OAuth server knows it: registered once, the first time a user connects.
CREATE TABLE source_clients (
  id SERIAL PRIMARY KEY,
  kind VARCHAR(32) NOT NULL,
  issuer TEXT NOT NULL,
  redirect_uri TEXT NOT NULL,
  -- The client's id and secret and the server's addresses, sealed.
  registration TEXT NOT NULL,
  created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL,
  UNIQUE (kind, issuer, redirect_uri)
);

-- A user's account at a source, which a sync_source job reads new activities from.
CREATE TABLE source_connections (
  id SERIAL PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users (id),
  kind VARCHAR(32) NOT NULL,
  client_id INTEGER NOT NULL REFERENCES source_clients (id),
  -- The user's tokens, sealed.
  credentials TEXT NOT NULL,
  -- Activities starting before this have all been dealt with.
  synced_through TIMESTAMP WITH TIME ZONE NOT NULL,
  last_synced_at TIMESTAMP WITH TIME ZONE,
  -- Why the last sync failed; empty when it didn't.
  last_error TEXT NOT NULL DEFAULT '',
  -- The source turned the tokens away, so nothing syncs until the user connects again.
  needs_reconnect BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL,
  UNIQUE (user_id, kind)
);

-- Every activity a sync has dealt with, so none is fetched twice: the source limits fetches, and a workout the user deleted should stay deleted.
-- By user and not connection, so disconnecting and connecting again doesn't start over.
CREATE TABLE source_activities (
  user_id INTEGER NOT NULL REFERENCES users (id),
  kind VARCHAR(32) NOT NULL,
  external_id VARCHAR(64) NOT NULL,
  activity_file_id INTEGER REFERENCES activity_file (id) ON DELETE SET NULL,
  start_time TIMESTAMP WITH TIME ZONE,
  -- How many files it took, which is what the source's daily limit counts.
  files INTEGER NOT NULL DEFAULT 0,
  fetched_at TIMESTAMP WITH TIME ZONE NOT NULL,
  PRIMARY KEY (user_id, kind, external_id)
);

CREATE INDEX idx_source_activities_fetched ON source_activities (user_id, kind, fetched_at);
