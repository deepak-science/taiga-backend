# taiga roadmap

The goal is seeing and comparing runs in a browser.
Pieces: this API, the browser frontend (`taiga-frontend`), and the `taco` CLI (`taiga-cliclient`) for scripting.

**Non-goals:** sharing and anything cross-user (friends, feeds, public profiles, leaderboards between users).
Each user's data is private to them, and every query stays scoped by `user_id`.
Multiple accounts exist only so more than one person can use the same deployment.

This file holds only what's left. What's been built, with the design notes and the looks at the data behind it, is in [docs/roadmap-archive.md](docs/roadmap-archive.md).
Milestones keep their numbers in both files, so "M13" means the same milestone in either.

**Done in full:** M1 (OIDC sign-in), M3 (deploy), M11 (time windows), M17 (metrics). Every other milestone below has its built part in the archive.

---

## M2 — Upload hygiene

- [ ] Frontend and `taco`: say "already uploaded" on a `200` re-upload, offer a backfill action, and list files that failed to parse.

## M4 — Cleanup

- [ ] Tokens are still issued as `trygo-<environment>` (`internal/tokens`). Renaming that turns away every token already out, so each browser signs in once more and each `taco` session refreshes; do it with a deploy where that is fine.

## M6 — Visualisations

- [ ] A self-hosted vector basemap (PMTiles + MapLibre): full control of the style, and the map's area kept away from a tile server.

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

Average, max and min HR per workout are stored (M16).

- [ ] A per-user max HR (and optionally resting HR) setting, defaulting to the highest HR seen across the user's workouts.
- [ ] Time in HR zones per workout, derived from that setting. Changing the setting enqueues stats jobs rather than reprocessing files.

## M8 — Automatic import

COROS syncs through its MCP server (`docs/sources.md`); Endomondo JSON and Google Fit TCX upload by hand.

- [ ] COROS promises nothing about its replies, and reworded the FIT tools' five days before the first run against it (`docs/sources.md`, "How much of this will hold"). The terms are still unread.
- [ ] Pickleball arrives as FIT sport `racket`, so it is named "Madison evening racket"; `sportNouns` has no better word for it.
- [ ] **History.** A new connection starts a week back, since older runs are already here from exports and each file asked for counts against the 50 a day. To backfill a fresh account: walk back in windows, and match on start time first so a run already held costs no request.
- [ ] **Which sports.** Everything comes across, as an export does. A per-connection list of sport codes (100–103 running, 900 walking) would keep the rest out.
- [ ] Not handled: connecting again while a sync is running can leave the old session's refreshed tokens saved over the new ones, which shows as "connect again" once more.
- [ ] Endomondo notes (`message`, on 52 of 405 workouts) aren't kept; workouts have nowhere to put them yet.
- [ ] **Jittery runs, the frontend half.** A jittery Google Fit run placed on a route (`jitter_route_id`, `jitter_route_miles`) counts in totals at the route's length. Not shown yet: the route's distance on the workout ("about 6.3 mi, on route 12"), and map-matched splits as estimates.
- [ ] **Inflated Endomondo totals.** Some counted Endomondo runs have totals far over their tracks (395 and 396 at +22%, 482, 495 and 499 at +10–12%). Endomondo files have dense points, so the track shouldn't cut corners that much; worth the same pace check against their routes that confirmed the jittery Google Fit runs.

## M9 — Routes

- [ ] **The same ground in different sports.** Routes are per sport (`ClusterRoutes` runs once per scored sport, and a move or merge onto a route of another sport is refused), so a loop both walked and run is two routes. Found placing jittery runs (#130), where 713 and 739 tied with walking routes. On 2026-11-24, 4 of the 56 routes pair up across sports, all well inside the route tolerance (Fréchet 18–29m): running 2 "Short run" (67 runs) with walking 16 (3), running 4 "Long run" (43) with walking 22 "Long walk" (4), running 10 "Shorter run" (7) with walking 64 (2), and walking 29 "Shorter walk" (16) with running 44 (2).
  - **Cross-sport by default:** a route is ground. Cluster every scored sport together, and split a route's figures (best time, run count, trend) by sport, since walking and running times don't compare. One concept instead of two, and a route page shows both. Costs: route list and detail figures gain a sport dimension; the first regrouping merges the pairs above, and of two named routes ("Long run", "Long walk") only one name survives, since names carry over by overlap; moves and merges lose their sport checks; the representative needs a rule (most runs' sport?).
  - **Linked:** routes stay per sport, and regrouping also records a derived link between routes of different sports whose representatives pass `SameRoute`. A route page says "also walked: Long walk". Every comparison stays within a sport, which is what M9's goal (comparing a route's runs on time) needs, names are untouched, and nothing migrates. One more derived relation, rebuilt from scratch like the rest.
  - Leaning towards linked first: it's cheaper, keeps time comparisons honest, and doesn't merge anything named. Going cross-sport later is still open if the link is wanted everywhere. Either way, placing jittery runs keeps matching the run's own sport.
- [ ] **Variations.**
  - Small detours (a block around construction, GPS drift) fall inside the tolerance and still match.
  - Extensions (the usual loop plus an extra mile) cover most of the route but add length: match them as a variant of the route, flagged, and keep them out of the route's total-time comparison. Today they fail the length check and become a route of their own.
  - Truly different runs become their own route. Partial overlap between routes is what stretches (M13) would handle.
- [ ] **A coarse index** (the set of ~150m grid cells a track passes through, compared by overlap) to find candidate routes without comparing against every route's first run. Not needed at hundreds of runs: most pairs fail the endpoint check.

## M10 — Corrections

- [ ] Other fields the file got wrong, as more override columns applied the same way, once there's a case for one.

## M12 — Workout names

- [ ] `taco workouts` doesn't show the name yet.
- [ ] **Renaming a workout** (review of taiga-backend#108): a user-set title kept as an M10 correction, so it wins over the derived name and survives every reprocess; clearing it gives the derived one back. This is the answer wherever the rule calls a workout the wrong thing, rather than more cases in the rule.
  - The case that raised it: a corrected sport keeps the file's sub-sport, so an `indoor_walking` file corrected to a run is still "Indoor walk", and a strength session corrected to a run is still "Strength workout". Only the run and ride nouns give way to a corrected sport today.
  - A sub-sport correction beside the sport one would fix the same case at its root, and would also decide whether the workout is scored (a treadmill run isn't), so it's the larger change.
- [ ] **More than time of day**, later and only while still deterministic: the route's name ("Saturday morning Lake loop"). With nearly every run in one town the place stops telling runs apart, which is when the route's name would earn its spot.
- [ ] Endomondo files state no zone, so its sessions without GPS (119 of 405 in the export) have only a noun. The user's other workouts around that date would say where they were.

## M13 — Stretches

The three looks at the data behind stretches and walking breaks are in the archive.

- [ ] **Slack at a stretch's start.** A stretch that starts at a junction only matches runs arriving the way its source did: cut from Regent Street's corner, Whitney Way gets 40 of its 78 passes, and all 78 once it starts 50m on. The frontend should say so, or the start should be given some slack along the stretch.
- [ ] **Walking per workout outside the series:** no break count or run-only pace in the workout list.
- [ ] **Places walked versus run.** Across runs, the same ground walked again and again is a pattern: a hill that's always walked, a warm-up block. Bin the ground into cells along the tracks and compare how often each is walked or run, drawn on a map. Those places are also candidate stretches, found without drawing them; so are the shared pieces of overlapping routes.
- [ ] **Suggested stretches**: the candidates of the second look (shared pieces of routes, climbs) offered for naming. Not built; they repeat each other too much yet.
- [ ] **Frontend:** stretches and walking breaks on the workout page itself, cadence as a line on its graph, and picking a range by dragging on the graph, which still zooms.
- [ ] A stretch's line drawn on the route's map, which needs its points in the route's answer.
- [ ] **Sparse GPS** (from review of the first pass). Matching goes point by point, so it needs fixes close together: a straight track matches itself with fixes up to 40m apart and fails at 60m. Two causes, one weakness:
  - a record may only advance 80m along the stretch (the guard against jumping to a later part of a stretch that doubles back), so fixes further apart than that fall behind and drop off it;
  - an end is only reached by a fix near it, so an end crossed between two distant fixes is missed.
  - The watch's runs are 20m apart at worst (a marathon thinned to 2000 points), so nothing recorded so far is affected. It would show across a GPS dropout or on a sparse phone recording. When that's seen: fill in points between distant fixes before matching, as `BuildTrack` already does for routes, with a cap so a long dropout isn't bridged; simpler than scaling the lookahead and testing segments for crossings.
- [ ] Not handled: a stretch's efforts aren't rebuilt if matching changes without `extractorVersion` moving, and on PostgreSQL a workout processed in the same instant a stretch is made can miss it until it's next reprocessed.

## M14 — Signing in from `taco`

- [ ] **A token made on the account page.** `cli_sessions` is close to an API-key table already. A "create a token" button there would make a session without the device dance, for a cron job on a box with no one to approve it.
- Open questions:
  - How long an unused session lasts: built as 90 days, started again by each refresh.
  - Whether a signed-in `taco` should be able to approve another machine's login (`taco approve WDJB-MJHT`), for setting up a second box from the first without a browser.

## M15 — Weather

The backend half is built (`docs/weather.md`); the archive has how the sources compared against weather stations.

- [ ] **Showing it**: as an optional column in the list and `taco workouts`; a wind arrow on the map, since direction only means something against the route. The arrow takes the mean speed and the mean direction as a vector, and shows how steady the wind was (steadiness, or constancy: the vector mean speed over the plain mean speed, 1 for a wind that held and near 0 for one that swung round) by how wide it fans, so a wind that turned from north to south doesn't read as calm or as one direction.
  - [ ] **Fields and values, not a sentence** (feedback 2026-11-21). The workout page's line reads as prose and has to be parsed by eye. Show the reading as labelled figures instead, the way the rest of the summary is laid out: temperature, dew point, humidity, wind (speed and direction), cloud cover and precipitation each under its own heading, start and end side by side where they differ, and the source named.
- [ ] **Using it**, which is the point:
  - Filter and group by conditions: best 5k above 25°C, this route on cold days.
  - Headwind and tailwind along the track: the wind against each piece of the run's heading, which matters for an out-and-back and for a stretch run one way.
  - A temperature-adjusted pace, so a summer run and a winter one can be compared. There are published adjustments by dew point; whether one fits these runs is something the data can answer first (pace against dew point on the same route, walking taken out).
- Open questions:
  - Whether the dew point can be trusted on hot afternoons in dry country, where the second look found ERA5 far too humid. That is where a heat adjustment would lean on it.
  - Where the units live: the API speaks miles today, so Fahrenheit and mph would match, but the stored values should be metric.

## M16 — More from each workout

- [ ] **Walk-back loose ends** (Copilot on #113, left since neither touches the data so far). The walk back looks for workouts whose `source` is `LIKE 'COROS%'`, which PostgreSQL matches by case and SQLite doesn't: a COROS file with no product name is called "Coros" and skipped in production only. Compare lowercased. And its check for a workout already tied (`source_activities` by `activity_file_id`) has no index; add one on `(user_id, kind, activity_file_id)` in both migration sets if histories grow.
- [ ] **Cadence off foot.** A sport that isn't a run or walk keeps the file's count as revolutions, so the frontend shows a gym or racket session's cadence as "22 rpm", when the watch was counting one foot's strides there too. Decide per sport what the count is (steps, revolutions, or nothing worth showing), in `Figures.Counted` and the series' cadence column, and label it to match.
- [ ] **Other sources.** See what the Endomondo JSON, Google Fit's TCX and a Garmin FIT have that corresponds (heart rate summaries, ascent, calories, laps, running dynamics), and read it into the same fields.
- [ ] **Later: daily fitness** from COROS, a row per user and day, which is a new kind of data here: the fitness assessment (VO2max, running level, threshold pace, race predictions) and training load first. The predictions set against actual best efforts is the picture worth having. Recovery, sleep, HRV and stress only if taiga is to be a health log too.
- [ ] **Later: form over a long run.** With ground contact time in the series, show it rising as a run goes on, and compare that across runs.

## M18 — Admins

- [ ] **Later, from the provider:** an OIDC group or claim (authentik's groups) that grants the role at sign-in, per provider, off by default.
- [ ] **Disabling an account, and registration at runtime:** both need state the token check doesn't read yet. Deleting an account and everything it owns is its own piece of work: files are shared by hash.
- [ ] **Replacing the deployment's registration with COROS.**
- [ ] **Why files failed to parse,** as counts by reason. By user is done; the reason isn't stored yet.
- [ ] **Frontend `#/admin`**, shown only to admins: accounts, jobs (with a reprocess action and dead jobs), sources and the deployment page.
- [ ] **`taco admin ...`** for the same calls, so the runbook's `kubectl exec` lines go away.

## M19 — Export, import and backups

- [ ] `taco import`, and the frontend proxy's body limit and read timeout for `/api/import`.

### Before an upgrade

A dependency bump can break things nothing tests for (sql-formatter changing migration layout, a FIT library reading a field differently, a driver change), and migrations run by themselves on boot (`db.auto_migrate_up`). So a deploy should be undoable from its own backup:

- [ ] **A backup before every deploy:** the homelab runs `cmd/backup` as a pre-deploy Job (or an init container ahead of the server), so the archive is of the version before the new image migrates anything. Then rolling back means the old image plus that archive, even if the migration's `down` loses data.
- [ ] **Rehearse risky ones:** for a bump that touches parsing, the database driver or migrations, restore the latest backup into the dev stack, run the new image against it, reprocess, and diff what the old and new images derive from the same files (workouts, totals, best efforts, routes). Then "reprocessing changed every workout's distance" shows up on the dev stack, not as a surprise on the real one.
- [ ] Keep bumps small enough to tell what broke: Go modules, the flake and the formatter in separate commits, so a bisect lands on one of them.

## Later

- **Repository settings in Terraform.** Master here only takes pull requests (the `default-branch` ruleset), so `just release` can't push, and `seal` works only because the release app is on the ruleset's bypass list (`Integration`, `always`; set by hand 2026-11-22). taiga-frontend has no ruleset at all, so the two have already drifted apart.
  - A root in proxmox-homelab's `iac-live` (beside `github-oidc`, which already has the `deepak-science` GitHub provider), with `drift-ci` catching hand edits: import the backend's `default-branch` and Copilot rulesets, give the frontend the same, and put the release app on both bypass lists by its App ID.
  - Once the frontend has the ruleset, its local `just release` stops working too: both release through `seal`, or a release PR as with #117.
- **Faster CI.** A branch run takes about 2 minutes with the dependency cache restored. What's left:
  - The Determinate installer still lists FlakeHub's cache as a substituter, and every lookup there fails with 401 (some 400 a run). Harmless so far, but one run stalled 2½ minutes during those lookups; dropping it from the substituters would rule that out.
  - Run formatting, lint and tests as parallel jobs, so a formatting failure reports in seconds.
- **`taco` catches up with the API:** the writes (corrections, cutting or renaming a stretch, reprocessing), routes, totals, and files that failed to parse.
- **`taco` with more than one server.** The credentials are already kept per backend, so a named profile (`--profile test`) is the config's half of that.
