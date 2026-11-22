package models

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/coros"
	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

const (
	// sourceHistoryListsPerSync caps how many lists one sync asks for on its way back through the source's history; the rest wait an hour.
	sourceHistoryListsPerSync = 12
	// The source's list and the file it gave state the same start; this is slack for rounding.
	sourceMatchTolerance = 5 * time.Second
	// More workouts than a window of history can hold.
	sourceHistoryWorkouts = 5000
)

// sourceRecorders is what the files a source's devices record call themselves (workouts.source), as a LIKE pattern: only those can be the source's activities.
var sourceRecorders = map[string]string{coros.Kind: "COROS%"}

// matchSourceHistory ties workouts the user uploaded by hand to the source's activities, by their starts, so the source's own figures can be asked for them; no file is fetched.
// It takes the newest workout not yet looked for, lists the source's month up to it, and ties or marks every workout in that month, so each sync works further back and a workout uploaded later is found too.
// It returns how many it tied and, as words for the connection, why it stopped short; only a failure that ends the sync (the grant gone) is an error.
func (m *storeModel) matchSourceHistory(ctx context.Context, conn *SourceConnection, src ActivitySource, now time.Time, userToken *tokens.UserToken) (int, string, error) {
	recorder, ok := sourceRecorders[conn.Kind]
	if !ok {
		return 0, "", nil
	}
	q, err := m.store.GetQuerier()
	if err != nil {
		return 0, "", err
	}
	waiting := func(since time.Time, atMost int32) ([]*db.ListWorkoutsToMatchToSourceRow, error) {
		return q.ListWorkoutsToMatchToSource(ctx, &db.ListWorkoutsToMatchToSourceParams{UserID: userToken.ID, Kind: conn.Kind, SourceLike: recorder, Since: timestamptz(since), AtMost: atMost})
	}
	stopped := func(err error) string {
		return fmt.Sprintf("the activities came in, but COROS's history couldn't be searched for older workouts: %v", err)
	}

	matched, problem := 0, ""
	for lists := 0; lists < sourceHistoryListsPerSync; {
		newest, err := waiting(time.Unix(0, 0), 1)
		if err != nil {
			if ctx.Err() != nil {
				return matched, stopped(ctx.Err()), nil
			}
			return matched, "", err
		}
		if len(newest) == 0 {
			break
		}
		latest := newest[0].StartTime.Time
		to := latest.Add(sourceMatchTolerance)
		width := sourceWindow
		activities, err := src.List(ctx, to.Add(-width), to)
		lists++
		// More in the window than one list holds: a shorter one, which still ends at the workout being looked for.
		for errors.Is(err, coros.ErrTooMany) && width > minSourceWindow {
			// Out of lists with nothing learnt: the window is left as it was for the next sync.
			if lists >= sourceHistoryListsPerSync {
				return matched, problem, nil
			}
			width /= 2
			activities, err = src.List(ctx, to.Add(-width), to)
			lists++
		}
		switch {
		case errors.Is(err, coros.ErrAuthorization):
			return matched, "", err
		// A day too full to list: its workouts are marked as looked for, or every sync would stop here.
		case errors.Is(err, coros.ErrTooMany):
			problem = stopped(err)
			activities = nil
		case err != nil:
			log.Printf("sources: listing %s's history for user %d: %v", conn.Kind, userToken.ID, err)
			return matched, stopped(err), nil
		}

		tied := 0
		err = m.store.InTx(ctx, func(q db.Querier) error {
			tied = 0
			// Listed again inside the transaction, since a workout can be deleted while COROS answers.
			// One right at the window's far edge waits for the next window, where an activity a few seconds before it is listed too.
			candidates, err := q.ListWorkoutsToMatchToSource(ctx, &db.ListWorkoutsToMatchToSourceParams{UserID: userToken.ID, Kind: conn.Kind, SourceLike: recorder, Since: timestamptz(to.Add(sourceMatchTolerance - width)), AtMost: sourceHistoryWorkouts})
			if err != nil {
				return err
			}
			// One uploaded while COROS answered can start after the window, and marking it would be saying it was looked for.
			candidates = slices.DeleteFunc(candidates, func(w *db.ListWorkoutsToMatchToSourceRow) bool { return w.StartTime.Time.After(latest) })
			taken := make(map[int32]bool, len(candidates))
			for _, a := range activities {
				if a.Start.IsZero() {
					continue
				}
				if _, err := q.GetSourceActivity(ctx, &db.GetSourceActivityParams{UserID: userToken.ID, Kind: conn.Kind, ExternalID: a.ID}); err == nil {
					continue
				} else if !IsNotFound(err) {
					return err
				}
				var closest *db.ListWorkoutsToMatchToSourceRow
				for _, w := range candidates {
					off := w.StartTime.Time.Sub(a.Start).Abs()
					if taken[w.ID] || off > sourceMatchTolerance {
						continue
					}
					if closest == nil || off < closest.StartTime.Time.Sub(a.Start).Abs() {
						closest = w
					}
				}
				if closest == nil {
					continue
				}
				taken[closest.ID] = true
				sportType := int32(a.SportType)
				// No files: the one the workout came from was uploaded, not fetched.
				if err := q.CreateSourceActivity(ctx, &db.CreateSourceActivityParams{
					UserID:         userToken.ID,
					Kind:           conn.Kind,
					ExternalID:     a.ID,
					ActivityFileID: closest.ActivityFileID,
					StartTime:      timestamptz(a.Start),
					FetchedAt:      timestamptz(now),
					SportType:      &sportType,
				}); err != nil {
					return err
				}
				tied++
			}
			for _, w := range candidates {
				if taken[w.ID] {
					continue
				}
				if err := q.MarkSourceMatchTried(ctx, &db.MarkSourceMatchTriedParams{WorkoutID: w.ID, Kind: conn.Kind, UserID: userToken.ID, TriedAt: timestamptz(now)}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			// The sync's deadline can pass in a write as well as in a call, and the activities are in either way.
			if ctx.Err() != nil {
				return matched, stopped(ctx.Err()), nil
			}
			return matched, "", err
		}
		matched += tied
	}
	return matched, problem, nil
}
