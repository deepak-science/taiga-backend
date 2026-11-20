# Signing in from a CLI

A CLI such as `taco` signs in without ever seeing a password: it shows a short code, the user approves that code in a browser already signed in to taiga, and the CLI is given a session of its own.
It works however the account signs in, a provider or a password, and whether or not `auth.password_login` is on.
The shape is OAuth's device authorization grant (RFC 8628) with taiga as the authorization server; the design and its trade-offs are in ROADMAP M14.

## The flow

1. The CLI asks to sign in.

   ```sh
   curl -X POST https://taiga.example.com/api/auth/device -d '{"client_name":"laptop"}'
   ```

   ```json
   {
     "device_code": "…",
     "user_code": "WDJB-MJHT",
     "verification_uri": "https://taiga.example.com/#/device",
     "verification_uri_complete": "https://taiga.example.com/#/device?code=WDJB-MJHT",
     "expires_in": 600,
     "interval": 5
   }
   ```

   `client_name` is what the approval page calls it; it defaults to `taco`. The links are absolute when `auth.public_base_url` is set, and start at `/` otherwise.

2. It shows the user the link and the code, and polls every `interval` seconds:

   ```sh
   curl -X POST https://taiga.example.com/api/auth/device/token -d '{"device_code":"…"}'
   ```

   Until the user decides, the answer is `400` with `{"error": "…"}`:

   | `error` | |
   |---|---|
   | `authorization_pending` | Not approved yet; keep polling. |
   | `slow_down` | Polled sooner than `interval`; wait longer. |
   | `access_denied` | The user turned it away. Stop. |
   | `expired_token` | Ten minutes passed. Start again. |
   | `invalid_grant` | Not a device code taiga knows: never issued, or already used. |

3. The user opens the link, signs in if they aren't, and sees the code and the client's name (`GET /auth/device/{user_code}`). Approving is `POST /auth/device/approve` with `{"user_code": "WDJB-MJHT"}`, denying `POST /auth/device/deny`; both need the user's token. The code is taken in either case, with or without the dash.

4. The next poll answers `200`:

   ```json
   { "token": "…", "refresh_token": "…", "expires_in": 7200 }
   ```

   `token` is the same two-hour token every other sign-in gets, sent as `Authorization: Bearer`. A device code is good for one grant.

5. When the token runs out, the CLI buys another:

   ```sh
   curl -X POST https://taiga.example.com/api/auth/tokens/refresh -d '{"refresh_token":"…"}'
   ```

   `200` with `{"token": "…", "expires_in": 7200}`, or `400` `invalid_grant` once the session is revoked or has lapsed, which means signing in again. The refresh token stays the same for the life of the session.

## Sessions

Each approved sign-in is a session, named after the client.

- `GET /auth/sessions` lists the user's: `{id, name, created_at, last_used_at, expires_at}`.
- `DELETE /auth/sessions/{id}` revokes one. Its CLI keeps working until the token it holds runs out, at most two hours, and can't get another.
- `POST /auth/tokens/revoke` with `{"refresh_token": "…"}` is the CLI signing itself out; `204` whether or not the token was still good.
- A session lapses after 90 days without a refresh; each refresh starts the 90 days again.

## What's kept, and what isn't

- The device code and the refresh token are stored only as SHA-256 hashes; the CLI holds the only copies. The user code is stored as it is: it's short-lived and worth nothing without a signed-in user to approve it.
- A sign-in nobody approves is deleted when it runs out. About a hundred may be waiting at once (`429` past that), since asking for one takes no credentials; requests arriving together can run a few over.
- Approving a code signs that CLI in as you. Only approve a sign-in you started, on a terminal showing the same code: the link someone sends you is theirs, not yours.
