package models

import (
	"cmp"
	"context"
	"slices"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

// maxReasonLength is the excluded_reason column's width.
const maxReasonLength = 64

// duplicateReason names the recording that counts instead, so the copy explains itself in a list.
func duplicateReason(source string) string {
	if source == "" {
		return "duplicate of another recording"
	}
	r := "duplicate of the " + source + " recording"
	if len(r) > maxReasonLength {
		return "duplicate of another recording"
	}
	return r
}

// DetectDuplicates marks each of the user's workouts from a file that another source recorded too (workouts.FindDuplicates), and clears ones whose original is gone or no longer matches.
// A workout whose mark changes is queued for reprocessing, which is what takes it out of (or puts it back into) best efforts, routes and stats.
func (m *storeModel) DetectDuplicates(ctx context.Context, now time.Time, userToken *tokens.UserToken) error {
	return m.store.InTx(ctx, func(q db.Querier) error {
		all, err := q.ListWorkoutsByUser(ctx, userToken.ID)
		if err != nil {
			return err
		}
		var fromFiles []*Workout
		for _, w := range all {
			if w.ActivityFileID != nil {
				fromFiles = append(fromFiles, w)
			}
		}
		// By id, so a tie between equal sources goes to the first uploaded however start times shift on reprocessing.
		slices.SortFunc(fromFiles, func(a, b *Workout) int { return cmp.Compare(a.ID, b.ID) })
		recs := make([]workouts.Recording, len(fromFiles))
		for i, w := range fromFiles {
			recs[i] = workouts.Recording{Start: w.StartTime.Time, End: w.EndTime.Time, Source: w.Source}
		}
		for i, of := range workouts.FindDuplicates(recs) {
			w := fromFiles[i]
			var want *int32
			if of >= 0 {
				want = &fromFiles[of].ID
			}
			if equalID(want, w.DuplicateOf) {
				continue
			}
			if err := q.SetWorkoutDuplicateOf(ctx, &db.SetWorkoutDuplicateOfParams{ID: w.ID, UserID: userToken.ID, DuplicateOf: want}); err != nil {
				return err
			}
			if err := q.EnqueueProcessWorkout(ctx, &db.EnqueueProcessWorkoutParams{UserID: userToken.ID, WorkoutID: &w.ID, RunAt: timestamptz(now)}); err != nil {
				return err
			}
		}
		return nil
	})
}

func equalID(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
