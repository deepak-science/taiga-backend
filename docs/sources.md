# Sources

A source is an account somewhere else that taiga reads new activities from, so a run shows up without being exported and uploaded by hand.
There is one: COROS, through the MCP server COROS runs for AI assistants ([coroslab/coros-mcp](https://github.com/coroslab/coros-mcp)).
It is off unless `sources.coros.enabled` is set.

**Tried against COROS**, on 2026-11-21, from a server on `localhost`: it registered, signed a user in, imported the six activities of the week before, found nothing to fetch on the next sync, and refreshed its tokens three times running. "What COROS sends" below is what those runs recorded.

## Turning it on

```yaml
auth:
  public_base_url: "https://taiga.example.com"   # needed: COROS sends the browser back through it
sources:
  coros:
    enabled: true
```

or `TAIGA_SOURCES_COROS_ENABLED=true`. The server needs to reach `mcp.coros.com`, the regional server it names (`mcpus.coros.com`, `mcpeu.coros.com` or `mcpcn.coros.com`) and `s3.coros.com` over HTTPS.
`sources.coros.endpoint` points it at another MCP address.

Nothing is registered with COROS by turning it on. The first user to connect makes the server register itself there as an OAuth client named `taiga`, with the callback `{api_base_url}/sources/coros/callback`; that registration is kept and used for every user after.
Changing `auth.public_base_url` or `auth.api_base_url` changes the callback, so the server registers again and every user connects again.

## Connecting

1. `POST /sources/coros` returns `{url}` and sets a cookie; the frontend sends the browser to the URL.
2. The user signs in at COROS and approves.
3. COROS sends the browser to the callback, which checks it against the cookie, exchanges the code for tokens, and redirects to the frontend's `#/account?source=coros`, or `#/account?source_error=…` with why not.

It is the authorization code flow with PKCE, as sign-in through an OIDC provider is (`docs/oidc.md`), and the cookie is the same idea: encrypted, HttpOnly, scoped to the callback, good for ten minutes and for one callback.

A connection is the user's own. No one else's syncs use it, and `GET /sources` shows only theirs.

## What a sync does

A `sync_source` job runs when a user connects, every hour after, and when they ask (`POST /sources/{id}/sync`):

1. Ask COROS for the activities since the last sync, less a day in case the watch uploaded one late. A new connection starts a week back.
2. Skip every activity already dealt with.
3. For each of the rest, oldest first, ask for its FIT file and put it through the same path as an upload.
4. Ask for COROS's own figures on each workout that came from it and has none yet (below).

A long gap is asked for 30 days at a time, and a stretch with more activities than one list holds (200) is asked for again in halves, down to a day.

The file COROS hands over is the same one t.coros.com exports, byte for byte, so a run uploaded by hand as well is recognised by its content and not imported twice.
Everything comes across, not only runs: a pickleball game is stored and listed the way an uploaded one is, and isn't scored.
A file taiga can't read is kept with no workout, where `GET /activity_files?without_workout=true` shows it.

Each activity dealt with is remembered by its COROS id (`source_activities`), by user and not by connection.
So a workout deleted from taiga stays deleted, and disconnecting and connecting again fetches nothing twice.

History from before the connection isn't fetched: upload an export for that.

### COROS's own figures

COROS works out things about an activity that are in no file: training load, aerobic and anaerobic training effect, a training focus ("Threshold"), a performance word ("Best"), and its adjusted, moving-average and best-kilometre paces.
Its activity detail tool gives those along with its own totals, and taiga keeps all of it beside the workout (`coros_metadata`), served by `GET /workouts/{id}/coros`.

- **It is for tracking only.** Nothing in it feeds best efforts, splits, totals or the workout's own summary, which stay worked out from the file. COROS's distance and time can differ from taiga's, and both are kept.
- **It isn't derived from the file**, so reprocessing leaves it alone, as with weather. Deleting the workout deletes it.
- **Which workouts:** every one whose file a sync has matched to a COROS activity, a run uploaded by hand included, since its file is the same one. At most 20 a sync, newest first, with any whose reply couldn't be read last time behind the ones not yet tried; whether COROS limits these calls isn't known. An activity a sync dealt with before taiga kept its sport code gets its figures once a sync lists it again, which is within a day of importing it; older ones wait for the walk back described below.
- **Stored in metres and seconds**, paces as seconds a kilometre, whichever units the account shows. What COROS said is kept whole as well, so a figure taiga doesn't read today can be read later without asking again.
- **When it can't be had**, the activities still come in and the sync still finishes: the connection's `last_error` says the figures couldn't be read or fetched, and the next sync asks again. Only a grant COROS has ended stops it.

Runs from before the connection whose files were never matched by a sync have none: the list would have to be walked back through the years for that, which isn't built.

### The daily limit

COROS hands over 50 FIT files a day for an account, and counts every request, including ones for a file already fetched.
taiga takes at most 45 in any 24 hours and leaves the rest for whatever else the user has connected to COROS.
A sync that reaches either limit stops there without failing and carries on from the same activity next time.
COROS counts a file when it issues the link, so taiga does too (`source_file_requests`, kept for a day): a link whose download or import then failed is still one of the 45.
An activity recorded in parts has several files, and how many isn't known until COROS has issued them, so the last activity of a day can go a file or two over 45. The five left under COROS's 50 cover that.

### When it goes wrong

| What happened | What taiga does |
| --- | --- |
| COROS can't be reached, or answers with an error | The job retries with backoff, five times, then is dead; `last_error` on the connection says why. The hourly sweep leaves a connection with a dead job alone until it's retried (`POST /jobs/{id}/retry`, or `POST /sources/{id}/sync`). |
| COROS no longer honours the tokens (the user removed taiga there, or a refresh token was lost) | The connection is marked `needs_reconnect` and nothing more is asked until the user connects again, which carries on from where the syncs had got to. |
| An activity is still being recorded | It waits for the next sync; later ones aren't held up. |
| COROS's list or file reply can't be read | The sync fails, as an outage does. |
| COROS's own figures for an activity can't be read or fetched | The sync finishes and `last_error` says so; they're asked for again next time. |

That last one matters because COROS's tools answer in prose written for a language model, not data (the whole of it is under "What COROS sends"):

```
1. Outdoor Run — 2026-11-21
   Time Window: startTimestamp=1795286592 | endTimestamp=1795289692
   LabelId: 481919417381322856 | SportType: 100
```

taiga reads the ids and times out of that by pattern, and checks the count against the "(5 records)" the reply states. A change of wording makes the sync fail loudly; it can't read as "nothing new".
The file is the only thing trusted for the workout itself.

## What's kept, and how

- **The registration** (`source_clients`): the client id COROS issued (and a secret, had it given one), and the addresses of its servers.
- **Each user's tokens** (`source_connections`): an access token (30 days) and a refresh token (180 days, replaced at every refresh, so a connection that syncs stays alive).
- **What's been dealt with** (`source_activities`): COROS's id and sport code for the activity, when it started, and which file it became.
- **COROS's own figures** (`coros_metadata`): one row a workout, with the reply they were read from.
- **What was asked for** (`source_file_requests`): each request for an activity's files in the last day, for the daily limit.

The registration and the tokens are encrypted (AES-256-GCM) under a key derived from the token signing key (`tokens.private_key_path`), so a copy of the database alone doesn't give access to anyone's COROS account.
Replacing that key makes them unreadable: every user connects again, and the first to do so registers the deployment with COROS afresh, in place of the registration that can no longer be read.
Neither is ever returned by the API or written to the log.

Download links for files work for anyone who has them, with no sign-in. They are fetched once, without the user's token, and never stored or logged.

`DELETE /sources/{id}` deletes the tokens and asks COROS to revoke them. Workouts already imported stay.

## What taiga can do at COROS, and what it does

COROS has one scope, `mcp.tools`, covering every tool its server offers, and some of those write: training plans, scheduled workouts, routes.
taiga asks for that scope because there is no narrower one, and calls three tools, all reads:

- `querySportRecords`, to list activities;
- `queryActivityFitFileDownloadUrls`, to get a file's link;
- `getActivityDetail`, for COROS's own figures on an activity.

What reaches COROS is the account's own sign-in, the dates asked about, and the ids of activities fetched. Nothing from taiga's side: no workouts, no other users.

## What COROS sends

Recorded on 2026-11-21 from `mcpus.coros.com`. The replies taiga reads are kept, with the ids, places and some figures changed, in `internal/coros/testdata/`, and the parser's tests run on them.

### Signing in

- `POST https://mcp.coros.com/mcp` with no token is a `401` whose `WWW-Authenticate` names the region's metadata, `https://mcpus.coros.com/.well-known/oauth-protected-resource/mcp`. That says the resource is `https://mcpus.coros.com/mcp` and the authorization server `https://mcpus.coros.com`, whose own metadata is at `/.well-known/oauth-authorization-server`.
- Endpoints: `/oauth2/authorize`, `/oauth2/token`, `/oauth2/revoke`, `/connect/register`. PKCE is S256 only. Scopes offered: `openid`, `mcp.tools`, `offline_access`.
- **Registration** (`POST /connect/register`) took a client named `taiga` with an `http://localhost` callback and the `authorization_code` and `refresh_token` grants, with no token of its own and no approval, and gave back a client id (a UUID). It was asked for `client_secret_basic` and gave **no secret**: taiga is a public client there, proving itself by PKCE alone, and was given a refresh token all the same.
- **The token response:** `token_type` `Bearer`, `expires_in` 2591999 (30 days), `scope` `mcp.tools offline_access`, an access token that is a JWT (886 characters) and a refresh token that is an opaque 128 characters. No ID token, as `openid` wasn't asked for.
- **Revoking** the refresh token at `/oauth2/revoke`, naming the client by id, answered `200`.
- **Refreshing** works for a client with no secret: three in a row, forced by treating the stored token as expired. Each gave a new access token good for another 30 days and a new refresh token, and each used the refresh token the one before had saved. COROS's FAQ says a refresh token lasts 180 days; whether the one it replaces stops working at once wasn't tried.

### The MCP server

- Protocol version `2025-11-25`. It calls itself `mcp-server` version `1.0.0`, which says nothing about which build is running; the HTTP responses carry a `v: Build2.11.15` header that does.
- 34 tools. None declares an output schema, and no reply carried structured content: text is all there is.
- Each tool says whether it writes (`readOnlyHint`). Eight do: `uploadRouteFile`, `createScheduledWorkout`, `updateScheduledWorkout`, `createTrainingPlan`, `updateTrainingPlan`, `createSingleWorkout`, `scheduleWorkout` and `updateWorkoutDetails`. The three taiga calls are marked read-only.

### `querySportRecords`

Asked with `{"startDate": "20261112", "endDate": "20261122", "limit": 200}`. No argument is required; the others (`sportTypeCodes`, `locationKeyword`, and limits on distance, duration and pace) aren't used.

The reply is one text item holding **a JSON string**: it starts and ends with a double quote and its line breaks are the two characters `\n`. Decoded:

```
Sport Records — 2026-11-12 to 2026-11-22 (6 records)
========================

1. Outdoor Run — 2026-11-21
   Location: Springfield Run
   Start Coordinates: 39.800000, -89.650000
   Time Window: startTimestamp=1795286592 | endTimestamp=1795289692
   Duration: 51:40 | Distance: 7.63 km
   Average Pace: 6:47 /km | Avg HR: 150 bpm | Calories: 300 kcal
   LabelId: 400000000000002856 | SportType: 100

5. Gym Cardio — 2026-11-15
   Location: Gym Cardio
   Time Window: startTimestamp=1794752085 | endTimestamp=1794756056
   Duration: 1:06:11
 | Avg HR: 150 bpm | Calories: 300 kcal
   LabelId: 400000000000007616 | SportType: 400
```

- Newest first. The dates in the heading are the ones asked for, and "records" is plural even for one.
- `startTimestamp` is Unix seconds and is the start the FIT file's session states, to the second.
- `Location` is the town and the sport, or an issue on COROS's tracker says the workout's name when it has one. An activity with no GPS has no coordinates line, and its figures break across two lines where the distance would be.
- A sport without a pace has `Average Speed: 1.6 km/h`, and a short distance is in metres (`505 m`).
- With nothing in the window: `No sport records found from 2024-01-01 to 2026-11-21.`
- A window of nearly three years returned 101 entries in one reply, so the default of 20 isn't a cap on what `limit` can ask for.

taiga reads four things from each entry: the heading, `startTimestamp`, `endTimestamp`, and `LabelId` with `SportType`.

### `queryActivityFitFileDownloadUrls`

Asked with `{"labelId": "…", "sportType": 100, "limit": 10}`: `limit` is how many files it may name, 5 unless asked and never more than 10. The reply is plain text, not a JSON string:

```
Activity FIT file download URL(s):
1. 400000000000001234_0123456789.fit
   https://s3.coros.com/fit/v2/400000000000000000/400000000000001234_0123456789.fit
```

- The path holds the account's id, then a file id (which is not the `labelId`) and the last ten characters of the file's MD5.
- The link is served from S3 through CloudFront with no signature and no expiry in it, and needed no credentials. Its `ETag` is the file's MD5.
- The file is the one t.coros.com's export gives: same bytes, same name.
- For an activity still being recorded, COROS documents the reply `This activity is still in progress; the FIT file is available after it ends.` taiga matches "still in progress".
- **What it says at the daily limit has not been seen.** taiga treats a reply that mentions "limit" and has no link as the limit; anything else without a link is a failed sync.

### `getActivityDetail`

Asked with `{"labelId": "…", "sportType": 100}`. The reply is a JSON string like the list, a heading and then a line a figure:

```
🏃 Outdoor Run Activity Details
========================================

Workout Time: 51:40
Distance: 7.63 km
Total Time: 52:05
Average Pace: 6:47 /km
Moving Average Pace: 6:38 /km
Adjusted Pace: 6:43 /km
Best Kilometer: 6:20 /km
Average Heart Rate: 150 bpm
Average Cadence: 170 spm
Average Stride Length: 1.05 m
Average Power: 250 W
Elevation Gain / Loss: 54 m / 51 m
Calories: 300 kcal
Training Load: 120
Aerobic TE: 3.6
Anaerobic TE: 0.9
Training Focus: Threshold
Performance: Best
```

- **Which lines there are goes by the sport.** A pickleball game had only workout time, distance, heart rate, calories, load, the two training effects and focus; a gym session had no distance.
- taiga reads each line it knows by its label, and passes over one it doesn't. A known label whose value it can't read (a unit not seen before, a time that isn't one) fails that activity's figures; it is never guessed at.
- **Only a metric account has been seen.** Miles and feet are read if they come as `mi`, `/mi` and `ft`, which is a guess at what an imperial account gets.
- Whether these calls are limited isn't known.

## How much of this will hold

COROS says nothing about it. Its docs ([coroslab/coros-mcp](https://github.com/coroslab/coros-mcp)) have no statement on stability, versions, deprecation or compatibility, and describe what the list and file tools return only as "matching activities with date, sport, location, distance, duration, pace or speed, and activity identifiers and times" and "file names and their FIT download links". The one example of a list entry they give leaves out the identifier lines.

What the record shows, from launch on 2026-05-05 to November:

- Thirteen changelog entries in six and a half months.
- **The wording of replies does change.** 2026-11-16: "Added an 'in progress' indicator for ongoing activities and improved FIT file retrieval messages", five days before the run above.
- **Tools get removed without notice.** `analyzeActivityDetail` went on 2026-10-23, with no deprecation recorded.
- The sign-in has moved twice: one endpoint for all regions on 2026-05-19, and a "stateless" flow on 2026-06-24.
- The FIT tools and their limit of 50 arrived on 2026-06-23. The limit is "per calendar day" in the changelog and "a fixed 24-hour window" in the FAQ.
- Open issues include every activity tool failing with a "Tool call anomalies detected" error (2026-09-13), which is the kind of reply that reads here as one taiga doesn't know.

So the list should be expected to change under taiga, and the design is for that:

- The FIT file is the only thing the workout is built from. The list decides what to fetch and nothing else.
- A reply that doesn't parse, or whose entries don't number what its heading says, fails the sync, shows on the connection as `last_error`, and after five tries waits to be asked again. It never reads as "nothing new".
- Nothing is lost while it's broken: the next sync that works carries on from the last one that did.
- COROS's own figures are extra: a detail reply that can't be read costs those figures and nothing else.
- When it does change: record the new reply (the tests' files show the form), fix `internal/coros/parse.go` or `detail.go`, and add the new reply beside the old.

## What isn't known

- **Whether a client that isn't an AI assistant is welcome.** COROS's docs assume one throughout, though registration turned nothing away. Read their terms before turning this on for anyone but yourself.
- **How the limit resets**, what COROS says when it's reached, and whether listing is limited at all.
- **How to take a registration back.** Disconnecting revokes the user's tokens; the client taiga registered stays at COROS.
- **Activities recorded in parts** (a triathlon). The file tool can return several links for one; taiga fetches and stores each, and keeps the first as the activity's file. Whether `limit` applies to one activity's parts hasn't been seen; taiga asks for the most, 10, and logs a warning when exactly that many come back, since there may be more.
