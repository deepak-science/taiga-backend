-- Whoever runs the deployment: granted by useradd -admin or cmd/admin, never through the API, and checked from here on each admin request rather than carried in a token.
ALTER TABLE users
ADD COLUMN is_admin BOOLEAN NOT NULL DEFAULT FALSE;

-- When a token was last issued for the account: a sign-in, or a CLI session's refresh.
ALTER TABLE users
ADD COLUMN last_seen_at DATETIME;
