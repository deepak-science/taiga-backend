# Backups

A taiga backup is a tar that taiga writes and reads itself (`internal/backup`). It holds a user's files and whatever nothing can rebuild from them. Everything else (summaries, segments, series, tracks, laps, routes, stretch efforts, duplicates) is derived from the files, so a restore rebuilds it by reprocessing.

## Exporting

`GET /export` (signed in) streams the user's archive as `taiga-YYYY-MM-DD.tar`:

```bash
curl -H "Authorization: Bearer $TOKEN" https://taiga.example.com/api/export -o taiga.tar
```

`?fetched=false` leaves out weather and COROS's figures. Both can be fetched again, but that's slow, and each lookup tells the provider again where and when the user ran.

The documents are read in one snapshot (REPEATABLE READ on PostgreSQL), so they are one moment's state; the file bytes are streamed after it. The router's 30-second timeout doesn't apply to it, and each write to the client gets its own two minutes. If the export fails after it has started, the connection is cut rather than ended cleanly, so a partial download never reads as a whole archive.

## What's in it

Entries, in this order:

| Entry | Holds |
| --- | --- |
| `manifest.json` | `format` (`taiga-backup`), `version`, `processor_version`, `created_at`, `fetched`, and `account` (`email`, `display_name`), which is for whoever reads the archive and is never used to sign in |
| `files.json` | Every uploaded file: `sha256`, `timestamp`, `created_at`, and `workout`, which says whether a workout was made from it (false for a file that failed to parse, or whose workout the user deleted) |
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

A workout is referred to by its file's SHA-256, never by a database id, so an archive goes into any deployment, SQLite or PostgreSQL. A route's `key` means something only inside its own archive.

Never in it: COROS tokens and the client registration (sealed under the signing key; a restore should connect again), CLI sessions, device sign-ins, and jobs.

## Reading one

`backup.Read` checks the archive as it goes, and turns away one that:

- doesn't start with a manifest of format `taiga-backup`;
- is of a newer version than this taiga reads;
- has an entry it doesn't know, an unknown field in a document, anything after a document's JSON value, or a document after the files;
- has a file whose bytes don't match its name, or that `files.json` doesn't list;
- is missing a document, or a file `files.json` lists, which is how an archive cut short shows.

`backup.Write` also checks each file against its hash as it reads it from the repo, so a damaged repo fails the export instead of producing a backup that won't restore.

One file is held in memory at a time while its hash is checked (at most 64MB; uploads stop at 32MB).

## Changing the format

The archive refers to tables by meaning, not by schema, so a migration doesn't change it on its own. A new table that holds user state nothing can rebuild needs a document here, and a new field on an existing one goes in as a version bump: `Read` turns away unknown fields, so an older taiga refuses a newer archive instead of silently dropping what it can't read.
