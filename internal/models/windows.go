package models

import (
	"cmp"
	"context"
	"log"
	"math"
	"slices"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"github.com/jackc/pgx/v5/pgtype"
)

// Window limits a listing to workouts starting in [From, To); a zero end is open.
type Window struct {
	From, To time.Time
}

// Far-off bounds stand in for an open end, so one query serves both; UTC because SQLite compares timestamps as text.
var (
	windowStart = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	windowEnd   = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
)

func (w Window) Contains(t time.Time) bool {
	return (w.From.IsZero() || !t.Before(w.From)) && (w.To.IsZero() || t.Before(w.To))
}

func (w Window) bounds() (from, to pgtype.Timestamptz) {
	f, t := windowStart, windowEnd
	if !w.From.IsZero() {
		f = w.From.UTC()
	}
	if !w.To.IsZero() {
		t = w.To.UTC()
	}
	return pgtype.Timestamptz{Time: f, Valid: true}, pgtype.Timestamptz{Time: t, Valid: true}
}

// WorkoutQuery pages through a window of the workout list, newest first; a zero Limit returns the rest of the window.
type WorkoutQuery struct {
	Window
	Limit, Offset int32
}

func (m *storeModel) Workouts(ctx context.Context, query WorkoutQuery, userToken *tokens.UserToken) ([]*Workout, error) {
	querier, err := m.store.GetQuerier()
	if err != nil {
		log.Printf("could not get a querier: %v", err)
		return nil, err
	}
	from, to := query.bounds()
	limit := query.Limit
	if limit == 0 {
		limit = math.MaxInt32
	}
	workouts, err := querier.ListWorkoutsByUserInWindow(ctx, &db.ListWorkoutsByUserInWindowParams{
		UserID:     userToken.ID,
		WindowFrom: from,
		WindowTo:   to,
		PageLimit:  limit,
		PageOffset: query.Offset,
	})
	if err != nil {
		log.Printf("Could not get workouts: %v", err)
		return nil, err
	}
	return workouts, nil
}

// LongestWorkout is the workout that went farthest, or lasted longest, of one sport's counted workouts in a window.
type LongestWorkout struct {
	WorkoutID     int32     `json:"workout_id"`
	WorkoutName   string    `json:"workout_name"`
	StartTime     time.Time `json:"start_time"`
	DistanceMiles *float64  `json:"distance_miles"`
	TimeSeconds   *float64  `json:"time_seconds"`
}

// SportTotals sums one sport's counted workouts in a window.
type SportTotals struct {
	Sport         string  `json:"sport"`
	Workouts      int64   `json:"workouts"`
	DistanceMiles float64 `json:"distance_miles"`
	TimeSeconds   float64 `json:"time_seconds"`
	// The two can be different workouts; each is null when no workout states that figure.
	LongestByDistance *LongestWorkout `json:"longest_by_distance"`
	LongestByTime     *LongestWorkout `json:"longest_by_time"`
}

// add counts one workout; a tie for longest stays with whichever add saw first.
func (s *SportTotals) add(r *db.ListCountedWorkoutsInWindowRow) {
	s.Workouts++
	longest := func() *LongestWorkout {
		return &LongestWorkout{WorkoutID: r.ID, WorkoutName: r.Name, StartTime: r.StartTime.Time, DistanceMiles: r.DistanceMiles, TimeSeconds: r.TimeSeconds}
	}
	if d := r.DistanceMiles; d != nil {
		s.DistanceMiles += *d
		if *d > 0 && (s.LongestByDistance == nil || *d > *s.LongestByDistance.DistanceMiles) {
			s.LongestByDistance = longest()
		}
	}
	if t := r.TimeSeconds; t != nil {
		s.TimeSeconds += *t
		if *t > 0 && (s.LongestByTime == nil || *t > *s.LongestByTime.TimeSeconds) {
			s.LongestByTime = longest()
		}
	}
}

// sumBySport totals rows per sport, most workouts first.
func sumBySport(rows []*db.ListCountedWorkoutsInWindowRow) []*SportTotals {
	sports := []*SportTotals{}
	for _, r := range rows {
		i := slices.IndexFunc(sports, func(s *SportTotals) bool { return s.Sport == r.Sport })
		if i < 0 {
			sports = append(sports, &SportTotals{Sport: r.Sport})
			i = len(sports) - 1
		}
		sports[i].add(r)
	}
	slices.SortFunc(sports, func(a, b *SportTotals) int {
		return cmp.Or(cmp.Compare(b.Workouts, a.Workouts), cmp.Compare(a.Sport, b.Sport))
	})
	return sports
}

type WorkoutTotals struct {
	Sports []*SportTotals `json:"sports"`
	// FirstStartTime is the earliest workout listed in the window, counted or not, so a client can offer the years there are; null when there's none.
	FirstStartTime *time.Time `json:"first_start_time"`
}

func (m *storeModel) WorkoutTotals(ctx context.Context, window Window, userToken *tokens.UserToken) (*WorkoutTotals, error) {
	querier, err := m.store.GetQuerier()
	if err != nil {
		log.Printf("could not get a querier: %v", err)
		return nil, err
	}
	from, to := window.bounds()
	rows, err := querier.ListCountedWorkoutsInWindow(ctx, &db.ListCountedWorkoutsInWindowParams{UserID: userToken.ID, WindowFrom: from, WindowTo: to})
	if err != nil {
		log.Printf("Could not total workouts: %v", err)
		return nil, err
	}
	totals := &WorkoutTotals{Sports: sumBySport(rows)}
	first, err := querier.FirstWorkoutStartInWindow(ctx, &db.FirstWorkoutStartInWindowParams{UserID: userToken.ID, WindowFrom: from, WindowTo: to})
	switch {
	case IsNotFound(err):
	case err != nil:
		log.Printf("Could not find the first workout: %v", err)
		return nil, err
	case first.Valid:
		totals.FirstStartTime = &first.Time
	}
	return totals, nil
}
