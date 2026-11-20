-- A sign-in a CLI has asked for and is waiting on a signed-in user to approve in a browser; short-lived and used once.
CREATE TABLE device_authorizations (
  id SERIAL PRIMARY KEY,
  -- SHA-256 of the code the CLI polls with, in hex; the code itself is never stored.
  device_code_hash TEXT NOT NULL UNIQUE,
  -- The short code the user reads off the terminal, without its dash.
  user_code TEXT NOT NULL UNIQUE,
  client_name TEXT NOT NULL,
  created_at TIMESTAMP WITH TIME ZONE NOT NULL,
  expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
  -- Who approved it; NULL until someone does.
  user_id INTEGER REFERENCES users (id) ON DELETE CASCADE,
  denied BOOLEAN NOT NULL DEFAULT FALSE,
  last_polled_at TIMESTAMP WITH TIME ZONE
);

-- A CLI that stays signed in: its refresh token buys new access tokens until the session is revoked or lapses unused.
CREATE TABLE cli_sessions (
  id SERIAL PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  -- SHA-256 of the refresh token, in hex.
  token_hash TEXT NOT NULL UNIQUE,
  created_at TIMESTAMP WITH TIME ZONE NOT NULL,
  last_used_at TIMESTAMP WITH TIME ZONE NOT NULL,
  expires_at TIMESTAMP WITH TIME ZONE NOT NULL
);

CREATE INDEX idx_cli_sessions_user_id ON cli_sessions (user_id);
