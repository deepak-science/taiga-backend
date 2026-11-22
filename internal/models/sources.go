package models

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/coros"
	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

type (
	SourceClient     = db.SourceClient
	SourceConnection = db.SourceConnection
)

const (
	JobSyncSource = "sync_source"

	// SourceDailyFiles is how many files a day a sync takes from a source. COROS allows 50; the rest is left for the user's own use of the same account.
	SourceDailyFiles = 45
	// A new connection starts a week back, which is what COROS lists by default; older history comes from an export.
	sourceLookback = 7 * 24 * time.Hour
	// Each sync looks again at the last day it covered, for an activity the watch uploaded late.
	sourceOverlap = 24 * time.Hour
	// A connection that hasn't synced in a long while catches up a month at a time, so no one list is too long for the source to give whole.
	sourceWindow = 30 * 24 * time.Hour
	// The shortest a window is cut to when a list comes back full.
	minSourceWindow = 24 * time.Hour
)

// ActivitySource is a signed-in source of activities; a *coros.Session is one.
type ActivitySource interface {
	// List returns the activities starting between from and to, oldest first.
	List(ctx context.Context, from, to time.Time) ([]coros.Activity, error)
	// Files returns the activity's files, or coros.ErrInProgress or coros.ErrDailyLimit when there are none to have yet.
	Files(ctx context.Context, a coros.Activity) ([][]byte, error)
	// Detail returns the source's own figures for the activity.
	Detail(ctx context.Context, a coros.Activity) (*coros.Detail, error)
}

// SyncResult is what one sync did.
type SyncResult struct {
	// Listed is how many activities the source named, ones already dealt with included.
	Listed int
	// Imported is how many became new workouts.
	Imported int
	// AlreadyHad is how many turned out to be files the user had uploaded themselves.
	AlreadyHad int
	// Unreadable is how many files were stored but couldn't be made into a workout.
	Unreadable int
	// Waiting is how many were put off to the next sync: still being recorded, or the first one past the daily limit.
	Waiting int
	// LimitReached says the sync stopped at the source's daily limit on files.
	LimitReached bool
	// Detailed is how many workouts got the source's own figures.
	Detailed int
	// DetailProblem is why some didn't, which the connection shows as its error though the sync itself finished; empty when all did.
	DetailProblem string
}

func (m *storeModel) SourceClient(ctx context.Context, kind, issuer, redirectURI string) (*SourceClient, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	return q.GetSourceClient(ctx, &db.GetSourceClientParams{Kind: kind, Issuer: issuer, RedirectUri: redirectURI})
}

func (m *storeModel) SourceClientByID(ctx context.Context, id int32) (*SourceClient, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	return q.GetSourceClientByID(ctx, id)
}

// SaveSourceClient keeps a registration, or returns the one already kept for the same server and callback.
func (m *storeModel) SaveSourceClient(ctx context.Context, kind, issuer, redirectURI, registration string) (*SourceClient, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	return q.CreateSourceClient(ctx, &db.CreateSourceClientParams{Kind: kind, Issuer: issuer, RedirectUri: redirectURI, Registration: registration})
}

// ReplaceSourceClient puts a new registration in place of was, one that can no longer be read.
// When another request has replaced it meanwhile, that one is kept and returned: a sign-in already under way was started with it.
func (m *storeModel) ReplaceSourceClient(ctx context.Context, id int32, was, registration string) (*SourceClient, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	row, err := q.ReplaceSourceClient(ctx, &db.ReplaceSourceClientParams{ID: id, Registration: registration, Was: was})
	if IsNotFound(err) {
		return q.GetSourceClientByID(ctx, id)
	}
	return row, err
}

// ConnectSource keeps the user's tokens for a source and queues its first sync.
// A user already connected gets the new tokens and carries on from where their syncs had got to.
func (m *storeModel) ConnectSource(ctx context.Context, kind string, clientID int32, credentials string, now time.Time, userToken *tokens.UserToken) (*SourceConnection, error) {
	var conn *SourceConnection
	err := m.store.InTx(ctx, func(q db.Querier) error {
		var err error
		conn, err = q.UpsertSourceConnection(ctx, &db.UpsertSourceConnectionParams{
			UserID:      userToken.ID,
			Kind:        kind,
			ClientID:    clientID,
			Credentials: credentials,
			// The first sync looks a day behind this like any other, which makes the week.
			SyncedThrough: timestamptz(now.Add(-sourceLookback + sourceOverlap)),
		})
		if err != nil {
			return err
		}
		// A sync that died on the old tokens would keep the sweep from queueing another.
		if err := q.DeleteSyncSourceJobs(ctx, userToken.ID); err != nil {
			return err
		}
		return q.EnqueueSyncSource(ctx, &db.EnqueueSyncSourceParams{UserID: userToken.ID, RunAt: timestamptz(now)})
	})
	return conn, err
}

// SourceConnectionInfo is a connection with how many of the source's activities have been dealt with.
type SourceConnectionInfo struct {
	*SourceConnection
	Activities int64
}

func (m *storeModel) SourceConnections(ctx context.Context, userToken *tokens.UserToken) ([]SourceConnectionInfo, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	conns, err := q.ListSourceConnections(ctx, userToken.ID)
	if err != nil {
		return nil, err
	}
	out := make([]SourceConnectionInfo, 0, len(conns))
	for _, conn := range conns {
		n, err := q.CountSourceActivities(ctx, &db.CountSourceActivitiesParams{UserID: userToken.ID, Kind: conn.Kind})
		if err != nil {
			return nil, err
		}
		out = append(out, SourceConnectionInfo{SourceConnection: conn, Activities: n})
	}
	return out, nil
}

func (m *storeModel) SourceConnection(ctx context.Context, id int32, userToken *tokens.UserToken) (*SourceConnection, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	return q.GetSourceConnection(ctx, &db.GetSourceConnectionParams{ID: id, UserID: userToken.ID})
}

func (m *storeModel) SourceConnectionByKind(ctx context.Context, kind string, userToken *tokens.UserToken) (*SourceConnection, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	return q.GetSourceConnectionByKind(ctx, &db.GetSourceConnectionByKindParams{UserID: userToken.ID, Kind: kind})
}

// DisconnectSource forgets the connection and its tokens. What it imported stays, and so does the record of what it dealt with.
func (m *storeModel) DisconnectSource(ctx context.Context, id int32, userToken *tokens.UserToken) error {
	return m.store.InTx(ctx, func(q db.Querier) error {
		n, err := q.DeleteSourceConnection(ctx, &db.DeleteSourceConnectionParams{ID: id, UserID: userToken.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return errNoConnection
		}
		return q.DeleteSyncSourceJobs(ctx, userToken.ID)
	})
}

var errNoConnection = fmt.Errorf("no such source connection: %w", sql.ErrNoRows)

func (m *storeModel) SetSourceCredentials(ctx context.Context, id int32, credentials string, userToken *tokens.UserToken) error {
	q, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	return q.SetSourceCredentials(ctx, &db.SetSourceCredentialsParams{ID: id, UserID: userToken.ID, Credentials: credentials})
}

// FailSourceSync records why a sync failed; needsReconnect stops further syncs until the user connects again.
func (m *storeModel) FailSourceSync(ctx context.Context, id int32, reason string, needsReconnect bool, userToken *tokens.UserToken) error {
	q, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	return q.FailSourceSync(ctx, &db.FailSourceSyncParams{ID: id, UserID: userToken.ID, LastError: reason, NeedsReconnect: needsReconnect})
}

// EnqueueSyncSource queues a sync of the user's sources now, in place of one waiting on a backoff or dead; one already running is left to finish.
func (m *storeModel) EnqueueSyncSource(ctx context.Context, now time.Time, userToken *tokens.UserToken) error {
	return m.store.InTx(ctx, func(q db.Querier) error {
		// A dead sync would keep the sweep from queueing another, and asking is how the user says to try again.
		if err := q.DeleteSyncSourceJobs(ctx, userToken.ID); err != nil {
			return err
		}
		return q.EnqueueSyncSource(ctx, &db.EnqueueSyncSourceParams{UserID: userToken.ID, RunAt: timestamptz(now)})
	})
}

// EnqueueSourceSyncsDue queues a sync for every connection that can have one.
func (m *storeModel) EnqueueSourceSyncsDue(ctx context.Context, now time.Time) (int64, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return 0, err
	}
	return q.EnqueueSourceSyncsDue(ctx, timestamptz(now))
}

// SyncSource brings in the activities the source has that the connection hasn't dealt with: each one's file goes through the same path as an upload, so one the user already uploaded is recognised by its content.
// It stops at SourceDailyFiles a day and picks up from there next time; an error leaves what was already brought in.
func (m *storeModel) SyncSource(ctx context.Context, conn *SourceConnection, src ActivitySource, fileRepo filerepo.FileRepo, now time.Time, userToken *tokens.UserToken) (res *SyncResult, err error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	day := timestamptz(now.Add(-24 * time.Hour))
	if err := q.DeleteSourceFileRequestsBefore(ctx, &db.DeleteSourceFileRequestsBeforeParams{UserID: userToken.ID, Kind: conn.Kind, RequestedAt: day}); err != nil {
		return nil, err
	}
	used, err := q.CountSourceFilesSince(ctx, &db.CountSourceFilesSinceParams{UserID: userToken.ID, Kind: conn.Kind, RequestedAt: day})
	if err != nil {
		return nil, err
	}

	res = &SyncResult{}
	// As after an upload: the new runs may belong on routes, or be copies of ones another source recorded.
	// However the sync ends, since the next one skips what this one brought in and would see nothing new.
	defer func() {
		if res.Imported > 0 {
			err = errors.Join(err, m.EnqueueDetectRoutes(context.WithoutCancel(ctx), now, userToken))
		}
	}()
	// The earliest activity left for next time, which is as far as the connection can say it has synced.
	var held time.Time
	hold := func(a coros.Activity) {
		res.Waiting++
		at := a.Start
		if at.IsZero() {
			// Where the cursor already is: anything earlier would have each sync look further back than the last.
			at = conn.SyncedThrough.Time
		}
		if held.IsZero() || at.Before(held) {
			held = at
		}
	}

	from := conn.SyncedThrough.Time.Add(-sourceOverlap)
	width := sourceWindow
windows:
	for start := from; start.Before(now); {
		end := start.Add(width)
		if end.After(now) {
			end = now
		}
		activities, err := src.List(ctx, start, end)
		// More in the window than one list holds: the same stretch again in halves, down to a day.
		if errors.Is(err, coros.ErrTooMany) && width > minSourceWindow {
			width /= 2
			continue
		}
		if err != nil {
			return res, err
		}
		for _, a := range activities {
			res.Listed++
			if _, err := q.GetSourceActivity(ctx, &db.GetSourceActivityParams{UserID: userToken.ID, Kind: conn.Kind, ExternalID: a.ID}); err == nil {
				continue
			} else if !IsNotFound(err) {
				return res, err
			}
			if int(used) >= SourceDailyFiles {
				res.LimitReached = true
			}
			var files [][]byte
			if !res.LimitReached {
				files, err = src.Files(ctx, a)
				// Counted as soon as COROS has issued the links, whatever becomes of the download or the import.
				spent := len(files)
				var after *coros.SpentError
				if errors.As(err, &after) {
					spent = after.Files
				}
				if spent > 0 {
					used += int32(spent)
					if recordErr := q.RecordSourceFileRequest(context.WithoutCancel(ctx), &db.RecordSourceFileRequestParams{UserID: userToken.ID, Kind: conn.Kind, ExternalID: a.ID, Files: int32(spent), RequestedAt: timestamptz(now)}); recordErr != nil {
						return res, errors.Join(err, recordErr)
					}
				}
				switch {
				case errors.Is(err, coros.ErrInProgress):
					hold(a)
					continue
				case errors.Is(err, coros.ErrDailyLimit):
					res.LimitReached = true
				case err != nil:
					return res, err
				}
			}
			if res.LimitReached {
				// Oldest first, so this is the earliest one left; the rest wait behind it.
				hold(a)
				break windows
			}

			var fileID *int32
			for _, content := range files {
				file, _, err := m.AddActivityFile(ctx, bytes.NewReader(content), now, userToken, fileRepo)
				if err != nil {
					return res, err
				}
				if fileID == nil {
					fileID = &file.ID
				}
				_, created, err := m.CreateWorkoutFromActivityFile(ctx, file.ID, userToken, fileRepo)
				switch {
				// Kept as a file with no workout, where GET /activity_files?without_workout=true shows it.
				case errors.Is(err, workouts.ErrUnreadableFile), errors.Is(err, workouts.ErrUnsupportedActivity):
					log.Printf("sources: %s activity %s for user %d is not a workout taiga can read: %v", conn.Kind, a.ID, userToken.ID, err)
					res.Unreadable++
				case err != nil:
					return res, err
				case created:
					res.Imported++
				default:
					res.AlreadyHad++
				}
			}
			startTime := timestamptz(a.Start)
			startTime.Valid = !a.Start.IsZero()
			sportType := int32(a.SportType)
			if err := q.CreateSourceActivity(ctx, &db.CreateSourceActivityParams{
				UserID:         userToken.ID,
				Kind:           conn.Kind,
				ExternalID:     a.ID,
				ActivityFileID: fileID,
				StartTime:      startTime,
				Files:          int32(len(files)),
				FetchedAt:      timestamptz(now),
				SportType:      &sportType,
			}); err != nil {
				return res, err
			}
		}
		start = end
	}

	through := now
	if !held.IsZero() {
		through = held
	}
	if err := q.FinishSourceSync(ctx, &db.FinishSourceSyncParams{ID: conn.ID, UserID: userToken.ID, SyncedThrough: timestamptz(through), LastSyncedAt: timestamptz(now)}); err != nil {
		return res, err
	}

	// After the cursor is saved: COROS's figures are extra, and failing to get them shouldn't have the next sync list all this again.
	res.Detailed, res.DetailProblem, err = m.fetchSourceDetails(ctx, conn, src, now, userToken)
	if err != nil {
		return res, err
	}
	if res.DetailProblem != "" {
		// Said on the connection without failing the job: a job that dies stops the hourly syncs, and the activities themselves are coming in.
		if err := q.FailSourceSync(ctx, &db.FailSourceSyncParams{ID: conn.ID, UserID: userToken.ID, LastError: res.DetailProblem}); err != nil {
			return res, err
		}
	}
	return res, nil
}
