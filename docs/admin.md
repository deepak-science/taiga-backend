# Admins

The person running a deployment is an account with the admin role. The role gets them the `/admin/...` routes, which show counts, states and errors across every account. They never show anyone's workouts, tracks or files, and never their tokens.

## Making an admin

The API never grants the role, so the first admin is made from inside the deployment:

```bash
useradd -email you@example.com -admin        # a new account with the role
admin -grant you@example.com                 # an existing account
admin -revoke you@example.com
admin -list
```

In the cluster: `kubectl -n taiga exec -it deploy/taiga-backend -- admin -grant you@example.com`.

For a deployment configured by env alone, `auth.admins` (`TAIGA_AUTH_ADMINS`, comma-separated) lists emails whose accounts hold the role too. A listed email only counts once the account's email is verified (made by `useradd`, an admin, a restore of such an account, or a provider that verified it), since open registration takes any email and anyone could register an admin's first. `admin -revoke` can't take the role from a listed account; take it off the list.

The role is read from the database on every admin request, not carried in the token, so revoking it works at once.

## The routes

All behind a JWT, and `404` for anyone without the role, so the surface doesn't show. Every admin request is logged with who made it (`admin: user 1 (you@example.com) GET /admin/accounts`).

- `GET /auth/me` (any signed-in user) - `{id, email, display_name, admin}`; `admin` tells the frontend whether to show the admin link, while the `404` is what keeps others out
- `GET /admin/deployment` - `{build: {version, commit, date}, processor_version, workouts_behind, accounts, workouts, files, migration_version, migration_dirty, features: {registration_open, password_login, oidc_providers, weather, sources, metrics}, queue: {jobs: [{kind, state, jobs}], oldest_due_seconds: {kind: s}, connections: [{kind, state, connections}]}}`. The queue comes from the same snapshot the metrics port serves (`models.MetricsSnapshot`), so this page and Grafana never disagree. `build` is empty outside a Nix build
- `GET /admin/accounts` - `[{id, email, display_name, email_verified, admin, created_at, last_seen_at, password, identities, workouts, files, files_without_workout, cli_sessions}]`; `identities` is the issuers of the account's linked sign-ins, `last_seen_at` when a token was last issued for it (a sign-in or a CLI refresh; null before this was recorded), `files_without_workout` mostly files that couldn't be read
- `POST /admin/accounts` - `useradd` through the API, `{email, display_name, password}` (password at least 8 characters, display name defaulting to the email); the email counts as verified and the account is never an admin. `201`, `409` when an account has the email in any case
- `DELETE /admin/accounts/{id}/sessions` - Revoke every CLI session the account has: `{ended}`. A browser's token can't be taken back and runs out within two hours
- `GET /admin/jobs?state=&kind=&user_id=` - Every account's 200 newest jobs, each filter optional, each job with its account's `email`
- `POST /admin/jobs/{id}/retry` - Requeue any account's dead job; `409` unless it's dead
- `DELETE /admin/jobs/{id}` - Drop a queued or dead job; `204`, `409` while it runs. Whatever queued it may queue it again (the hourly sweeps for syncs and weather, a boot for stale workouts)
- `POST /admin/reprocess` - `{"user_id": N}` queues reprocessing of one account's file-backed workouts, `{"user_id": null}` every account's; `202` with `{enqueued}`, `422` for an unknown account
- `GET /admin/sources` - `{connections, clients}`: every connection's state, `{id, user_id, email, kind, client_id, created_at, synced_through, last_synced_at, last_error, needs_reconnect, activities, files_today, files_limit}` (`files_today` against COROS's daily allowance), and the deployment's registrations with each source, `{id, kind, issuer, redirect_uri, created_at}`. Never the sealed credentials or the registration's secret

## Not yet

- Disabling an account, and opening or closing registration at runtime: both need state the token check doesn't read today
- Replacing the deployment's COROS registration
- Why a file couldn't be read: the reason isn't stored, so the accounts page only counts files without a workout
- Granting the role from an OIDC group or claim
