# Backups

A taiga backup is a tar that taiga writes and reads itself (`internal/backup`). It holds files and whatever nothing can rebuild from them. Everything else (summaries, segments, series, tracks, laps, routes, stretch efforts, duplicates) is derived from the files, so a restore rebuilds it by reprocessing.

There are two kinds, told apart by the manifest's `kind`:

- `user`: one user's export, from `GET /export` or `taco export`;
- `deployment`: every account, with what it takes to sign in to each again, from `cmd/backup`. `cmd/restore` reads it into an empty deployment.

## Exporting one user

`GET /export` (signed in) streams the user's archive as `taiga-YYYY-MM-DD.tar`:

```bash
taco export -o taiga.tar
curl -H "Authorization: Bearer $TOKEN" https://taiga.example.com/api/export -o taiga.tar
```

`?fetched=false` (`taco export --without-fetched`) leaves out weather and COROS's figures. Both can be fetched again, but that's slow, and each lookup tells the provider again where and when the user ran.

The documents are read in one snapshot (REPEATABLE READ on PostgreSQL), so they are one moment's state; the file bytes are streamed after it. The router's 30-second timeout doesn't apply to it, and each write to the client gets its own two minutes. If the export fails after it has started, the connection is cut rather than ended cleanly, so a partial download never reads as a whole archive.

## Backing up the deployment

`backup` is in the image beside `useradd`, and reads the same config:

```bash
kubectl -n taiga exec deploy/taiga-backend -- backup > taiga.tar
backup -o /backups/taiga.tar          # written beside it and renamed, so a failed run leaves the last one
backup -fetched=false                 # without weather and COROS's figures
```

It holds every account's email, display name, bcrypt password hash, whether the email is verified, and linked OIDC identities (issuer and subject), so keep it as you would the database. A file is in it once, however many accounts uploaded it. Every account is read from one snapshot.

It never migrates the database. Before an upgrade, run it with the image that's deployed: a new image's queries may not match the old schema.

## Restoring

```bash
restore -i taiga.tar
```

It refuses a deployment that already has an account, and a user's export. Into an empty one it:

1. stores every file in the file repo, checked against its hash;
2. per account: makes the account and its linked identities, then its stretches, then a workout from every file that had one, as an upload makes them (so the derived data is this taiga's, and each workout is timed over the stretches);
3. puts back hand-entered workouts, corrections (each reprocesses its workout), stretches' source workouts, routes with their names and runs, the user's placements, what sources dealt with, and COROS's figures; weather only where the rebuilt workout has the span it was asked for, so the sweep asks again for the rest;
4. queues route detection, which marks duplicates and regroups the routes, keeping the restored names. The server's worker runs it.

A file this taiga can no longer read is kept as a file without a workout, and listed. A restore that fails partway leaves a part-restored deployment: empty it (`db.drop_on_start`, or a fresh database) before trying again. The file repo needs no emptying, since it's content-addressed.

Not kept: when rows were made (`created_at` on accounts, files, stretches and routes is the restore's time). A restore connects no COROS account: each user connects again, and what sources dealt with keeps that from fetching anything twice.

## What's in it

A user's export, in this order:

| Entry | Holds |
| --- | --- |
| `manifest.json` | `format` (`taiga-backup`), `kind`, `version`, `processor_version`, `created_at`, `fetched`, and `owner` (`email`, `display_name`), which is for whoever reads the archive and is never used to sign in |
| `files.json` | Every uploaded file: `sha256`, `timestamp`, `created_at`, and `workout`, which says whether a workout was made from it (false for a file that failed to parse, or whose workout the user deleted). In the order their workouts were made, then the rest in upload order: a restore makes them in that order, so duplicate detection keeps the same recording between equal sources |
| `workouts.json` | Hand-entered workouts in full, since they have no file |
| `corrections.json` | The user's `sport` and `excluded_reason` per file-backed workout |
| `stretches.json` | Name, sport, length, points, and `source_file`, the workout it was cut from |
| `routes.json` | Each route as detection last left it: `key`, `name`, `representative` and its runs (`workouts`). The runs are kept so that a restore's detection keeps the names, which go to the old route each new group overlaps most |
| `route_overrides.json` | The user's placements: `file`, `route` (a route's `key`, or null for kept off every route), `with_group` |
| `source_activities.json` | Every activity a sync has dealt with, so that a restore neither fetches it again nor brings back a workout the user deleted |
| `source_match_attempts.json` | Workouts the walk back through COROS's history looked for and didn't find |
| `weather.json` | Each source's reading, with what it was asked (empty when `fetched` is false) |
| `coros_metadata.json` | COROS's own figures, with the reply they were read from (empty when `fetched` is false) |
| `files/<sha256>` | Each file's stored bytes |

A deployment's backup has `accounts.json` after the manifest (`key`, `email`, `display_name`, `password`, `email_verified`, `created_at`, `identities`), then the same documents for each account under `accounts/<key>/`, then `files/<sha256>` for every account's files together.

A workout is referred to by its file's SHA-256, never by a database id, so an archive goes into any deployment, SQLite or PostgreSQL. A route's and an account's `key` mean something only inside their own archive.

Never in it: COROS tokens and the client registration (sealed under the signing key; a restore should connect again), CLI sessions, device sign-ins, and jobs.

## Reading one

`backup.Read` checks the archive as it goes, and turns away one that:

- doesn't start with a manifest of format `taiga-backup` and a known `kind`;
- is of a newer version than this taiga reads;
- has an entry it doesn't know, an unknown field in a document, anything after a document's JSON value, or a document after the files;
- has an account key that isn't lowercase letters, digits and dashes, or the same key twice;
- has a file whose bytes don't match its name, or that no `files.json` lists;
- is missing a document, or a file a `files.json` lists, which is how an archive cut short shows.

`backup.Write` also checks each file against its hash as it reads it from the repo, so a damaged repo fails the backup instead of producing one that won't restore.

One file is held in memory at a time while its hash is checked (at most 64MB; uploads stop at 32MB).

## Proving it

`cmd/restore`'s test builds a deployment with every kind of state above, backs it up, restores it into a fresh one, runs detection, backs that up, and compares the two (less when rows were made). A new kind of user state that the backup doesn't carry shows up there as a difference, once the test makes some.

## Changing the format

The archive refers to tables by meaning, not by schema, so a migration doesn't change it on its own. A new table that holds user state nothing can rebuild needs a document here, and a new field on an existing one goes in as a version bump: `Read` turns away unknown fields, so an older taiga refuses a newer archive instead of silently dropping what it can't read.
