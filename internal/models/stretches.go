package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"

	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrStretchSource means a stretch can't be cut from the workout named: it's missing, has no GPS series, or doesn't count.
var ErrStretchSource = errors.New("stretch source")

// NewStretch asks for the ground one workout covered between two distances along it.
type NewStretch struct {
	Name        string
	WorkoutID   int32
	StartMeters float64
	EndMeters   float64
}

// StretchEffort is one pass of a workout over a stretch.
type StretchEffort struct {
	WorkoutID int32 `json:"workout_id"`
	// StartTime is when the workout started; the pass begins StartOffsetSeconds into it.
	StartTime          pgtype.Timestamptz `json:"start_time"`
	StartOffsetSeconds float64            `json:"start_offset_seconds"`
	EndOffsetSeconds   float64            `json:"end_offset_seconds"`
	DurationSeconds    float64            `json:"duration_seconds"`
	StartMeters        float64            `json:"start_meters"`
	DistanceMeters     float64            `json:"distance_meters"`
	// WalkedFraction is null when the workout can't tell walking from running.
	WalkedFraction *float64 `json:"walked_fraction"`
}

// StretchSummary is a stretch with figures over its efforts (those in the window, if one was given).
type StretchSummary struct {
	ID              int32              `json:"id"`
	Name            string             `json:"name"`
	Sport           string             `json:"sport"`
	Meters          float64            `json:"meters"`
	SourceWorkoutID *int32             `json:"source_workout_id"`
	CreatedAt       pgtype.Timestamptz `json:"created_at"`
	EffortCount     int                `json:"effort_count"`
	WorkoutCount    int                `json:"workout_count"`
	BestSeconds     *float64           `json:"best_seconds"`
	BestWorkoutID   *int32             `json:"best_workout_id"`
	LatestStartTime pgtype.Timestamptz `json:"latest_start_time"`
}

// StretchDetail is a stretch with its line and its efforts, newest first.
type StretchDetail struct {
	StretchSummary
	// [lat, lon] pairs, first at the start.
	Points  [][2]float64     `json:"points"`
	Efforts []*StretchEffort `json:"efforts"`
}

// WorkoutStretchEffort is a pass as listed on the workout that made it.
type WorkoutStretchEffort struct {
	StretchID     int32   `json:"stretch_id"`
	Name          string  `json:"name"`
	StretchMeters float64 `json:"stretch_meters"`
	StretchEffort
}

func summarizeStretch(s *db.Stretch, efforts []*StretchEffort) *StretchSummary {
	out := &StretchSummary{ID: s.ID, Name: s.Name, Sport: s.Sport, Meters: s.Meters, SourceWorkoutID: s.SourceWorkoutID, CreatedAt: s.CreatedAt, EffortCount: len(efforts)}
	seen := map[int32]bool{}
	for _, e := range efforts {
		seen[e.WorkoutID] = true
		if out.BestSeconds == nil || e.DurationSeconds < *out.BestSeconds {
			out.BestSeconds, out.BestWorkoutID = &e.DurationSeconds, &e.WorkoutID
		}
		if !out.LatestStartTime.Valid || e.StartTime.Time.After(out.LatestStartTime.Time) {
			out.LatestStartTime = e.StartTime
		}
	}
	out.WorkoutCount = len(seen)
	return out
}

func (m *storeModel) Stretches(ctx context.Context, window Window, userToken *tokens.UserToken) ([]*StretchSummary, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	stretches, err := q.ListStretchesByUser(ctx, userToken.ID)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListStretchEffortsByUser(ctx, userToken.ID)
	if err != nil {
		return nil, err
	}
	efforts := map[int32][]*StretchEffort{}
	for _, r := range rows {
		if window.Contains(r.StartTime.Time) {
			efforts[r.StretchID] = append(efforts[r.StretchID], &StretchEffort{
				WorkoutID: r.WorkoutID, StartTime: r.StartTime, StartOffsetSeconds: r.StartOffsetSeconds, EndOffsetSeconds: r.EndOffsetSeconds,
				DurationSeconds: r.DurationSeconds, StartMeters: r.StartMeters, DistanceMeters: r.DistanceMeters, WalkedFraction: r.WalkedFraction,
			})
		}
	}
	out := make([]*StretchSummary, 0, len(stretches))
	for _, s := range stretches {
		out = append(out, summarizeStretch(s, efforts[s.ID]))
	}
	return out, nil
}

func (m *storeModel) Stretch(ctx context.Context, id int32, window Window, userToken *tokens.UserToken) (*StretchDetail, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	return stretchDetail(ctx, q, id, window, userToken)
}

func stretchDetail(ctx context.Context, q db.Querier, id int32, window Window, userToken *tokens.UserToken) (*StretchDetail, error) {
	s, err := q.GetStretch(ctx, &db.GetStretchParams{ID: id, UserID: userToken.ID})
	if err != nil {
		return nil, err
	}
	rows, err := q.ListStretchEffortsByStretch(ctx, &db.ListStretchEffortsByStretchParams{StretchID: id, UserID: userToken.ID})
	if err != nil {
		return nil, err
	}
	efforts := []*StretchEffort{}
	for _, r := range rows {
		if window.Contains(r.StartTime.Time) {
			efforts = append(efforts, &StretchEffort{
				WorkoutID: r.WorkoutID, StartTime: r.StartTime, StartOffsetSeconds: r.StartOffsetSeconds, EndOffsetSeconds: r.EndOffsetSeconds,
				DurationSeconds: r.DurationSeconds, StartMeters: r.StartMeters, DistanceMeters: r.DistanceMeters, WalkedFraction: r.WalkedFraction,
			})
		}
	}
	detail := &StretchDetail{StretchSummary: *summarizeStretch(s, efforts), Efforts: efforts}
	if err := json.Unmarshal(s.Points, &detail.Points); err != nil {
		return nil, fmt.Errorf("decoding stretch %d: %w", id, err)
	}
	return detail, nil
}

// CreateStretch cuts a stretch from one of the user's workouts and times every workout of that sport over it before returning.
func (m *storeModel) CreateStretch(ctx context.Context, req NewStretch, userToken *tokens.UserToken) (*StretchDetail, error) {
	source, err := m.Workout(ctx, req.WorkoutID, userToken)
	if IsNotFound(err) {
		return nil, fmt.Errorf("%w: no workout %d", ErrStretchSource, req.WorkoutID)
	}
	if err != nil {
		return nil, err
	}
	if summary := (workouts.RunSummary{Sport: source.Sport, SubSport: source.SubSport, ExcludedReason: source.ExcludedReason}); !summary.Scored() {
		return nil, fmt.Errorf("%w: workout %d isn't an outdoor run or walk that counts", ErrStretchSource, req.WorkoutID)
	}

	var detail *StretchDetail
	err = m.store.InTx(ctx, func(q db.Querier) error {
		series, err := storedSeries(ctx, q, req.WorkoutID, userToken)
		if IsNotFound(err) {
			return fmt.Errorf("%w: workout %d has no recorded series", ErrStretchSource, req.WorkoutID)
		}
		if err != nil {
			return err
		}
		stretch, err := workouts.BuildStretch(series, req.StartMeters, req.EndMeters)
		if err != nil {
			return err
		}
		points, err := json.Marshal(stretch.Points)
		if err != nil {
			return err
		}
		row, err := q.CreateStretch(ctx, &db.CreateStretchParams{
			UserID: userToken.ID, Name: req.Name, Sport: source.Sport, SourceWorkoutID: &source.ID, Meters: stretch.Meters, Points: points,
		})
		if err != nil {
			return err
		}
		// The stored tracks rule most workouts out without reading their series.
		tracks, err := q.ListWorkoutTracksByUserAndSport(ctx, &db.ListWorkoutTracksByUserAndSportParams{UserID: userToken.ID, Sport: source.Sport})
		if err != nil {
			return err
		}
		// Processing gives efforts only to workouts with a track, so a source without one would be a stretch its own workout isn't on.
		if !slices.ContainsFunc(tracks, func(t *db.ListWorkoutTracksByUserAndSportRow) bool { return t.WorkoutID == req.WorkoutID }) {
			return fmt.Errorf("%w: workout %d has too little GPS to match against, or is a copy of another", ErrStretchSource, req.WorkoutID)
		}
		for _, t := range tracks {
			var track workouts.Track
			if err := json.Unmarshal(t.Track, &track); err != nil {
				return fmt.Errorf("decoding track of workout %d: %w", t.WorkoutID, err)
			}
			if !stretch.MightPass(&track) {
				continue
			}
			candidate := series
			if t.WorkoutID != req.WorkoutID {
				candidate, err = storedSeries(ctx, q, t.WorkoutID, userToken)
				if IsNotFound(err) {
					continue
				}
				if err != nil {
					return err
				}
			}
			if err := addStretchEfforts(ctx, q, row.ID, t.WorkoutID, stretch.Passes(candidate), userToken); err != nil {
				return err
			}
		}
		detail, err = stretchDetail(ctx, q, row.ID, Window{}, userToken)
		return err
	})
	return detail, err
}

func storedSeries(ctx context.Context, q db.Querier, workoutID int32, userToken *tokens.UserToken) (*workouts.Series, error) {
	raw, err := q.GetWorkoutSeries(ctx, &db.GetWorkoutSeriesParams{WorkoutID: workoutID, UserID: userToken.ID})
	if err != nil {
		return nil, err
	}
	var series workouts.Series
	if err := json.Unmarshal(raw, &series); err != nil {
		return nil, fmt.Errorf("decoding series of workout %d: %w", workoutID, err)
	}
	return &series, nil
}

func addStretchEfforts(ctx context.Context, q db.Querier, stretchID, workoutID int32, passes []workouts.Pass, userToken *tokens.UserToken) error {
	tenth := func(v float64) float64 { return math.Round(v*10) / 10 }
	for _, p := range passes {
		var walked *float64
		if !math.IsNaN(p.WalkedFraction) {
			w := math.Round(p.WalkedFraction*1000) / 1000
			walked = &w
		}
		err := q.CreateStretchEffort(ctx, &db.CreateStretchEffortParams{
			StretchID: stretchID, WorkoutID: workoutID, UserID: userToken.ID,
			StartOffsetSeconds: tenth(p.StartSeconds), EndOffsetSeconds: tenth(p.EndSeconds), DurationSeconds: tenth(p.EndSeconds - p.StartSeconds),
			StartMeters: tenth(p.StartMeters), DistanceMeters: tenth(p.EndMeters - p.StartMeters), WalkedFraction: walked,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// storeStretchEfforts replaces a workout's passes over the user's stretches; a workout that doesn't count, or has too little GPS for a track, keeps none.
func storeStretchEfforts(ctx context.Context, q db.Querier, workoutID int32, run *workouts.Run, series *workouts.Series, userToken *tokens.UserToken) error {
	if err := q.DeleteStretchEffortsByWorkout(ctx, &db.DeleteStretchEffortsByWorkoutParams{WorkoutID: workoutID, UserID: userToken.ID}); err != nil {
		return err
	}
	// The same workouts a new stretch is matched against: those with a stored track.
	if !run.Summary.Scored() || workouts.BuildTrack(run) == nil {
		return nil
	}
	summary := &run.Summary
	stretches, err := q.ListStretchesByUserAndSport(ctx, &db.ListStretchesByUserAndSportParams{UserID: userToken.ID, Sport: summary.Sport})
	if err != nil {
		return err
	}
	for _, s := range stretches {
		stretch := workouts.Stretch{Meters: s.Meters}
		if err := json.Unmarshal(s.Points, &stretch.Points); err != nil {
			return fmt.Errorf("decoding stretch %d: %w", s.ID, err)
		}
		if err := addStretchEfforts(ctx, q, s.ID, workoutID, stretch.Passes(series), userToken); err != nil {
			return err
		}
	}
	return nil
}

func (m *storeModel) RenameStretch(ctx context.Context, id int32, name string, userToken *tokens.UserToken) (*StretchDetail, error) {
	var detail *StretchDetail
	err := m.store.InTx(ctx, func(q db.Querier) error {
		if _, err := q.RenameStretch(ctx, &db.RenameStretchParams{ID: id, UserID: userToken.ID, Name: name}); err != nil {
			return err
		}
		var err error
		detail, err = stretchDetail(ctx, q, id, Window{}, userToken)
		return err
	})
	return detail, err
}

func (m *storeModel) DeleteStretch(ctx context.Context, id int32, userToken *tokens.UserToken) error {
	return m.store.InTx(ctx, func(q db.Querier) error {
		if _, err := q.GetStretch(ctx, &db.GetStretchParams{ID: id, UserID: userToken.ID}); err != nil {
			return err
		}
		if err := q.DeleteStretchEffortsByStretch(ctx, &db.DeleteStretchEffortsByStretchParams{StretchID: id, UserID: userToken.ID}); err != nil {
			return err
		}
		return q.DeleteStretch(ctx, &db.DeleteStretchParams{ID: id, UserID: userToken.ID})
	})
}

func (m *storeModel) WorkoutStretchEfforts(ctx context.Context, workoutID int32, userToken *tokens.UserToken) ([]*WorkoutStretchEffort, error) {
	workout, err := m.Workout(ctx, workoutID, userToken)
	if err != nil {
		return nil, err
	}
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	rows, err := q.ListStretchEffortsByWorkout(ctx, &db.ListStretchEffortsByWorkoutParams{WorkoutID: workoutID, UserID: userToken.ID})
	if err != nil {
		return nil, err
	}
	out := make([]*WorkoutStretchEffort, 0, len(rows))
	for _, r := range rows {
		out = append(out, &WorkoutStretchEffort{StretchID: r.StretchID, Name: r.Name, StretchMeters: r.Meters, StretchEffort: StretchEffort{
			WorkoutID: r.WorkoutID, StartTime: workout.StartTime, StartOffsetSeconds: r.StartOffsetSeconds, EndOffsetSeconds: r.EndOffsetSeconds,
			DurationSeconds: r.DurationSeconds, StartMeters: r.StartMeters, DistanceMeters: r.DistanceMeters, WalkedFraction: r.WalkedFraction,
		}})
	}
	return out, nil
}
