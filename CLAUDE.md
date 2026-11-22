# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Architecture

This is the taiga API: upload FIT files (or Endomondo export JSON, or TCX such as Google Fit's Takeout), derive workouts, segments and per-record series from them, and serve them to the browser frontend and the `taco` CLI. PostgreSQL in production, SQLite in development, JWT auth. The application follows clean architecture principles with clear separation of concerns.

### Core Components

- **cmd/server**: Application entrypoint with graceful shutdown and automatic migrations
- **cmd/migrate**: Standalone migration tool with up/down/steps/goto commands
- **cmd/useradd**: Create an account while registration is closed
- **cmd/fit_view**: Utility for viewing FIT (Garmin) workout files

- **internal/config**: Configuration management with Viper
  - YAML config files and environment variable support
  - Database driver switching (PostgreSQL/SQLite)
  - JWT token configuration for RSA keys
  - File repository configuration
  - Development/production environment detection

- **internal/routes**: HTTP request handlers and routing
  - Auth endpoints with JWT token generation/validation
  - Health checks with database connectivity
  - Activity files API for secure file upload/download
  - Protected routes using JWT middleware

- **internal/server**: HTTP server setup and middleware
  - Chi router with standard middleware
  - CORS support for development
  - Connection pooling and timeouts

- **internal/store**: Database abstraction layer
  - PostgreSQL and SQLite implementations
  - Error handling and connection management
  - Interface-based design for testability

- **internal/db**: Generated sqlc code (after running `sqlc generate`)
  - Type-safe database operations
  - Compiled SQL queries with Go interfaces

- **internal/oidc**: Sign-in through OpenID Connect providers (`docs/oidc.md`)
  - Provider registry from `auth.oidc`, endpoints from each issuer's discovery document, checked at boot
  - Authorization code flow with PKCE; state, nonce and verifier in a signed, HttpOnly cookie scoped to the callback path

- **internal/tokens**: JWT token management
  - RSA key-based signing and verification
  - Middleware for route authentication
  - Token generation and validation

- **internal/filerepo**: Content-addressable file storage
  - SHA-256 based file organization
  - Configurable prefix length for directory structure

- **internal/weather**: What the weather was over a workout (`docs/weather.md`)
  - `Provider` with one implementation, Open-Meteo's archive API; `Sources` is the ranked list of answers it can give
  - Off unless `weather.provider` is set, since a lookup tells the provider roughly where and when the user ran

- **internal/places**: The town a point is in or beside, and its timezone
  - GeoNames' cities5000 cut down to `cities.tsv.gz` and embedded (`go run ./internal/places/gen cities5000.zip internal/places/cities.tsv.gz` rebuilds it), so no lookup leaves the server
  - `Lookup` has centres and populations, not outlines: a town's reach grows with its size, and a point goes to the town it's deepest inside

- **internal/workouts**: Activity file processing
  - FIT, Endomondo JSON and TCX parsing (`ReadActivityFile` tells them apart by content) and workout data extraction
  - Segment analysis and activity processing
  - `BuildSeries` (per-record columns, thinned to `MaxSeriesPoints`) and `ProcessorVersion`
  - `DetectWalking` (a run's walking breaks, warm-up and cool-down) and `Stretch` (a named piece of ground and the passes over it)
  - `RunSummary.Name` (what to call a workout, e.g. "Madison afternoon run")

- **internal/models**: The model layer handlers call; processing a file into a workout (`processing.go`) and the job queue (`jobs.go`)

- **internal/jobs**: The in-process worker that drains the `jobs` table (see Reprocessing below)

- **internal/migration**: golang-migrate wrapper used on startup and by `cmd/migrate`

- **internal/testsupport**: Real dependencies for integration tests (see Testing below)

## Development Commands

### Setup and Code Generation
```bash
go mod tidy              # Install dependencies
just chores              # Run gomod2nix (with --with-deps) and sqlc generate
sqlc generate           # Generate database code from SQL queries
```

### Running the Application
```bash
just serve               # Run server with go run (recommended)
go run cmd/server/main.go # Run with SQLite (default)
just build               # Build using nix
nix build                # Alternative nix build
```

### Testing and Development
```bash
just test                # Run full test suite with coverage
just full_test           # Run tests + sqlc vet + sqlc diff
just vet_sqlc            # Validate sqlc configuration
just fmt                 # Format code using nix
just update              # nix flake update
```

### Commits and the changelog
Every commit subject is a [conventional commit](https://www.conventionalcommits.org): `<type>[(scope)][!]: <description>`, type one of `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`, `style`, `revert` (e.g. `feat(routes): rename a route`, `docs(roadmap): ...`, `fix!: ...` for a breaking change).
hanko reads them for the bump (`feat` → minor, `!`/`BREAKING CHANGE` → major, else patch) and git-cliff (`cliff.toml`) groups them into `CHANGELOG.md`; commits from before the switch land under "Other".
Merge commits are exempt.
`scripts/check-commits.sh` checks them: the `commits` workflow runs it on every PR's commits and title (a squash merge takes the title), and `just install-hooks` makes it the local `commit-msg` hook.

### Releasing (hanko)
The project uses [hanko](https://github.com/dmallubhotla/hanko) `seal` to bump
the version stamped in `flake.nix`, regenerate `CHANGELOG.md` (git-cliff, a `seal.pre-commit` hook), create the release commit, tag, and push.
Configuration lives in `.hanko.yaml`.

```bash
just release-plan        # Preview what `just release` would do (dry run)
just release             # Stamp version, write the changelog, commit, tag, push
just changelog           # Regenerate CHANGELOG.md with unreleased commits under [Unreleased]
```

A `seal` GitHub workflow (`.github/workflows/seal.yml`) does the same thing on
`workflow_dispatch`, signed by a GitHub App token (secrets:
`RELEASE_APP_CLIENT_ID`, `RELEASE_APP_PRIVATE_KEY`).

### JWT Key Generation (Development Only)
```bash
just generate_keypair    # Generate RSA keypair for JWT signing
```

### Docker Commands
```bash
just build-docker        # Build Docker image with nix
just load-docker         # Load Docker image into Docker daemon
just build-load          # Build and load Docker image
just exec-docker         # Run interactive shell in Docker container
```

### Utility Commands
```bash
go run cmd/fit_view/main.go -filename file.fit  # View FIT workout files
go run cmd/migrate/main.go [options]    # Manual database migrations
go run cmd/useradd/main.go -email you@example.com  # Create an account while registration is closed (prompts for the password; in the cluster: kubectl -n taiga exec -it deploy/taiga-backend -- useradd -email ...)
```

## Project Structure

- **Go 1.26** with modern module structure
- **sqlc** for type-safe SQL code generation
- **golang-migrate** for database migrations
- **Chi v5** router with middleware support
- **Viper** for configuration management
- **JWT** with RSA key authentication
- **PostgreSQL** (production) and **SQLite** (development) support
- **Nix flakes** for reproducible development environment
- **Just** task runner for common operations
- **Content-addressable file storage** for binary assets
- **FIT file processing** for Garmin workout data
- **Docker deployment** with layered image building

## Database Operations

### Adding New Queries

1. **Write SQL in `queries/` directory**:
   ```sql
   -- name: GetUser :one
   SELECT * FROM users WHERE id = $1;
   ```

   `.sql-formatter.json` tells `just fmt` (sql-formatter, via treefmt) to leave `@name` and `sqlc.arg(...)`/`sqlc.embed(...)` alone; without it they become `@ name` and `sqlc.embed (t)`, which sqlc can't parse.

2. **Generate Go code**:
   ```bash
   sqlc generate
   ```

3. **Use in handlers**:
   ```go
   user, err := queries.GetUser(ctx, userID)
   ```

### Database Migrations

The project uses **golang-migrate** for database schema management with proper up and down migrations.

#### Migration Files
- Two directories with the same numbering: `migrations/` (PostgreSQL) and `migrations-sqlite/` (SQLite); every migration needs both
- Naming convention: `NNNNNN_description.up.sql` and `NNNNNN_description.down.sql`
- Use the next free number after the highest existing one

#### Migration Commands

**Using the CLI tool:**
```bash
# Run all up migrations
go run cmd/migrate/main.go -command=up

# Rollback all migrations
go run cmd/migrate/main.go -command=down

# Check current version
go run cmd/migrate/main.go -command=version

# Step up/down by N migrations
go run cmd/migrate/main.go -command=steps -steps=1   # up 1
go run cmd/migrate/main.go -command=steps -steps=-1  # down 1

# Migrate to specific version
go run cmd/migrate/main.go -command=goto -version=2
```

**Automatic migrations:**
- Migrations run automatically on server startup when `db.auto_migrate_up: true` (default)
- `db.drop_on_start: true` (development only) drops every table on startup

#### Creating New Migrations

1. **Create up migration:**
   ```sql
   -- migrations/NNNNNN_add_comments.up.sql (and the same in migrations-sqlite/)
   CREATE TABLE comments (
       id SERIAL PRIMARY KEY,
       post_id INTEGER REFERENCES posts(id),
       content TEXT NOT NULL,
       created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
   );
   ```

2. **Create down migration:**
   ```sql
   -- migrations/NNNNNN_add_comments.down.sql
   DROP TABLE IF EXISTS comments;
   ```

3. **Run migrations:**
   ```bash
   go run cmd/migrate/main.go -command=up
   ```

## Configuration

### Config File (`config.yaml`)
```yaml
app:
  port: "8080"
  environment: "development"

auth:
  registration: "open"             # "open" or "closed"; defaults to open only in development
  password_login: true             # false turns off /auth/register and /auth/tokens (SSO only)
  public_base_url: "https://taiga.example.com"  # the frontend's origin; needed for OIDC
  # api_base_url defaults to public_base_url + "/api"; provider callbacks are {api_base_url}/auth/oidc/{name}/callback
  oidc:                            # see docs/oidc.md; from env, TAIGA_AUTH_OIDC holds this list as JSON
    - name: authentik
      display_name: "authentik"
      issuer: "https://auth.example.com/application/o/taiga/"
      client_id: "..."
      client_secret_file: "/run/secrets/taiga-oidc-authentik"
      scopes: [openid, email, profile]   # the default
      allow_signup: true               # the default: an unknown identity gets a new account
      link_by_verified_email: false    # the default

db:
  driver: "sqlite"                 # "sqlite" or "postgres"
  filepath: "./data.db"            # for sqlite
  migration_path: "./migrations-sqlite"
  drop_on_start: false             # dev only - drops all tables on start
  auto_migrate_up: true            # automatically run migrations on startup

tokens:
  public_key_path: "cert/test.pem"   # RSA public key for JWT verification
  private_key_path: "cert/test.key"  # RSA private key for JWT signing

file_repo:
  type: "local"                      # "local" or "s3" (s3_bucket, s3_region, s3_prefix)
  asset_path: "local/filerepo/"      # content-addressable file storage
  prefix_length: 2                   # directory structure depth

weather:
  provider: ""                       # "open-meteo" looks up each tracked workout's weather; off by default (docs/weather.md)
```

### Environment Variables
- Prefix: `TAIGA_`
- Examples: `TAIGA_APP_PORT`, `TAIGA_DB_DRIVER`, `TAIGA_DB_HOST`, `TAIGA_TOKENS_PRIVATE_KEY_PATH`
- Override any config file setting using dot notation converted to underscores, e.g. `db.migration_path` → `TAIGA_DB_MIGRATION_PATH`; every field in `config.Config` is bound, so a deployment can be configured by env alone

## API Endpoints

### Public Endpoints
- `GET /` - Hello World with service info
- `GET /ping` - Simple ping/pong response
- `GET /health` - Health check with database connectivity status
- `POST /auth/register` - User registration; `403` when `auth.registration` is closed (env `TAIGA_AUTH_REGISTRATION=open|closed`, default open in development and closed otherwise) or `auth.password_login` is off
- `GET /auth/providers` - What the login page should offer: `{password_login, registration_open, device_login, providers: [{name, display_name}]}` (`device_login` says a CLI can sign in through `POST /auth/device`)
- `POST /auth/tokens` - User login with `{email, password}` (returns a JWT token); `403` when `auth.password_login` is off
- `GET /auth/oidc/{provider}/login` - Redirect to the provider to sign in
- `GET /auth/oidc/{provider}/callback` - Where the provider sends the browser back; verifies the ID token, finds the account by (issuer, subject), links or creates one as the provider allows, and redirects to the frontend's `#/login/callback?token=…` (or `?error=…`), or to `#/account?linked=…` after linking

### Linked sign-ins (requires JWT token)
- `GET /auth/identities` - `{password, identities: [{id, provider, display_name, issuer, email_at_link, created_at}]}`; `password` is whether the user can also sign in with one
- `POST /auth/identities/{provider}` - Start linking the provider to the signed-in account: `{url}` to send the browser to, which comes back through the callback
- `DELETE /auth/identities/{id}` - Unlink; `409` when it's the user's only way to sign in

### Signing in from a CLI (`docs/cli-sign-in.md`)
- `POST /auth/device` - A CLI asks to sign in, optionally `{"client_name": "laptop"}`: `{device_code, user_code, verification_uri, verification_uri_complete, expires_in, interval}`; `429` when about a hundred are already waiting
- `POST /auth/device/token` - The CLI polls with `{"device_code": "..."}`: `{token, refresh_token, expires_in}` once approved, until then `400` with `{"error": ...}`, one of `authorization_pending`, `slow_down`, `access_denied`, `expired_token`, `invalid_grant`
- `POST /auth/tokens/refresh` - `{"refresh_token": "..."}` buys a new `{token, expires_in}`; `400` `invalid_grant` once the session is revoked or lapsed
- `POST /auth/tokens/revoke` - `{"refresh_token": "..."}` ends that session; `204` either way
- `GET /auth/device/{user_code}` (requires JWT token) - The sign-in waiting on that code, `{user_code, client_name, created_at, expires_at}`; `404` once decided or run out
- `POST /auth/device/approve`, `POST /auth/device/deny` (requires JWT token) - `{"user_code": "WDJB-MJHT"}` signs the waiting CLI in as the user, or turns it away; `204`, `404` once decided or run out
- `GET /auth/sessions` (requires JWT token) - The user's CLI sessions, most recently used first: `{id, name, created_at, last_used_at, expires_at}`
- `DELETE /auth/sessions/{id}` (requires JWT token) - Revoke one; `204`

### Activity Files API (requires JWT token)
- `GET /activity_files` - List all activity files for authenticated user (newest first); `?without_workout=true` lists only files with no workout, e.g. ones that failed to parse
- `GET /activity_files/{id}` - Get specific activity file metadata
- `POST /activity_files` - Upload new activity file (multipart form with `file` and optional `timestamp`); `201` when new, `200` with the existing file when the user already uploaded the same content
- `PUT /activity_files/{id}` - Update the activity file's `timestamp`; nothing else can be set, and `400` for any other field
- `DELETE /activity_files/{id}` - Delete the activity file record (stored content is shared by hash and kept); `409` while workouts still reference it
- `GET /activity_files/{id}/download` - Download the actual file content

### Workouts and Segments API (requires JWT token)
- Every workout carries `name` (e.g. `Madison afternoon run`; see Reprocessing below), `place`, `timezone` (IANA) and `utc_offset_seconds` (null when unknown) beside `start_time`, which stays UTC; a best effort and a stretch effort carry their workout's as `workout_name`
- `GET /workouts` - List workouts (newest first); `?from=&to=` keeps those starting in that window (from inclusive, to exclusive; RFC3339, or a bare date as UTC midnight; either end optional), `?limit=` (1–1000) and `?offset=` page through it, and without a limit it returns the whole window; `GET /workouts/{id}`, `POST`, `PUT /workouts/{id}`, `DELETE /workouts/{id}` for CRUD. `PUT` only edits hand-entered workouts; one from a file is `422`, since reprocessing owns its summary (correct its sport or exclusion instead)
- `POST /workouts/from-activity-file/{id}` - Parse an uploaded FIT, Endomondo JSON or TCX file into a workout and store its segments; `201` when new, `200` with the existing workout if the file already has one, `404` for an unknown file, `422` with the reason for an unreadable or multi-session file
- `GET /workouts/{id}/segments` - Fastest effort at each standard distance the workout covers, shortest distance first, then the farthest effort for each standard duration it lasts (`kind` is `distance` or `duration`)
- `GET /workouts/{id}/series` - The workout's records as parallel columns (`time_s`, `distance_m`, `speed_mps`, `altitude_m`, `heart_rate`, `lat`, `lon`, `cadence`; null where a sample is missing, a column omitted if never recorded), at most 2000 points, and for a run where it can be told, `walking`: `{method, warmup_s, cooldown_s, walk_s, breaks: [{start_s, end_s}]}`; gzipped; `404` for a hand-entered workout
- `POST /workouts/{id}/reprocess` - Rebuild the workout's summary and segments from its file now and return it; `422` for a hand-entered workout with no file
- `PUT /workouts/{id}/corrections` - Replace a file-backed workout's corrections, `{"sport": "walking", "excluded_reason": "ran with a friend"}` (a FIT sport name; a reason of at most 64 characters), and return it reprocessed with them; a field left out or empty clears that correction. `400` for an unknown sport, `422` for a hand-entered workout
- `POST /workouts/reprocess` - Queue reprocessing of all the user's file-backed workouts; `202` with `{enqueued}`, progress under `/jobs`
- `GET /workouts/totals?from=&to=` - `{sports, first_start_time}`: per sport, the count (`workouts`), `distance_miles` and `time_seconds` of the workouts starting in the window that count (an `excluded_reason` keeps one out), most workouts first; and the earliest start of any workout listed in the window, so a client can offer the years there are (null when there are none)
- `GET /workouts/totals/week` and `GET /workouts/totals/month`, `?from=&to=&tz=` - `[{start, sports}]`, the same per-sport sums for every week (from Monday) or month across the window, empty ones included, at the local midnights of `tz` (an IANA zone such as `America/Chicago`; default UTC). An open end stops at the first or last counted workout; a period straddling an edge only sums what's inside; `400` past 1000 periods
- `GET /workouts/compare?ids=1,2` - Up to 10 workouts with segments keyed by name, plus `distances` (the segment names any of them have, durations last)
- `GET /segments` - The standard segments: distances (`1k`, `1mi`, `3k`, `2mi`, `5k`, `5mi`, `10k`, `half`, `marathon`) with `meters`, then durations (`30min`, `1h`) with `seconds`, each with its `kind`
- `GET /segments/{name}/best?sport=running|walking&limit=N&from=&to=` - The user's best efforts at that segment across workouts of one sport (default `running`; default 10, max 100) starting in the window (same `from`/`to` as `GET /workouts`; default all time): fastest for a distance, farthest for a duration
- `GET /segments/yearly-bests?sport=running|walking&tz=` - `[{year, efforts}]`, newest first: for each local calendar year with any, the best effort at every standard segment, keyed by name (each effort as in `/best`)

### Stretches API (requires JWT token)
- `GET /stretches` - The user's stretches, named pieces of ground timed across workouts: `{id, name, sport, meters, source_workout_id, created_at, effort_count, workout_count, best_seconds, best_workout_id, latest_start_time}`; `?from=&to=` (as on `GET /workouts`) counts only efforts by workouts starting in the window
- `POST /stretches` - Cut a stretch from a workout with `{"name": "...", "workout_id": N, "start_m": 500, "end_m": 1500}` (distances along the workout, as its series' `distance_m` counts) and time every workout of that sport over it; `201` with the stretch as `GET /stretches/{id}` gives it. `400` without a name (trimmed, at most 100 characters) or range, `422` for an unknown, hand-entered or uncounted workout, one with no GPS or too little for a track (under 200m), or a range outside its track or not between 100m and 20km
- `GET /stretches/{id}` - A stretch with its `points` (`[lat, lon]` every 10m, first at the start) and `efforts`, newest first: `{workout_id, workout_name, start_time, start_offset_seconds, end_offset_seconds, duration_seconds, start_meters, distance_meters, walked_fraction}` (`walked_fraction` null when the workout can't tell walking from running); `?from=&to=` keeps efforts in the window, and the figures follow
- `PUT /stretches/{id}` - Rename with `{"name": "..."}`
- `DELETE /stretches/{id}` - Delete the stretch and its efforts; `204`
- `GET /workouts/{id}/stretches` - The workout's passes over the user's stretches in the order it ran them, each effort with `stretch_id`, `name` and `stretch_meters`

### Weather API (requires JWT token; `docs/weather.md`)
- `GET /workouts/{id}/weather` - `{source, fetched_at, start, end, precipitation_mm, sources}`: the reading from the best source the workout has, `start` and `end` each `{temperature_c, dew_point_c, humidity_pct, wind_speed_mps, wind_direction_deg, cloud_cover_pct}`, and every source's reading under `sources`, best first; `404` when no source has answered (weather off, no track, or not fetched yet)

### Routes API (requires JWT token)
- `GET /routes` - The user's routes, most recently run first; `?from=&to=` (as on `GET /workouts`) keeps routes run in the window, and counts only those runs: `{id, name, sport, representative_workout_id, run_count, distance_miles, best_time_seconds, best_workout_id, latest_start_time}`
- `GET /routes/{id}` - A route with its `workouts` (newest first; `?from=&to=` keeps those in the window, and the figures follow), `track`, the representative run's resampled `[lat, lon]` points, and `moved_workout_ids` and `merged_workout_ids`, the runs the user put there
- `PUT /routes/{id}` - Rename with `{"name": "..."}` (trimmed, at most 100 characters); an empty or null name clears it
- `POST /routes/{id}/merge` - Merge another route into this one with `{"route_id": N}` and return it; the other route is deleted, and its name carries over if this one has none. `422` for an unknown route, itself, or one of another sport
- `DELETE /routes/{id}/merges` - Undo every merge into the route; `204`
- `GET /workouts/{id}/route` - The route a workout is on; `404` when it has none (no GPS, nothing else matches it yet, or the user took it off)
- `GET /workouts/{id}/route/override` - The user's placement of the workout, `{workout_id, route_id}` (a null `route_id` keeps it off every route); `404` when detection placed it
- `PUT /workouts/{id}/route/override` - Move the workout onto a route with `{"route_id": N}`, or off every route with `{"route_id": null}`; `422` for an unknown route, one of another sport, or a workout with no GPS track
- `DELETE /workouts/{id}/route/override` - Hand the workout back to detection; `204`

### Jobs API (requires JWT token)
- `GET /jobs?state=queued|running|done|dead` - The user's 100 newest jobs, optionally in one state; done jobs are pruned after a week
- `GET /jobs/{id}` - One job, with `attempts` and `last_error`
- `POST /jobs/{id}/retry` - Requeue a dead job with fresh attempts; `409` unless it's dead

## Development Workflow

1. **Set up development environment**: `nix develop` (or use existing Nix shell)
2. **Generate keys for JWT**: `just generate_keypair` (first time only)
3. **Create migrations** if changing database schema
4. **Run migrations**: `go run cmd/migrate/main.go -command=up` or use auto-migration
5. **Modify SQL queries** in `queries/`
6. **Run `just chores`** to regenerate sqlc code and update dependencies
7. **Update routes/handlers** to use new database operations
8. **Test with `just serve`** (auto-runs migrations)
9. **Run tests**: `just test` or `just full_test`
10. **Format code**: `just fmt`

## Database Switching

**SQLite (Default)**:
- File-based: `./data.db`
- No external dependencies
- Good for development/testing

**PostgreSQL**:
- Production-ready
- Better concurrency and features
- Use Docker Compose for local development

Switch by updating `config.yaml` or setting `TAIGA_DB_DRIVER` environment variable.

## Special Features

### JWT Authentication
- RSA key-based JWT tokens for stateless authentication
- Middleware protection for routes requiring authentication
- Keys stored in `cert/` directory (generated with `just generate_keypair`)

### Content-Addressable File Storage
- Files stored by SHA-256 hash in `local/filerepo/`
- Configurable directory prefix length for performance
- Deduplication through content addressing

### FIT File Processing
- Garmin FIT workout file parsing with `muktihari/fit` library
- Workout data extraction and segment analysis
- Utility tool (`cmd/fit_view`) for inspecting FIT files

### Dual Database Support
- Seamless switching between SQLite (development) and PostgreSQL (production)
- Separate migration paths for each database type
- Configuration-driven database selection

### Nix Integration
- Full Nix flake for reproducible development environment
- All dependencies declared in `flake.nix`
- Supported systems: `x86_64-linux`, `aarch64-linux`, `aarch64-darwin`, `x86_64-darwin`
- Flake `checks`: `formatting` (treefmt), `golangci-lint`, `go-test` (the `taiga` package itself, whose build runs `go test`) and `docker-image` — all run by `nix flake check`
- `gomod2nix.toml`'s `cachePackages` (written by `gomod2nix generate --with-deps`, which `just chores` runs) lists the dependencies to precompile into a Go build cache, `.#go-cache`. It's built without the ldflags (which stamp the commit), so its store path only changes with the dependencies, and CI keeps it between runs with `actions/cache`. The package and lint builds restore it; the package's checkPhase is plain `go test ./...` (tests in parallel, keeping `-trimpath` so they hit the cache), and its source unpacks to `/build/source` like the cache's, since cgo packages (go-sqlite3) miss from any other directory. Test helpers find the repo with `testsupport.RepoRoot` (walking up to `go.mod`), as `runtime.Caller` gives no file paths under `-trimpath`
- Consistent formatting and building across machines

### CI / GitHub Actions
- `ci.yml` runs `nix flake check` (which builds the binary and Docker image too) on every push, and
  publishes the image to `ghcr.io/<owner>/taiga` from `master` and tags only (the homelab deploys `master-<sha>`). Tag fan-out is
  delegated to `hanko version docker tags --latest-on-release-tag`:
  - Tagged release (`vX.Y.Z`) → `:X.Y.Z`, `:X.Y`, `:X`, `:latest` (`--branch-sha-tag=false`, since the tag checkout is detached and would be named `:detached-<sha>`; the master push of the same commit publishes `:master-<sha>`)
  - `master` between releases → `:master-<sha>` only: CI passes `--semver-tags=false` there, since hanko versions an untagged master build as the next release and would otherwise move `:X.Y.Z`, `:X.Y`, `:X` and `:latest` onto it
  - Feature branches → nothing published

  Auth uses the workflow's `GITHUB_TOKEN` with `packages: write`. Images are
  pushed directly from the Nix-built `docker-archive` via `skopeo copy` — no
  Docker daemon needed in CI.
- There's no Nix binary cache step: uploading every build cost about 3½ minutes a run, more than it saved. Only `.#go-cache` is kept, through `actions/cache` (`nix copy` to and from a `file://` store), and saved only when the restore missed.
- `ci.yml` skips pushes that only touch Markdown, `docs/` or `LICENSE*` (tag pushes always run); `[skip ci]` in the head commit skips any push. `gh workflow run ci --ref <branch>` runs it by hand (an empty commit doesn't: it touches no paths, so the docs-only skip drops it).
- `commits.yml` checks PR commit subjects and titles are conventional commits.
- `seal.yml` is a manually-triggered hanko release workflow.

### Docker Deployment
- Layered Docker image built with Nix for minimal size and reproducibility
- Image includes compiled Go binary, migration files, and minimal runtime dependencies
- Tagged as `taiga:latest` by default
- Supports volume mounting for configuration, certificates, database, and file repository
- The image runs `/bin/server` by default, with migrations at `/migrations` (postgres) and `/migrations-sqlite`
- Example run command with volume mounts: `docker run -v ./config.yaml:/workspace/config.yaml -v ./cert:/cert -v ./data.db:/workspace/data.db -v ./filerepo:/filerepo taiga`

## Activity Files API

The application provides a comprehensive API for managing activity files (primarily Garmin FIT files) with secure upload, download, and metadata management.

### Key Features
- **Secure file upload** with 32MB size limit and multipart form support
- **Content-addressable storage** using SHA-256 hashes for deduplication
- **Metadata management** with RFC3339 timestamps and user ownership
- **User isolation** - users can only access their own files

### File Processing
- FIT files are processed using the `muktihari/fit` library for workout data extraction
- TCX (`workouts.ReadTCXFile`), as Google Fit's Takeout writes it: one file per session, its activities the stretches Google Fit detected, each restarting its distance, laid end to end. The author (`Google Fit`) is the `source`. Its per-point distances are stale or flat and it pads sessions out to the hour, so a session with enough GPS keeps only its fixes, measured along the track, and its time is their span; the stated distance stays the total. A run or walk that mixes sports, has time running backwards, has under 50 GPS fixes a mile, or whose track strays over 10% from the stated distance gets an `excluded_reason`
- Endomondo export JSON (`workouts.ReadEndomondoFile`, one file per workout; upload the JSON, not the export's `.tcx` twins, since TCX calls walks and treadmill runs `Other`) maps Endomondo's sport names onto FIT's. Its stated totals are the summary. A run or walk whose data can't be trusted (hand-entered, no distance, time running backwards, points that disagree with the totals) keeps those totals but no points, and gets an `excluded_reason`
- `POST /workouts/from-activity-file/{id}` parses the file once, storing the workout summary and its fastest segments (`workout_segments` table) for each standard distance in `workouts.StandardDistances`, plus the farthest stretch for each duration in `workouts.StandardDurations` (`kind = duration`, same table), in one transaction
- Segments are measured between recorded points, so a segment can overshoot its distance (or duration) by up to one record interval
- Download returns the stored bytes untouched
- File content is immutable once stored, ensuring data integrity

### Reprocessing and the job queue
- The FIT file is the source of truth; a workout's summary, segments and series (`workout_series`, one JSON document per workout) are derived from it and stamped with `workouts.ProcessorVersion`
- The series keeps every Nth record (at most `MaxSeriesPoints`, rounded per column) and is the only copy of per-record data in the database; segments and totals use every record from the file. ROADMAP M6 lists better sampling methods and when each would be needed
- `ProcessorVersion` combines `extractorVersion` (bump it in `internal/workouts/processor.go` whenever extraction changes) with a hash of `StandardDistances` and `StandardDurations`, so adding or removing one reprocesses old workouts on its own
- Each workout keeps its `source`, what recorded it: the FIT `file_id` product name (`COROS PACE 3`), else the manufacturer plus Garmin product (`Garmin fr965`), `Endomondo`, or a TCX file's author (`Google Fit`); empty for hand-entered workouts
- Each workout keeps the FIT session's `sport` and `sub_sport` (e.g. `running` / `indoor_running`). Only outdoor runs and walks (`workouts.ScoredSports`, less the indoor sub-sports `treadmill`, `indoor_running` and `indoor_walking`; `RunSummary.Scored`) get segments and tracks, and each sport is ranked and grouped on its own; rides, generic sessions and treadmill runs are stored and listed but never scored. A workout with an `excluded_reason` (set by processing, or by the user as a correction) never counts either: it stays out of best efforts, routes and any stats. An effort faster than its sport's cap (`maxSpeedMps` beside `ScoredSports`: about the 1k world record running, the mile race-walking record walking) isn't stored as a segment either
- Each workout keeps where and in what zone it started, and a name built from them. Processing looks its first GPS fix up in `internal/places` for `place` (the town, empty when none is in reach) and `timezone`, and `utc_offset_seconds` is that zone's offset at the start, or for a FIT file with no fix the one its activity message states (`local_timestamp`); a file dropped for bad points is located before they go. `name` (`RunSummary.Name`) is the place, or the local weekday without one, then the part of the day by the local hour (`partsOfDay`) and the sport's noun as corrected (`sportNouns`, `subSportNouns`): "Madison afternoon run", "Saturday morning treadmill run". With no offset at all it's the noun alone ("Treadmill run"), which is also how a hand-entered workout is named. Changing the rule needs an `extractorVersion` bump
- Corrections (`sport_override`, `user_excluded_reason`, set by `PUT /workouts/{id}/corrections`) are applied over the file on every reprocess (`models.Corrections`), so `sport` and `excluded_reason` always hold the effective values and queries never look at the override columns. A corrected sport keeps the file's `sub_sport`; processing's own `excluded_reason` wins over the user's
- Processing also stores the run's GPS track resampled every 20m by distance (`workout_tracks`, `workouts.BuildTrack`); a run without GPS gets none
- Each record keeps its cadence where the FIT file has one, and the series has it as a column: steps a minute counting both feet for a run or walk, going by the sport as corrected, and revolutions otherwise. A run's series also carries `walking` (`workouts.DetectWalking`): its warm-up before the first 30 seconds of steady running, its cool-down after the last, and the walking breaks between (10 seconds or more, ones under 5 seconds apart joined). Walking is told by cadence (under 125 steps a minute), or without cadence by the run's own speeds when they have two clear modes; a run with neither has no `walking`, which means unknown, not none
- Stretches (`stretches`, `stretch_efforts`) are named pieces of ground cut from one workout's series, in one direction, with a point every 10m (`workouts.BuildStretch`). A workout of the same sport passes one when it comes within 30m of its first point, follows it forward never more than 30m off, and reaches its last, covering 80–125% of its length (`Stretch.Passes`); each pass is an effort, timed between its closest approaches to the two ends, so a workout can have several. Efforts are derived: processing a workout replaces its efforts over every stretch, and making a stretch times the user's tracked workouts over it in the request, using the stored tracks to skip most without reading their series. Only workouts that count and have a stored track get efforts, whichever way they're matched. A stretch that starts at a junction only matches runs arriving the way its source did, so start one a little past it
- Routes (`routes`, `workout_routes`) group a user's runs on the same route, never across users. Two runs match when starts and ends are within 150m, lengths within 10%, and the discrete Fréchet distance between tracks is under 75m (`workouts.SameRoute`), skipping up to 150m at either end of either track so a watch started a little early or late still matches. Fréchet follows the points in order, so a loop run the other way round is a separate route
- Routes are derived across workouts, so they aren't part of per-workout reprocessing: a new, reprocessed or deleted workout queues one `detect_routes` job per user, which regroups every tracked run of each sport from scratch (`workouts.GroupRoutes`: oldest first against each route's first run, then merging routes whose representatives match) and keeps the id and name of the old route each group overlaps most. A run matching nothing isn't a route until a second run matches it
- The user's say over routes (`route_overrides`, one row per workout) is applied by that same regrouping, so it survives it: a run moved onto a route (`route_id`), kept off every route (null `route_id`), or carrying a merge (`with_group`: the whole group detection puts it in goes to `route_id`, and every run of that group is given the row, so the merge outlives the runs it was made with). Groups are still formed from every tracked run, so moving one away leaves the rest as it was; the representative is never a run that was moved in. A route that is named, merged into or moved onto is kept down to one run, and deleted only when it has none. Changing an override regroups in the request itself, not through the queue; every regrouping, the job's included, first takes the user's row lock (`routesTx`), so two never rebuild the memberships at once
- The same `detect_routes` job first finds cross-source duplicates (`DetectDuplicates`, `workouts.FindDuplicates`): two workouts from files with different `source`s, any sport, sharing at least half the shorter one's span are one workout recorded twice. The richer source is kept (a watch's FIT, then Endomondo, then Google Fit; between equals the first uploaded), and the other gets `duplicate_of`, the kept one's id. Reprocessing turns that into `excluded_reason` "duplicate of the COROS PACE 3 recording", which outranks any other reason. A changed mark queues the workout for reprocessing; `duplicate_of` has no foreign key, so a deleted original leaves a dangling id that the next detection clears, and the copy counts again. Copies from one source are left alone, since a recorder can't record a workout twice
- Weather (`workout_weather`, one row per workout and source, with what was asked and when) is not derived from the file, so processing never touches it: `fetch_weather` jobs fill it from `weather.Provider` when `weather.provider` is set. Each hour the worker queues one for every tracked workout that a source in `weather.Sources` should have by now (`MinAge` after it ended) and doesn't, 100 per source, which is the first fetch, the later ERA5 one and the backfill alike; a job can't queue its own second pass, since `idx_jobs_pending` allows one pending job per kind and workout. The job asks with the track's first point rounded to 0.1° and keeps start and end conditions; a source with nothing yet (`weather.ErrNotYet`) leaves no row and no failure, and a workout whose last fetch died is skipped until the job is retried. A reprocessed workout queues a fetch too, which only asks again if its span moved; a rebuild that changes the span drops the readings (on every path, the synchronous ones included) so the sweep asks again. A workout that later loses its track keeps the weather it has
- `internal/jobs` runs a worker goroutine in the server: on boot it queues a `process_workout` job for every workout whose version isn't current, then polls the `jobs` table
- The claim query runs unchanged on both databases (no `SKIP LOCKED`); a claimed job holds a 5-minute lease, so a crashed worker's job is picked up again
- Failures retry with exponential backoff from 30s, up to 5 attempts; errors that can't succeed on retry (unreadable file, missing workout) go straight to `dead`
- Store writes that must land together go through `store.InTx`; SQLite opens transactions `IMMEDIATE` so concurrent writers wait on `busy_timeout` instead of failing

### Testing
- `internal/testsupport` provides real dependencies for integration tests: a migrated file-backed SQLite model, a local file repo, a throwaway RSA toker, and `EncodeFIT` / `EncodeMultiSessionFIT` / `NewEndomondo` / `NewTCX` for synthetic activity files
