# taiga roadmap

The goal is seeing and comparing runs in a browser.
Pieces: this API, the browser frontend (`taiga-frontend`), and the `taco` CLI (`taiga-cliclient`) for scripting.
Milestones are roughly ordered; each should fit in one PR per repo. M5 comes before M6/M7 because both need old workouts re-read from their files.

**Non-goals:** sharing and anything cross-user (friends, feeds, public profiles, leaderboards between users).
Each user's data is private to them, and every query stays scoped by `user_id`.
Multiple accounts exist only so more than one person can use the same deployment.

---

## M1 — Sign in with an OIDC provider (alongside passwords)

**Goal:** a user can sign in through any OpenID Connect provider (authentik is the first target, but nothing may be authentik-specific), and password login keeps working.

### Design

- **Generic OIDC, configured by issuer URL.**
  Endpoints come from the provider's discovery document (`/.well-known/openid-configuration`), so authentik, Keycloak, Google etc. are config, not code.
  Config holds a list of providers:
  ```yaml
  auth:
    password_login: true
    public_base_url: "https://taiga.example.com"   # what the browser sees; callbacks are built from it
    oidc:
      - name: authentik
        display_name: "authentik"
        issuer: "https://auth.example.com/application/o/taiga/"
        client_id: "..."
        client_secret_file: "/run/secrets/taiga-oidc-authentik"
        scopes: [openid, email, profile]
  ```
  Libraries: `github.com/coreos/go-oidc/v3` (discovery, ID-token verification against the JWKS) and `golang.org/x/oauth2` (authorization code + PKCE).
- **The backend runs the flow; the rest of the API doesn't change.**
  - `GET /auth/providers` lists the configured providers (name, display name) so the login page can render buttons, and says whether password login is enabled.
  - `GET /auth/oidc/{provider}/login` redirects to the provider with `state`, `nonce` and a PKCE challenge.
    Those live in a short-lived, HttpOnly, signed cookie scoped to the callback path, so the server stays stateless.
  - `GET /auth/oidc/{provider}/callback` checks `state`, exchanges the code, verifies the ID token (issuer, audience, expiry, `nonce`), resolves the user, and mints the **same taiga JWT** `/auth/tokens` issues today.
    It then redirects to the frontend with the token in the URL fragment (`/#/login/callback?token=…`), which browsers don't send to servers or write to access logs.
    The frontend stores it exactly as it stores a password-login token.
  - Behind the frontend's nginx `/api` proxy the redirect URI is `{public_base_url}/api/auth/oidc/{provider}/callback`, and that's what gets registered with the provider.
- **Identity is (issuer, subject), never email.**
  New table `user_identities (id, user_id → users, issuer, subject, email_at_link, created_at, UNIQUE (issuer, subject))`.
  Callback lookup is by `(issuer, sub)` only.
  `users.password` is already nullable, so an OIDC-only user has no password row.
- **Account creation and linking.**
  - Unknown identity → create a user (display name from `name`/`preferred_username`, email from `email`) and link it.
    A per-provider `allow_signup: false` turns this off for invite-only setups.
  - Linking to an existing password account is explicit: from a logged-in session, `POST /auth/identities/{provider}` starts the same flow and attaches the identity to the current user.
  - Auto-linking by matching email is **off by default**.
    A per-provider `link_by_verified_email: true` enables it, and only when the ID token has `email_verified: true` — otherwise anyone who can set an email at some provider could take over an account.
    The account's own email must be verified too (`users.email_verified`, migration 000013: set by `useradd` or a provider that verified it, not by open registration), so nobody can pre-create an account with someone else's email and inherit their sign-in; and an email matching two accounts in different cases links neither.
  - `GET /auth/identities` and `DELETE /auth/identities/{id}` to see and unlink, refusing to remove a user's last way to sign in.
- **Password login stays.**
  `auth.password_login: false` disables `/auth/register` and `/auth/tokens` for SSO-only deployments.
- **Out of scope for M1:** single logout at the provider, refresh tokens (the taiga JWT still lasts 2h; with an active provider session re-auth is a redirect and back), and SCIM or group-based roles.

### Tasks

- [x] Config: `auth.*` section, provider list, secrets read from files; refuse to start if a provider's discovery fails at boot (log which).
  - From env alone, `TAIGA_AUTH_OIDC` holds the list as JSON.
- [x] Migration `user_identities` (000012) (postgres + sqlite) and sqlc queries.
- [x] `internal/oidc`: provider registry from config, login redirect, callback handling, state/nonce/PKCE cookie.
  - The cookie's HMAC key is derived from the token signing key, so a flow survives a restart and works across replicas.
- [x] Routes: `/auth/providers`, `/auth/oidc/{provider}/{login,callback}`, `/auth/identities` (list, link, unlink).
  - Linking is `POST /auth/identities/{provider}` returning `{url}` rather than a redirect, since a fetch carrying the bearer token can't navigate; the flow cookie remembers whom to link to.
- [x] Tests against an in-process fake provider (`testsupport.FakeOIDC`, written for the job instead of mockoidc so each test can pick the audience, expiry and nonce): happy path, bad `state`, no or replayed flow cookie, bad `nonce`, wrong audience, expired token, the provider's own error, signup disabled, email auto-link on and off, `email_verified: false`, a new account without an email, linking and unlinking, password login off.
- [x] Frontend (taiga-frontend#17): provider buttons on the login page from `/auth/providers`, a `#/login/callback` route that stores the token and takes it out of the URL, and an account page (`#/account`) to link and unlink providers.
- [x] Docs: an authentik walkthrough plus the generic settings any provider needs (`docs/oidc.md`).

**Exit criteria:** with only an authentik provider configured, a new user can sign in from the frontend and use every page; an existing password user can link authentik and then sign in either way; swapping in a second provider (e.g. Keycloak) needs config only.

---

## M2 — Upload hygiene

- [x] Unparsable FIT files and multi-session files (e.g. triathlons) return **422** with a reason, not 500.
- [x] Don't leave orphan `activity_file` rows when parsing fails (or expose them so they can be retried or deleted). Exposed via `GET /activity_files?without_workout=true`; kept rather than deleted so multi-sport files can be processed once supported.
- [x] Deduplicate uploads: the same file (same content hash, already computed) for the same user returns the existing workout instead of creating a second one.
- [x] Backfill segments for workouts created before `workout_segments` existed. (First as `POST /segments/recompute`; M5 replaced it with reprocessing, which backfills on its own.)
- [ ] Frontend and `taco`: say "already uploaded" on a `200` re-upload, offer a backfill action, and list files that failed to parse.

## M3 — Deploy

- [ ] Run backend + frontend images together. Test stack at `taiga.i.hruday.me` on the homelab k8s cluster (CNPG Postgres, local files PVC); manifests and runbook live in proxmox-homelab (`k8s-manifests/taiga`, `docs/taiga.md`), not here.
- [x] Env-only configuration: every config key binds to its `TAIGA_*` variable, and the image starts the server by default.
- [x] A production CORS story: none needed, because the frontend's `/api` proxy is the only browser entry point and `TAIGA_APP_ENVIRONMENT=production` keeps the dev-only wildcard CORS off.

### Before any public access

The test stack is internal-only (LAN and tailnet). These must land before taiga is reachable from the internet.

- [x] **Closable registration.** Before this, `POST /auth/register` was always open, so anyone who could reach the API could make an account.
  - `auth.registration: open | closed` (env `TAIGA_AUTH_REGISTRATION`); closed returns 403 from `/auth/register`.
  - Default closed outside development, so a prod deploy is closed unless it opts in.
  - `cmd/useradd` to create accounts while registration is closed (run via `kubectl exec`).
  - `GET /auth/providers` reports `registration_open` (the endpoint M1 extends with OIDC providers); the frontend hides its register link from it, but the 403 is the real control.
  - Separate from M1's `auth.password_login: false`, which disables password login entirely. With OIDC, `closed` plus a provider's `allow_signup: false` gives invite-only through authentik.

## M4 — Cleanup

- [ ] Remove the `hats` template code and its table.
- [ ] Retire or refresh `terraform/` (AWS ECS + RDS): the homelab stack replaces it as the deploy path, and it predates env-only config.
- [x] Fix CLAUDE.md drift (`/auth/tokens`, config keys, migration directories, package list) and replace the template README.
- [ ] Rename the `trygo-template` service name that `GET /` and `GET /health` still report.
- [ ] Tests for `internal/tokens`, `internal/store`, `internal/filerepo`.

## M5 — Reprocessing and a work queue

The FIT file is the source of truth; everything taiga computes from it (summary, segments, and later series and heart rate) is derived data that should be rebuildable whenever the app learns something new.

- [x] **Versioned processing.** Each workout carries the `processor_version` that built it: `extractorVersion` (bumped by hand when extraction changes) plus a hash of `StandardDistances`, so editing the distance list makes every workout stale without anyone remembering to bump.
- [x] **Reprocess from the file.** `ReprocessWorkout` rebuilds the summary and replaces the whole segment set in one transaction (via the new `store.InTx`), so added distances get filled in and removed ones disappear. It replaced `POST /segments/recompute`.
- [x] **Hand edits.** A workout from a file is derived data only: `PUT` returns 422 for it. If hand corrections turn out to be wanted, they go in separate override columns that reprocessing never touches.
- [x] **A job queue in the database**, not new infrastructure: a `jobs` table and a worker goroutine in the server (`internal/jobs`).
  - The claim is one `UPDATE … WHERE id = (SELECT … LIMIT 1) AND state = 'queued' RETURNING *` that runs on both Postgres and SQLite; a lost race claims nothing. `SKIP LOCKED` buys nothing with one replica.
  - Leases (5 min) so a crashed worker's job is claimed again; exponential backoff from 30s; dead after 5 attempts, or at once for errors a retry can't fix.
  - On boot the worker queues `process_workout` for every stale workout. `POST /workouts/reprocess` queues all of a user's; `GET /jobs`, `POST /jobs/{id}/retry` to watch and retry.
- [x] Standard distances gained `3k` and `2mi`; existing workouts pick them up through the above.
- [ ] Client side: a reprocess action and dead jobs, as part of the admin panel (see Later).

**Deferred:**
- **Async uploads.** Parsing takes milliseconds, so `202` plus polling in both clients isn't worth it yet. Uploads that genuinely run in the background arrive with M8's `sync_source` jobs.
- **`update_stats` jobs.** Nothing is aggregated across workouts yet. Add the job kind with the first stat that needs it (M6 trends, M7 zones).

## M6 — Visualisations

- [x] **Per-record series.** Built at processing time and stored as one JSON document per workout (`workout_series`), rebuilt by M5 reprocessing (`extractorVersion` 2 backfills old workouts).
  Columns: time, distance, speed, altitude, heart rate, lat/long, sampled evenly to at most 2000 points; a column the device never recorded is omitted.
  Served gzipped from `GET /workouts/{id}/series`, so pages never re-parse the FIT file.
- [x] **Mile splits** on the workout page: time, pace, elevation change and average HR per mile (or km), computed in the frontend from the series by interpolating where each mile falls, so nothing new is stored.
- [x] **Graphs** in the frontend: pace, elevation and heart rate overlaid on one graph (an axis each, toggles) over distance or time; a segment or split highlighted on it; a distance's best-effort trend over time.
- [x] **Maps.** The GPS track from the series drawn with Leaflet on OpenStreetMap tiles, with segments highlighted. Tiles come from openstreetmap.org, so the map's area is visible to the tile server.
- [x] Compare view: overlay workouts' pace graphs aligned by distance.
- [x] Workout page laid out as a desktop dashboard: map top left, splits top right, the graph along the bottom, sized to the screen.
- [x] **Best efforts by time**: the farthest stretch in 30 minutes and an hour, stored as `kind = duration` segments beside the distance ones and ranked by distance; they show on the workout page, best efforts and compare. More durations (20 minutes, 2 hours) are one line in `StandardDurations`, and reprocessing backfills them.
- [x] **Quieter map** (feedback 2026-11-11). The map took too much of the dashboard and the OSM style competed with the track.
  - The map gets less of the grid (3:2 columns, half the height), so splits and graphs get more room.
  - The OSM tiles are greyed by a CSS filter (inverted in dark mode), so road colours drop out. CARTO Positron was the first choice, but its tiles now need an API key.
  - The track is drawn at full opacity over a dark casing, with larger start/finish markers.
  - Still open: a self-hosted vector style (PMTiles + MapLibre) would give full control of the basemap, and would also keep the map's area away from a tile server.
- [x] **Grafana-style graph legend.** The colour swatches and checkbox toggles above the graph are gone; the legend under it lists each series with its colour and value at the cursor, and clicking an entry shows or hides that series.
- [x] **Years on time axes** (feedback 2026-11-12). The date axes on the best-effort pace trend and a route's runs labelled ticks with month and day only, so the year showed only in the hover readout. Now the first tick and each new year's first carry the year on a second line (`timeAxis` in the frontend's `chart.ts`); month steps drop the day, and year steps show just the year.
- [x] **Best efforts on the map and graph** (taiga-frontend#18). Each best effort is a labelled bar under the workout graph, lined up with its x axis and packed into rows; the bars under the cursor light up, naming what a point is part of. Hovering a bar or a row previews its stretch on the graph and map without zooming, and clicking selects it. Hovering the map track names the efforts at that point. One colour for every bar rather than one each: eleven overlapping efforts are more than any colour set keeps apart, so the labels carry identity. The bars can be hidden, and start hidden on a phone.
- [x] **Pace or speed** (taiga-frontend#18). A min/mi or mph switch, in the nav and the workout graph's toolbar, wherever pace shows: the graph, splits, best efforts, routes and compare. It's flipped while reading a graph, so it updates the page in place and keeps selections and hidden lines. Speed on the workout graph is smoothed over the last 30 seconds rather than inverted from pace (smoothed over 200m, stops left as gaps), so a stop drops to zero and the area under the line over time is the distance.
- [x] **Colour the map track by pace or heart rate** (taiga-frontend#18). Plain, pace (or speed) or HR, picked on the map: seven bins between the 5th and 95th percentiles, one blue ramp for pace and an orange one for HR, reversed in dark mode, with a legend.

### Series sampling: today, and upgrades for when they're needed

**Today.**
- `BuildSeries` keeps every Nth record (an even stride) so a workout has at most 2000 points, always keeping the last; nothing is averaged. At 1 record/s a one-hour run keeps every 2nd record, a marathon about every 7th.
- Values are rounded per column (time 0.1s, speed 0.001 m/s, altitude 0.1m, HR whole bpm, lat/long 6 decimals ≈ 10cm).
- Segments and workout totals use every record in the file; only graphs, the map and splits see the thinned series.
- The thinned series is stored (`workout_series`); full-resolution records are not, and the FIT file is reread only on reprocessing.
- The frontend plots the series as is: pace smoothed over a trailing 200m (gaps slower than 30 min/mi), splits interpolated at exact mile/km marks (good to about one stored point, a few seconds on a long run), elevation gain with 1m hysteresis, compare overlays resampled onto a shared distance grid.

**Upgrades, each with what would call for it.** Bumping `extractorVersion` rebuilds every workout from its file, so changing the method is cheap.
- [ ] **Peak-preserving downsampling** (LTTB, or min/max per bucket) instead of a plain stride, which can drop a short HR spike or sprint and makes the shape depend on where the stride falls. Needed for interval sessions, M7 HR zones and max HR read from the series, or zooming into a short stretch of a long run.
- [ ] **Shape-aware track simplification** (Douglas–Peucker / Visvalingam) for lat/long, keeping corners sharp with fewer points. M9 route matching resamples evenly by distance anyway, so this is only for drawing.
- [ ] **Detail on zoom:** keep a full-resolution (or second, denser) series and fetch a time window of it when the graph is zoomed. Only once second-by-second analysis is wanted; it costs storage roughly in proportion to recording rate.
- [ ] **Splits on the backend** from every record, stored like segments. Needed if splits appear where they must be exact (records pages, comparisons across runs, trends) or very long runs make the stride coarse.
- [ ] **Better elevation:** smoothing, or correction against a terrain model. This moves gain/loss more than sampling does; worth it when elevation numbers start to matter.

## M7 — Heart rate (lower priority)

- [ ] Average and max HR per workout from the FIT session (`avg_heart_rate`, `max_heart_rate`); HR is already in the M6 series. Old workouts pick these up through M5 reprocessing.
- [ ] A per-user max HR (and optionally resting HR) setting, defaulting to the highest HR seen across the user's workouts.
- [ ] Time in HR zones per workout, derived from that setting. Changing the setting enqueues stats jobs rather than reprocessing files.

## M8 — Automatic import (exploration)

Today every run is a manual FIT export from COROS, then `taco upload` or the browser.
The goal is new runs showing up on their own. Each option still needs checking against current docs and terms before building.

- [ ] **Survey the routes**, cheapest first:
  - **Via intervals.icu.** COROS already auto-syncs to intervals.icu, and its API returns the original uploaded file (`GET /api/v1/activity/{id}/file`, gzipped FIT) with a personal API key. Works for a single user today, at the cost of a third party in the path.
  - **COROS self-service developer access.** Reportedly OAuth 2.0 for personal apps with no approval step. Unconfirmed: whether it can download FIT files, and what the limits are.
  - **COROS Partner API.** Webhook push and FIT downloads, but it needs an application to api@coros.com and is aimed at established platforms. Probably not worth it for personal use.
  - **No API:** a bulk import of a COROS data export (t.coros.com), and possibly a watched folder that a phone or desktop sync drops files into.
- [ ] **A source abstraction.** Each user connection (kind, credentials, cursor) gets a periodic `sync_source` job on the M5 queue that fetches new activities and feeds them through the normal upload path.
  Content-hash dedupe already makes re-fetching harmless. Also store the source's activity ID, to skip downloads that would only dedupe.
- [ ] Credentials stored encrypted at rest, revocable per connection, and never logged.
- [x] **Endomondo archive import.** Endomondo shut down in 2020; its export has a `.tcx` and a `.json` per workout. Surveyed on a 405-workout export (2016–2020):
  - **Sport needs the JSON.** TCX says `Running` for 202 runs, `Biking` for 1 ride and `Other` for everything else, walks (83) and treadmill runs (23) included. The JSON's `sport` (`RUNNING`, `WALKING`, `TREADMILL_RUNNING`, `CYCLING_SPORT`, `SPINNING`, `STAIR_CLIMBING`, `WEIGHT_TRAINING`, …) maps onto FIT names, e.g. `TREADMILL_RUNNING` → running / treadmill.
  - **Points are in both.** Time, position, cumulative distance and altitude every 1–5s, and heart rate (`heart_rate_bpm`) where a strap was worn. TCX times are ISO UTC; JSON point timestamps switch between `"Thu Jul 23 22:20:14 UTC 2020"` and `"2020-12-07 20:44:46.0"`.
  - **GPS:** 280 of 285 runs and walks have a track. Treadmill, spinning, stairs and weights have 1–5 placeholder points, and 14 are hand-entered (`source: INPUT_MANUAL*`): no points, just a distance and duration.
  - **Broken files exist:** one run has 16 points over 120m against a stated 4.02 km (GPS lost, distance typed in), and its points jump back an hour after the first. Several 2016 runs' points carry on minutes past `end_time`, which is the start plus moving time, so that's a pause, not a fault.
  - 52 workouts carry a note (`message`); photo URLs point at Endomondo's dead servers.
  - **Built:** upload the `.json` files (`taco upload Workouts/*.json`, or the browser). `workouts.ReadActivityFile` tells FIT from Endomondo JSON by content and reads either into the same `workouts.Run`, so storage, reprocessing, segments and routes work unchanged. Untrustworthy files lean towards not counting: hand-entered, no distance, time running backwards, or points that disagree with the totals keep their stated totals but no points, and an `excluded_reason`. On the export that leaves 198 of 202 runs and 80 of 83 walks counted.
  - [ ] Notes (`message`) aren't kept; workouts have nowhere to put them yet.
  - [ ] Content dedupe won't catch a run that's also in a FIT export; match on start time and sport. Not needed for this archive, which ends before the COROS history starts.
- [x] **Each workout's source** (`source`): what recorded it, from the FIT `file_id` (its product name, e.g. `COROS PACE 3`, else the manufacturer and Garmin product), or `Endomondo`. Reprocessing fills it in for old workouts.
- [x] **Google Fit Takeout import.** Surveyed on a 3,427-session export (`Fit/Activities`, 2016–2026, TCX only):
  - **No app name.** Every file's author is `Google Fit`; Takeout's "All sessions" JSON would name the recording app, but isn't needed to find duplicates.
  - **One file per session,** made of the stretches Google Fit detected (`Walking`, `Running`, `Biking`, ...), each its own `<Activity>` restarting its distance. 367 files mix walking and running, often with a stray point hours before the start and time running backwards.
  - **Mostly noise.** 2,380 walks and 264 rides are step- or motion-detected (2021–23 walks average 0.7 mi in 18 min, most with under 20 GPS fixes a mile). Tracked runs have 200+ fixes a mile.
  - **Duplicates:** 271 sessions copy Endomondo workouts (117 of them over 10% short) and 80 copy COROS ones; 289 of the 351 start within 2 minutes of the original.
  - **Per-point distance is unusable:** it updates every half minute or so, or sits at the session total from the second point. Sessions are padded with fixless points out to the hour (a 25-minute run reads as 1:00:00). Positions are fine, so points are measured along the GPS track and the time is the fixes' span.
  - **Track vs. total:** on 109 of 122 runs the track is within 10% of the stated distance. 2026 phone runs jump 100–260m between fixes and overshoot by 20–58%; one 2023 run has a clean track 17% over its total. Disagreement excludes the run rather than guessing which is right.
  - [ ] **A short track can mean an inflated total.** A track only cuts corners, so with dense fixes it can under-measure but not over-measure. The 2023-04-25 run's phone took fixes every 5–35s for its first 3 minutes: 600m of track against Google Fit's 997m (a 4:40/mi pace), then both agree at 9:00/mi. Its total reads 2.96 mi at 8:10/mi, really about 2.7 mi, and it tops the 1-mile board; the track is only 7.6% short, so the 10% check passes it. Idea: when the track is dense and shorter than the total, use the track's distance. Check first how many runs start with sparse fixes.
  - **Built:** `workouts.ReadTCXFile`. A scored session that mixes sports, has time running backwards, has under 50 fixes a mile, or whose track strays from its total gets an `excluded_reason`. First pass uploaded only the 122 tracked runs that overlap nothing already stored (113 from 2021–2023, between Endomondo and COROS), so no duplicates went in; 109 count.
  - [x] **Cross-source duplicates:** the `detect_routes` job first marks workouts from different sources that share at least half the shorter one's span. The richer source wins (watch FIT, then Endomondo, then Google Fit); the other copy stays listed with `duplicate_of` and an `excluded_reason` naming the winner, so deleting the winner brings it back.
    - Neither "same sport" nor "starts minutes apart" survived the data. Google Fit's detected sport often disagrees (`Walking,Running` against an Endomondo run, `Walking` against a treadmill session), and its start can be 20–30 minutes off on a real copy. On the 466 Google Fit sessions overlapping a stored workout, 367 share over 90% of the shorter span; under 50% are nearly all step-detected walks either side of a run.
    - Against the 815 stored workouts plus the whole export, it marks none of the stored ones and 428 not-yet-uploaded Google Fit sessions, 122 of which would otherwise count. The rest of the export isn't worth uploading: what isn't a copy is mostly step-detected walks, and the first pass took the tracked runs.

## M9 — Routes

Most runs repeat a handful of routes. The goal: group runs on the same route automatically, so a route's runs can be compared on total time and its history shown as a trend.
Builds on the M6 series (positions); everything is per user, never matched across users.

- [x] **Detecting a route match** (`workouts.SameRoute`): starts and ends within 150m, lengths within 10%, then a discrete Fréchet distance under 75m between tracks resampled every 20m by distance. Only runs and walks, each sport on its own. Fréchet respects order, so a loop run clockwise and anticlockwise are separate routes; an out-and-back retraces itself and matches either way. Tuned on a two-year export of 255 GPS activities: 50m split routes that GPS drift had nudged apart, 75m puts 107 of 134 runs on 16 routes, and running the same loop the other way round still lands 400m+ away.
  - [x] **Slack at the ends** (feedback 2026-11-14). Fréchet had to pair the two starts, so a loop started 90m further round, or a watch started a few steps from the door, counted that offset as shape: routes 4 and 58 on the test stack were one loop split by 91m at the start, with most of their runs 15–40m apart. The coupling may now start and end anywhere within 150m (`RouteEndpointMeters`) of either track's ends; missing more than that, as when the watch is started a few minutes in, still fails the endpoint check and makes a different route. On the test stack's 458 runs this merges 4 with 58, the two "Short run" routes (2 and 41) and 42, 43 and 45, each confirmed as one route, and pulls a few unmatched runs onto routes.
- [x] **Clusters.** Each run, oldest first, joins the route whose first run it matches most closely, or waits as a candidate until a second run matches it; then routes whose representatives (the member closest to the rest) match are merged, since an off first run can split one route in two.
- [x] **As derived data:** a per-user `detect_routes` job on the M5 queue, after a workout is created, reprocessed or deleted, regroups from scratch. A rebuilt group keeps the id and name of the old route it overlaps most.
- [ ] **A coarse index** (the set of ~150m grid cells a track passes through, compared by overlap) to find candidate routes without comparing against every route's first run. Not needed at hundreds of runs: most pairs fail the endpoint check.
- [ ] **Variations.**
  - Small detours (a block around construction, GPS drift) fall inside the tolerance and still match.
  - Extensions (the usual loop plus an extra mile) cover most of the route but add length: match them as a variant of the route, flagged, and keep them out of the route's total-time comparison. Today they fail the length check and become a route of their own.
  - Truly different runs become their own route. Partial overlap between routes is what stretches (M13) would handle.
- [x] **User control beats detection.** Beside naming (`PUT /routes/{id}`): move a run onto another route or take it off every route (`PUT /workouts/{id}/route/override`), and merge two routes (`POST /routes/{id}/merge`), each undoable. The decisions live in `route_overrides`, one row per workout, and every re-detection applies them over its own grouping.
  - A merge is stored on the runs, not the route: the group detection puts a marked run in goes to the merged route, and every run of that group gets the mark, so a new run on the merged-in route follows it and the merge outlives the runs it was made with.
  - Groups are formed from every run, moved or not, so taking an odd run off a route can't reshape what's left.
  - A route that's named, merged into or moved onto is kept down to one run, so a named route no longer loses its name when it dissolves.
  - "Not this route" is the same as taking the run off every route: it stays off until the override is cleared or it's moved somewhere. Forbidding one route while still matching others would need detection to regroup around the ban; not built, with no case for it yet.
  - [x] Frontend (taiga-frontend#20): "Change" on the workout page's route (automatic, no route, or one of the routes), and "Merge in" and "Undo merges" on the route page, with the runs put there by hand marked.
- [x] Frontend: a routes list, a route page (map of the representative track, its runs and a time trend), and the route shown on each workout.

## M10 — Corrections

The file is the source of truth, but files are sometimes wrong: a ride saved as a walk, a GPS glitch, a run that shouldn't count. Corrections are stored beside the derived data and survive reprocessing.

- [x] **Per-sport plausibility cap.** Efforts faster than a sport could manage (a walking mile at 3:38) aren't stored as segments, so a mislabeled activity or a GPS jump can't top a leaderboard. The caps (`maxSpeedMps`, next to `ScoredSports`) are about the 1k world record for running and the race-walking mile record for walking.
- [x] **Exclude from stats.** A per-workout reason that keeps it listed but out of best efforts, route comparisons and trends (a run with a friend, a watch left running in the car): `user_excluded_reason`, set with `PUT /workouts/{id}/corrections` and applied over the file on every reprocess. Processing's own `excluded_reason`, for untrustworthy Endomondo and Google Fit files, wins over it.
  - First case, to exclude once this is deployed: the 2023-04-25 Google Fit run (test stack workout 739), whose total is inflated by a sparse GPS start (see M8).
- [x] **Edit a workout's sport**: `sport_override`, set the same way, wins over the file's sport, and changing it reprocesses the workout and requeues route detection, so segments and routes follow. The file's sub-sport stays, so a treadmill run corrected to a walk is still indoors.
- [ ] Other fields the file got wrong, as more override columns applied the same way, once there's a case for one.
- [x] Frontend: the sport and exclusion controls on the workout page ("Correct"), with a corrected sport marked and an exclusion shown as "Counted: No".
- [x] **Indoor runs aren't scored.** Treadmill, `indoor_running` and `indoor_walking` sessions measure distance with a belt or foot pod, so they get no segments and no track (`RunSummary.Scored`, #65).
- [x] Route changes (rename, merge, move, detach) are M9's user control.

## M11 — Time windows

Years of history (an Endomondo archive goes back to 2016) make an all-time list long and all-time bests stale.

- [x] **Restrict the workout list by time.** `GET /workouts?from=&to=&limit=&offset=`, and `GET /workouts/totals` for a window's count, distance and time per sport (counted workouts only) with the earliest start, so the frontend can list the years there are. The frontend opens on this year, keeps the period in the URL (`?period=`), and shows its totals above the list; a year narrows to a month.
- [x] **Mileage per week or month.** `GET /workouts/totals/week` and `/month` sum each period of a window per sport, at the viewer's local midnights (`?tz=`), empty periods included so a chart has no gaps. The workout list charts it above the totals: weeks for a year or a month, months for all time.
- [x] **Best efforts per year or window.** The same `from`/`to` on `GET /segments/{name}/best`. The best page picks all time, the last 12 months, the last 90 days or a year, and under all time shows every distance's best in each year from one call, `GET /segments/yearly-bests`.
- [x] **Route pages take the same window**, so a route's best can be this year's: `?from=&to=` on `GET /routes` and `GET /routes/{id}`, and a period picker on both pages.

## M12 — Workout names

A list of timestamps is hard to scan. Each workout gets a human name from when it happened and what it was: "Saturday afternoon run", "Lunchtime walk".

- [ ] **A deterministic name** from the local start time and the sport: the weekday, a part of the day (early morning, morning, lunchtime, afternoon, evening, night, at fixed hour boundaries) and the sport's noun (run, walk, ride, treadmill run). No randomness and nothing generated, so the same workout always reads the same, and the rule fits in a table test.
- [ ] **The name sits beside the timestamp, never in place of it.** Every API response keeps `start_time` as it is, and every frontend view that shows a name can still show the full timestamp; two "Saturday afternoon run"s are only told apart by their dates.
- Open questions:
  - **Whose local time.** Nothing stores a workout's timezone today; the period totals take the viewer's `?tz=`. A name from the viewer's zone calls a morning run abroad an evening one. Options: the FIT activity's `local_timestamp` offset where the file has one, a zone looked up from the first GPS fix, or the viewer's zone as the fallback for files with neither.
  - **Backend or frontend.** Computed in the frontend it needs no storage and follows the viewer's zone; computed at processing time (a `name` column, rebuilt by reprocessing) it's the same in `taco` and the browser and can use the workout's own zone. The second seems right once the zone question is answered.
  - **More than time of day**, later and only while still deterministic: the route's name ("Saturday morning Lake loop"), or a user-set title as an M10 correction that wins over the derived one.

## M13 — Stretches: comparing a piece of a route

M9 compares whole runs on the same route. The next question is about a piece of one: name "Whitney Way hill" once, then see every time over it, whichever run it was part of.
"Segment" already means a best effort at a standard distance, so a named piece of ground is a **stretch** here.
This began as exploration; the three looks at the data below are what the first pass was built from.

- [x] **A stretch is a piece of track**: the points every 10m between two distances along one workout, with a name and a direction (up the hill isn't down it). `POST /stretches` cuts one from a workout's series; rename and delete beside it.
  - A stretch that starts at a junction only matches runs arriving the way its source did: cut from Regent Street's corner, Whitney Way gets 40 of its 78 passes, and all 78 once it starts 50m on. The frontend should say so, or the start should be given some slack along the stretch.
- [x] **Efforts over a stretch** (`stretch_efforts`): every pass of a workout of the same sport, timed between its closest approaches to the two ends, from the series, so a workout's efforts are the same whether it was just processed or matched later. Processing a workout replaces its efforts; making a stretch times the user's tracked workouts in the request, with the stored tracks ruling most out first (78 efforts from 269 workouts in a quarter of a second). `GET /stretches/{id}` lists them with the window the routes take, and `GET /workouts/{id}/stretches` the ones a workout made.
- [x] **Within a route, or across them?** Across: a stretch is defined by its ground and matched against every tracked run of the sport (see the first look below; one stretch drew its 78 passes from six routes and eight runs on no route). A run can pass a stretch more than once (laps, out-and-back the same way), so an effort is a pass, not a workout.
- [x] **Walking breaks.** Cadence is read from the file and kept as a series column, and a run's series carries `walking`: the warm-up before the first steady running, the cool-down after the last, and the breaks between. By cadence where the file has it, else by the run's own two speed modes, else absent (see the third look). An effort carries the share of it walked, null where that can't be told.
  - [ ] Nothing per workout outside the series yet: no break count or run-only pace in the workout list.
- [ ] **Places walked versus run.** Across runs, the same ground walked again and again is a pattern: a hill that's always walked, a warm-up block. Bin the ground into cells along the tracks and compare how often each is walked or run, drawn on a map. Those places are also candidate stretches, found without drawing them; so are the shared pieces of overlapping routes.
- [ ] **Suggested stretches**: the candidates of the second look (shared pieces of routes, climbs) offered for naming. Not built; they repeat each other too much yet.
- [x] **Frontend, first part** (taiga-frontend#21): "Save a stretch" on a workout's graph (a name and a from and to in miles, filled in from the split or best effort selected, shown on the graph and map as they change), a stretches list, and a stretch page with its line on the map, its efforts, and time over the date coloured by walked share.
  - [ ] Still to do: stretches and walking breaks on the workout page itself, cadence as a line on its graph, and picking a range by dragging on the graph, which still zooms.
- [ ] **Sparse GPS** (from review of the first pass). Matching goes point by point, so it needs fixes close together: a straight track matches itself with fixes up to 40m apart and fails at 60m. Two causes, one weakness:
  - a record may only advance 80m along the stretch (the guard against jumping to a later part of a stretch that doubles back), so fixes further apart than that fall behind and drop off it;
  - an end is only reached by a fix near it, so an end crossed between two distant fixes is missed.
  - The watch's runs are 20m apart at worst (a marathon thinned to 2000 points), so nothing recorded so far is affected. It would show across a GPS dropout or on a sparse phone recording. When that's seen: fill in points between distant fixes before matching, as `BuildTrack` already does for routes, with a cap so a long dropout isn't bridged; simpler than scaling the lookahead and testing segments for crossings.
- [ ] Not handled: a stretch's efforts aren't rebuilt if matching changes without `extractorVersion` moving, and on PostgreSQL a workout processed in the same instant a stretch is made can miss it until it's next reprocessed.

### First look (2026-11-20)

On the COROS export: 135 GPS runs and 85 walks, January 2024 to October 2025, one record a second, all with cadence. Throwaway code, not in the repo.

- **Walking shows up cleanly in the watch's files.** In runs, cadence has two modes with a gap between (about 100–120 steps a minute walking, 130–150 running, split at 125), and so does speed over 10 seconds (1.5 and 2.5 m/s, split at 2.0). The two agree on 91.5% of records and give the same median of 9 breaks a run. About a quarter of all run time is walked; taking it out moves a run's pace by 30 seconds to 2 minutes a mile. (This was first read as "speed is enough for files without cadence"; the third look shows it isn't.)
  - A break here is 10 seconds or more of walking, with gaps under 5 seconds joined, and under 0.5 m/s counted as stopped rather than walking.
  - The watch sometimes writes cadence doubled (200+); those readings fall back to speed.
- **Clustering where the breaks are.**
  - DBSCAN on break positions (40m, 6 points) finds some twenty places, most visited by 20–70 runs, but chains everything near home into one cluster of 550 breaks: warm-up and cool-down walks overlap there. Start and middle positions give nearly the same clusters.
  - A grid (30m cells, each run counted once per cell, "mostly walked" at 60% of 8 or more passes, neighbours joined) doesn't chain, and gives five places 60–210m long walked on about 80% of passes. It's stricter: a place walked on half the passes is missed. Of the two, the grid is the one to build on, and its cells are also what a walked-or-run map would draw.
  - Taking warm-up and cool-down out (third look) doesn't cure the chaining either.
- **Climbs can be detected, but not timed by detection.** The watch's altitude is in whole metres and the ground is gentle (the steepest repeated climb is 13m at 3.8%), so climbs were taken as 4m or more at 1.5% on altitude smoothed over 60m. Clustering them by where they start and end (DBSCAN, 60m on the worse of the two) gives nine hills for runs, the busiest a 760m climb of 18m. But detection found that hill on 20 runs, and matching its ground found 41 passes: where a climb starts and stops moves with the altitude noise, and a longer climb swallows a shorter one. So detection proposes stretches, and matching the ground times them.
- **Matching a stretch against a run works.** Take the stretch's points every 10m; a pass enters within 30m of the first, moves forward along it never more than 30m off, and exits at its closest approach to the last. On a 522m stretch that found 78 passes on runs and 20 on walks, every one between 514 and 538m long, with times from 3:02 to 5:39.
- **A stretch's time is mostly how much of it was walked** (correlation 0.80–0.85 on that stretch), so an effort needs its walked share beside its time, and the comparison worth having may be the time over it when it was run throughout.
- **The hill as run isn't the hill as climbed.** That road's steep part is run downhill 78 times and uphill 4; what's walked on it is the gentle rise before the crest (85% of passes). A stretch has to be picked by the user or from where they walk, not from the gradient alone.
- **Shared pieces of routes as candidate stretches.** Cutting each route's representative wherever the set of routes sharing its ground changes (35m, same heading, pieces of 200m or more) gives 22 pieces, the busiest passed by 112 runs from 10 routes. It finds real corridors but repeats them (521m and 560m versions of the same road from two representatives), so pieces need merging by ground before they're offered.
- Not looked at yet: walks as their own sport, and a pass that enters or leaves partway.

### Second look (2026-11-20): all three archives, and a list of candidates

Endomondo and Google Fit runs are on the same ground, so with cross-source duplicates dropped there are 439 runs from 2016 to 2025.

- **Old files match as well as new ones.** The Whitney Way stretch (553m, north from Regent Street) has 237 passes from 230 runs across nine routes and 37 runs on no route, back to 2016, at Endomondo's 2.5 seconds a point as well as the watch's one. A stretch's history is as long as the archive, which a route's rarely is.
- **Walking in the older files: not to be trusted.** The fixed 2.0 m/s split calls 25% of Google Fit's run records walking and 35% of Endomondo's, close enough to the watch's 27% to look right, but the third look shows those files have no walking mode to split on.
- **A list of candidates.** Route overlaps and climb clusters together gave 229 pieces, 41 after dropping those with under 5 passes and those repeating a busier one. Two tests for a repeat: each line passes as an effort over the other, or 70% of the shorter lies on the longer and the less busy one isn't half as long again.
  - Cutting at every change in which routes share the ground fragments more as routes are added: 22 pieces from 15 routes became some 220 from about 30. Merging by coverage brings it back, but neighbouring pieces offset by a block still survive side by side. Cutting at junctions, where runs actually part, would be the next thing to try.
  - Passes run throughout (under 5% walked) are few on anything long: a handful on Whitney Way, none on stretches over a kilometre. "Time when run throughout" only works for short stretches; longer ones need the walked share shown beside the time.
- **Where runs are walked, as a map layer**: 30m cells with 5 or more passes by watch runs, coloured by the share of passes walked, warm-up and cool-down left out. Drawn with the candidates on a throwaway Leaflet page, the walked cells sit at junctions and the tops of rises, and a stretch's times over the years read as a scatter coloured by walked share (grey where it isn't known).

### Third look (2026-11-20): warm-up, cool-down, and which files can show walking

- **The rule:** the run proper starts with the first 30 seconds of unbroken running and ends with the last; what's before is warm-up, what's after is cool-down. On the watch's 134 runs:
  - Warm-up comes in two kinds: 31 runs start running within 5 seconds, and 49 walk for one to two minutes first (median 76m). Cool-down is nearly always there: a median of 66 seconds and 94m, a tenth over 3½ minutes.
  - Together they're 6% of recorded time, 91% of it walked, and hold 21% of all walking: 239 of 1,248 breaks. Mid-run breaks go from a median of 9 a run to 7, and the walked share inside the run from 26% to 22%.
  - The run proper is a median 16 seconds a mile faster than the whole recording (4 to 37 for the middle 80%), before any mid-run walking is taken out.
  - The 30 seconds isn't delicate: 10 to 60 gives nearly the same answer (median warm-up 27–54s, cool-down 55–84s). At 120 it breaks: a run that ends on run-walk intervals has its last third called cool-down.
  - A fixed cut (the first and last 90 seconds) is wrong both ways: it eats running from the runs that start at once, and misses the longer cool-downs.
- **It doesn't fix DBSCAN.** The big cluster near home drops from 552 breaks to 318 but stays: what's left is a chain along 940m of the street most runs go out and back on, breaks early and late in the run, not warm-up. Clustering breaks wants the grid, or positions along a stretch, not distance in the plane.
- **Only the watch's files show walking.** Run by run, speed has two modes with a clear dip between on 96% of COROS runs (the dip at 1.9–2.3 m/s), on 6% of Endomondo's and 1% of Google Fit's: phone GPS distance smears the two into one hump. On those files the rule above finds no steady running at all in 34 of 195 Endomondo runs and a median 8-minute "cool-down", and 20 "breaks" a run on Google Fit. Whether those runs had fewer walking breaks or just noisier distance can't be told from the files.
  - So walking breaks, warm-up and cool-down, and a stretch effort's walked share are for runs where walking can be told apart (next point); other efforts keep their time and show no walked share.
  - Stretch matching is unaffected: it uses positions and timestamps, and the older files match as well as the new.
- **A threshold per activity** (suggested 2026-11-20), found by splitting each run's speeds where the two classes are most distinct (Otsu):
  - On the watch's runs it lands between 2.05 and 2.30 m/s and agrees with cadence no better than the fixed 2.0 (91.4% of records against 91.7%). The modes are far enough apart that anything in the dip works.
  - It always finds a split, so a run with no walking gets half its running called walking: on the one run recorded by watch and phone both, the fixed split agrees with cadence on 98% of records and the per-run one on 37%. It needs a gate: the share of the speed variance the split explains is about 0.64 for one hump cut in two, and 0.80 on a typical watch run.
  - On the older files that share is a median 0.65 (Endomondo, from its distances or from the app's own speed field) and 0.63 (Google Fit), at 10, 20 or 30 seconds of smoothing: one hump. About one run in eight passes 0.7.
  - It isn't the format. Watch runs cut down to a fix every 2.5 seconds, with distance measured along the fixes, still agree with cadence on 93%. What can't be reproduced is the phone's position error; independent noise on each fix is far harsher than the real thing, so a simulation says nothing.
  - No ground truth for the phone: of the Google Fit copies of watch runs, one has enough fixes to compare.
  - So: per activity, gated. Cadence where the file has it; else the run's own split when it has two modes; else no walking claimed for that run, not zero walking.
## M14 — Signing in from `taco`

`taco login` posts an email and password to `/auth/tokens` and keeps the two-hour token.
An account that signs in through a provider has no password to give it, a deployment with `auth.password_login: false` has none for anyone, and a script outlives the token.
The goal: `taco login` works for any account however it signs in, and stays signed in until it's revoked.

### Design

**taiga's own device flow, not the provider's.** The shape is OAuth's device authorization grant (RFC 8628) with taiga as the authorization server: the CLI shows a short code, the user approves it in a browser that's already signed in to taiga, and the CLI polls until it has a token.
- It works for every way of signing in, passwords included, because approval happens inside a taiga session; the backend never learns how that session began.
- The alternative, the CLI running the device flow against authentik and handing taiga the ID token, depends on each provider offering that grant, needs a public client configured per provider, and adds an endpoint that takes ID tokens from outside the callback's state and nonce checks. It also does nothing for password accounts.
- A browser redirect to a port on localhost (RFC 8252) was the other candidate. It fails over SSH and on a headless box, which is where a CLI is most wanted.

**The flow.**
1. `taco login` calls `POST /auth/device` with a name for itself (the hostname by default) and gets `{device_code, user_code, verification_uri, verification_uri_complete, expires_in, interval}`. The code lasts ten minutes; the URI is `{public_base_url}/#/device`.
2. It prints the URL and the code, opens the browser when it can, and polls `POST /auth/device/token` with the device code every `interval` seconds. Until approval that answers `400` with `authorization_pending`, or `slow_down`, `expired_token` or `access_denied`, as the RFC names them.
3. In the browser, `#/device` asks the user to sign in if they aren't (by any method, coming back to the page afterwards), shows the code, the name the CLI gave and when it asked, and offers Approve and Deny: `POST /auth/device/approve` or `/deny` with the user code, behind the usual JWT middleware.
4. The next poll returns `{token, refresh_token, expires_in}`: the same two-hour JWT every other sign-in gets, and a refresh token.
5. When the JWT runs out, `taco` trades the refresh token at `POST /auth/tokens/refresh` for a new JWT. Nothing else in the API changes: every request still carries a JWT, and the authenticator still checks a signature and no database.

**What's stored.**
- `device_authorizations`: the device code as a SHA-256 hash, the user code, the client's name, when it expires, the user who approved it (null until then), whether it was denied, and when it was last polled. A row is used once and pruned once expired.
- `cli_sessions`: one per approved device. The refresh token as a hash, the user, the name, when it was made and last used, and when it lapses. Only the CLI ever holds the token itself.
- The account page lists the sessions (`GET /auth/sessions`) and revokes one (`DELETE /auth/sessions/{id}`). Revoking takes effect at the next refresh, so within two hours; that delay is the price of JWTs that need no lookup.

**Choices made, each open to change.**
- *Refresh tokens don't rotate.* Rotation with reuse detection is the textbook answer, but two `taco` processes refreshing at once would trip it and sign both out. A fixed token that's hashed at rest, revocable, and lapses after 90 days unused is enough for a homelab; rotation can come with a lock file in the CLI if it's ever wanted.
- *A session isn't scoped.* It can do whatever the account can. Read-only or upload-only sessions would need scopes in the JWT and a check on every route, with no case for them yet.
- *The user code* is eight letters from an alphabet without vowels or look-alikes, shown as `WDJB-MJHT` and compared without the dash or case: about 34 bits, for ten minutes.
- *Where `taco` keeps it:* `~/.config/taco/credentials.json` (XDG), mode 0600, in place of `cred/token` under whatever directory it was run from.

**What can go wrong.**
- *Phishing is the real risk, not guessing.* Someone starts a login and sends a victim the link; if the victim approves, the attacker's CLI is in. So the page never approves on load, even from `verification_uri_complete`: it shows the code and the client's name and says to approve only a login just started from a terminal that shows the same code. Guessing a code gains nothing, since approving a stranger's code grants them your account, not you theirs.
- *`POST /auth/device` takes no credentials* and writes a row, and the backend has no rate limiting. Pending authorizations are capped at about a hundred, expired ones pruned on insert (the count isn't locked, so requests arriving together can run a few over), and a poll sooner than `interval` gets `slow_down` without touching anything else.
- *The device code* is 32 random bytes, hashed at rest, good for one exchange, and the exchange deletes the row.
- Both codes and the refresh token never go in logs; the request logger prints paths, and none of them is in a path.
- Closed registration doesn't matter: the flow only ever signs in to an account that exists.

**Not a device flow, but nearly free afterwards: a token made on the account page.** `cli_sessions` is close to an API-key table already. A "create a token" button there would make a session without the device dance, for a cron job on a box with no one to approve it. Worth doing second, not instead.

### Tasks

Backend:
- [x] Migration `000017_cli_sessions` (both dialects): `device_authorizations`, `cli_sessions`. Numbered after the stretches migration (000016).
- [x] `POST /auth/device`, `POST /auth/device/token`, and behind the JWT middleware `GET /auth/device/{user_code}` (what the approval page shows), `POST /auth/device/approve`, `POST /auth/device/deny`.
- [x] `POST /auth/tokens/refresh` and `POST /auth/tokens/revoke` (the CLI signing itself out); `GET /auth/sessions`, `DELETE /auth/sessions/{id}`.
- [x] Pruning of expired authorizations and lapsed sessions: expired authorizations whenever one is asked for, and both hourly with the jobs prune.
- [x] Tests: the whole flow against the real store, each RFC error, a code used twice, an expired code, a revoked session's refresh, another user's session.
- [x] `docs/cli-sign-in.md`, and `device_login` in `GET /auth/providers` so an old CLI or frontend can tell the flow is there.

Frontend:
- [x] `#/device` page (taiga-frontend#22): the code from the link or typed, what's asking and as whom, Approve and Deny, and a plain confirmation afterwards. Approving checks the browser is still signed in as the account the card names, and asks again if it isn't.
- [x] Sign-in that returns to where it was headed, kept per tab so it survives a provider round trip; it also comes back to the page a session ran out on. The provider round trip hasn't been tried against a real provider yet.
- [x] Sessions on the account page, with Revoke.

`taco`:
- [x] `taco login` runs the device flow by default (taiga-cliclient#42); `taco login --email` is the password sign-in where the backend allows it. It opens a browser only where that puts a window in front of the user, never over SSH.
- [x] Refresh before a request when the token has run out or is about to, and once more on a `401`, as the session the request went out with.
- [x] Credentials in `~/.config/taco/credentials.json`, 0600, an entry per backend, behind a lock so two tacos don't lose each other's changes; `taco logout` forgets them and revokes the session.
- [x] `taco whoami`.
- [x] Tried on the deployed stack (2026-11-20): `taco login` printed its link, the code was approved in the browser, and the session worked.

### Open questions

- How long an unused session lasts: built as 90 days, started again by each refresh.
- Whether the account-page token belongs in this milestone or the next.
- Whether a signed-in `taco` should be able to approve another machine's login (`taco approve WDJB-MJHT`), for setting up a second box from the first without a browser.

## M15 — Weather (exploration)

A run's pace says little without the day it was run on: the same route is a different effort at 30°C and humid than at 5°C, and into a headwind than with it.
The goal: every workout that has a place and a time knows what the weather was, so the workout page can show it and comparisons can take it into account.
Nothing is built; this is what would need deciding.

- [ ] **Where the weather comes from.** A historical service that answers for a point and an hour, going back to the oldest workouts, some ten years.
  - Open-Meteo's archive is the first to try: reanalysis by the hour for any coordinates, decades back, no key for personal use. Check its terms and request limits before building on it, and how many days it lags behind today (a run from this morning needs its forecast endpoint, or a second look later).
  - Station observations (NOAA, Meteostat) are what was measured rather than modelled, but mean finding the nearest station and living with its gaps.
  - The files themselves don't have it (checked 2026-11-20). The Endomondo export has no weather field in any of its 405 workouts. The COROS files have no weather message either, though the app shows conditions for a run: only the watch's own average temperature per lap (262 of 269 files) and per session (146), nothing per record.
  - The watch's temperature isn't the weather: worn on the wrist it reads body heat. Five runs from late October to mid-November 2026 all average 22–25°C (72–77°F), starting warm from indoors (24–27°C) and settling to 19–23°C whatever the day was like. It isn't read from the file today, and at best it could flag a provider's answer that is far off on a hot day.
  - Behind an interface with one implementation, so the provider is a config choice (`weather.provider`, off by default) and tests use a fake.
- [ ] **What's kept**, in a `workout_weather` row per workout: temperature, dew point (what makes heat hard, more than humidity as a percentage), wind speed and direction, precipitation, cloud cover, and which provider and model said so.
  - Twice over, at the start and at the end, each interpolated between the hours either side: a long summer run that starts at 18°C and ends at 30°C isn't one that sat at 24°C throughout, and the day's hours are already in the response. Adding the end later would mean fetching everything again.
  - The mean of the two is what labels, filters and groups go by. The page shows one figure unless the run moved more than about 1.5°C, which is how far the sources disagree with each other anyway, and a range when it did.
  - Nothing finer: no weather per record, and no pace against temperature within a run. The data is hourly on a grid some 10km wide.
- [ ] **Fetched by a job, not by processing.** The file is the source of truth for everything processing derives, and reprocessing rebuilds it all; the weather comes from outside, is slow, and can fail, so it gets its own job kind (`fetch_weather`) queued when a workout first has a track, retried by the queue, and left alone by reprocessing unless the start or place changed.
  - Twice for a new run: the default blend as soon as it answers (about a day behind), so the workout page has something, and the same job queued again a week out (`run_at`) to replace it with ERA5. The row's model says which one it holds.
  - A backfill for the workouts already there, paced to the provider's limits: about 800 workouts, many from the same few places, so one request per place and month rather than one per workout.
  - Hand-entered workouts and ones without GPS have no place. Either no weather, or a home location on the account to stand in.
- [ ] **What leaves the server.** Each lookup tells a third party where and when the user ran. Round the coordinates first (0.1° is about 10km, finer than the weather model's own grid), send the hour and not the minute, and say in the docs that turning weather on does this.
- [ ] **Showing it**: on the workout page and as an optional column in the list and `taco workouts`; a wind arrow on the map, since direction only means something against the route.
- [ ] **Using it**, which is the point:
  - Filter and group by conditions: best 5k above 25°C, this route on cold days.
  - Headwind and tailwind along the track: the wind against each piece of the run's heading, which matters for an out-and-back and for a stretch run one way.
  - A temperature-adjusted pace, so a summer run and a winter one can be compared. There are published adjustments by dew point; whether one fits these runs is something the data can answer first (pace against dew point on the same route, walking taken out).

Open questions:

- Whether the model holds away from home: the first look below is one flat, inland place.
- Whether the outbound request is acceptable from the homelab at all, or the lookup should be something `taco` does from a laptop and uploads.
- Where the units live: the API speaks miles today, so Fahrenheit and mph would match, but the stored values should be metric.

### First look (2026-11-20)

Is a model close enough to what it was like on the ground? Open-Meteo's archive against two stations (through Meteostat's hourly files, its forecast-filled values dropped) at the start hours of the 627 workouts in the one 0.1° cell most of them start in, 2016 to 2026. Throwaway code, not in the repo.

- **Temperature: yes, with ERA5 asked for by name.** With `models=era5` it is typically 0.8°C (1.4°F) from the regional airport, within 1.7°C nine times in ten, and unbiased. A small airfield 3km from the cell's centre differs from that airport by 0.9°C, so the model is as close to a station as two stations are to each other. Open-Meteo's default blend is worse here: 1.3°C typically, about 0.5°C cool.
- **Dew point: yes.** ERA5 is typically 0.95°C from the airport, within 2.1°C nine times in ten. It looks twice as far from the airfield, but the two stations disagree by as much, which points at the airfield's sensor.
- **Wind: direction yes, speed roughly.** Direction is within 45° of the station in 94–95% of hours with 8 km/h of wind or more. Speed is typically 4.2 km/h off on a mean of 13, and the two stations differ by the same, so no single source says better.
- **Rain by the hour: no.** Of 581 workout hours with measured precipitation at the airport, the default blend called 39 wet (0.2mm or more) and the station agreed on 11. Across all hours the station agrees on about 60% of the model's wet hours. Keep precipitation as a coarse flag (a wet day), not as "it rained on this run".
- **So:** one provider, Open-Meteo with ERA5 pinned, and no station matching. ERA5 stops about five days short of today, so a new run takes the default blend first and ERA5 once it's there.
- **What this doesn't show.** ERA5 is built partly from station observations, quite possibly that airport's, so some of the agreement is circular; the airfield is the fairer test, and ERA5 is typically 1.0°C from it. It's one flat, inland place: nothing here speaks for the 44 workouts elsewhere, for hills or for a coast. And a station kilometres away isn't the weather on the route either.

## Later

- **Faster CI.** Backend runs took 12–14½ minutes (November 2026): the flake check, a second build of the binary, the image publish and a binary-cache upload of everything. Now a branch run takes about 2 minutes with the dependency cache restored (3¼ when the dependencies changed), plus a minute to publish on master. What's left:
  - The Determinate installer still lists FlakeHub's cache as a substituter, and every lookup there fails with 401 (some 400 a run). Harmless so far, but one run stalled 2½ minutes during those lookups; dropping it from the substituters would rule that out.
  - Run formatting, lint and tests as parallel jobs, so a formatting failure reports in seconds.
- **`taco` catches up with the API.**
  - **Built** (taiga-cliclient#44): `taco workouts` pages (`--limit`/`--offset`, `--all`) and takes `--from`/`--to`, with sport, source and why a workout doesn't count; `taco best <segment>`, `taco compare <id>...`, and `taco stretches` to list them or one stretch's efforts.
  - Left: the writes (corrections, cutting or renaming a stretch, reprocessing), routes, totals, and files that failed to parse.
- **`taco` sets itself up.**
  - **Built** (taiga-cliclient#45): `taco init <server>` finds the API (at the address given, or under `/api` there) and writes `~/.config/taco/config.toml` beside the credentials; `--config-file` and a `config.toml` in the working directory still win. Quiet unless `-v`.
  - Left: more than one server. The credentials are already kept per backend, so a named profile (`--profile test`) is the config's half of that.
- **Admin panel** in the frontend, for running the deployment rather than looking at runs: the job queue (queued, running, dead jobs with their last error, retry), reprocess one or all workouts, files that failed to parse, and eventually account management in place of `kubectl exec … useradd`. Needs a notion of an admin user first.
