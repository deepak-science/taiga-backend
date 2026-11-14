-- Whether the account's email is known to belong to its owner: set by an admin's useradd or a provider that verified it, not by open registration. Only such an account may be signed in to by a provider's matching verified email, or anyone could create an account with someone else's email and wait for them to arrive.
-- Existing accounts were made by an admin while registration was closed, so they count as verified.
ALTER TABLE users
ADD COLUMN email_verified BOOLEAN NOT NULL DEFAULT TRUE;
