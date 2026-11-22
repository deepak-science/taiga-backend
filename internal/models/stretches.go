package models

import (
	"cmp"
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
	WorkoutID   int32  `json:"workout_id"`
	WorkoutName string `json:"workout_name"`
	// StartTime is when the workout started; the pass begins StartOffsetSeconds into it.
	StartTime          pgtype.Timestamptz `json:"start_time"`
	StartOffsetSeconds float64            `json:"start_offset_seconds"`
	EndOffsetSeconds   float64            `json:"end_offset_seconds"`
	DurationSeconds    float64            `json:"duration_seconds"`
	StartMeters        float64            `json:"start_meters"`
	DistanceMeters     float64            `json:"distance_meters"`
	// WalkedFraction is null when the workout can't tell walking from running.
	WalkedFraction *float64 `json:"walked_fraction"`
	// RouteID is the route the workout is on; null when it's on none.
	RouteID *int32 `json:"route_id"`
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
	// Routes are the routes those efforts were run on, most efforts first.
	Routes []*StretchRoute `json:"routes"`
}

// StretchRoute is a route some of a stretch's efforts were run on, with how many.
type StretchRoute struct {
	ID   int32   `json:"id"`
	Name *string `json:"name"`
	// DistanceMiles is the representative run's, as a route's own is.
	DistanceMiles *float64 `json:"distance_miles"`
	EffortCount   int      `json:"effort_count"`
	WorkoutCount  int      `json:"workout_count"`
}

// RouteStretch is a stretch some of a route's runs pass, with figures over those runs alone.
type RouteStretch struct {
	StretchSummary
	// RunCount is how many runs the route has (in the window), to set WorkoutCount against.
	RunCount int `json:"run_count"`
	// MissedWorkoutIDs are the route's runs with no pass over it, newest first.
	MissedWorkoutIDs []int32 `json:"missed_workout_ids"`
}

// WorkoutStretchEffort is a pass as listed on the workout that made it.
type WorkoutStretchEffort struct {
	StretchID     int32   `json:"stretch_id"`
	Name          string  `json:"name"`
	StretchMeters float64 `json:"stretch_meters"`
	// BestSeconds is the fastest pass any workout has made over the stretch, and Rank where this one stands among all EffortCount of them, 1 the fastest.
	BestSeconds float64 `json:"best_seconds"`
	Rank        int     `json:"rank"`
	EffortCount int     `json:"effort_count"`
	StretchEffort
}

// workoutRouteIDs is the route each of the user's routed workouts is on.
func workoutRouteIDs(ctx context.Context, q db.Querier, userToken *tokens.UserToken) (map[int32]int32, error) {
	memberships, err := q.ListWorkoutRoutesByUser(ctx, userToken.ID)
	if err != nil {
		return nil, err
	}
	routeOf := make(map[int32]int32, len(memberships))
	for _, wr := range memberships {
		routeOf[wr.WorkoutID] = wr.RouteID
	}
	return routeOf, nil
}

// routeIDOf is the workout's route as an effort carries it.
func routeIDOf(routeOf map[int32]int32, workoutID int32) *int32 {
	if id, ok := routeOf[workoutID]; ok {
		return &id
	}
	return nil
}

// stretchRoutes counts the efforts by the route they were run on.
func stretchRoutes(ctx context.Context, q db.Querier, efforts []*StretchEffort, userToken *tokens.UserToken) ([]*StretchRoute, error) {
	out := []*StretchRoute{}
	if !slices.ContainsFunc(efforts, func(e *StretchEffort) bool { return e.RouteID != nil }) {
		return out, nil
	}
	names, err := q.ListRouteNamesByUser(ctx, userToken.ID)
	if err != nil {
		return nil, err
	}
	routes := make(map[int32]*db.ListRouteNamesByUserRow, len(names))
	for _, n := range names {
		routes[n.ID] = n
	}
	byID := map[int32]*StretchRoute{}
	seen := map[int32]bool{}
	for _, e := range efforts {
		if e.RouteID == nil {
			continue
		}
		r := byID[*e.RouteID]
		if r == nil {
			route := routes[*e.RouteID]
			// Regrouping can delete a route between the two reads.
			if route == nil {
				e.RouteID = nil
				continue
			}
			r = &StretchRoute{ID: route.ID, Name: route.Name, DistanceMiles: route.DistanceMiles}
			byID[r.ID] = r
			out = append(out, r)
		}
		r.EffortCount++
		if !seen[e.WorkoutID] {
			seen[e.WorkoutID] = true
			r.WorkoutCount++
		}
	}
	slices.SortStableFunc(out, func(a, b *StretchRoute) int { return b.EffortCount - a.EffortCount })
	return out, nil
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
				WorkoutID: r.WorkoutID, WorkoutName: r.WorkoutName, StartTime: r.StartTime, StartOffsetSeconds: r.StartOffsetSeconds, EndOffsetSeconds: r.EndOffsetSeconds,
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
	routeOf, err := workoutRouteIDs(ctx, q, userToken)
	if err != nil {
		return nil, err
	}
	efforts := []*StretchEffort{}
	for _, r := range rows {
		if window.Contains(r.StartTime.Time) {
			efforts = append(efforts, &StretchEffort{
				WorkoutID: r.WorkoutID, WorkoutName: r.WorkoutName, StartTime: r.StartTime, StartOffsetSeconds: r.StartOffsetSeconds, EndOffsetSeconds: r.EndOffsetSeconds,
				DurationSeconds: r.DurationSeconds, StartMeters: r.StartMeters, DistanceMeters: r.DistanceMeters, WalkedFraction: r.WalkedFraction,
				RouteID: routeIDOf(routeOf, r.WorkoutID),
			})
		}
	}
	routes, err := stretchRoutes(ctx, q, efforts, userToken)
	if err != nil {
		return nil, err
	}
	detail := &StretchDetail{StretchSummary: *summarizeStretch(s, efforts), Efforts: efforts, Routes: routes}
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
	// The workout's own passes are among them, in the order it ran them.
	rows, err := q.ListStretchEffortsOverWorkoutStretches(ctx, &db.ListStretchEffortsOverWorkoutStretchesParams{WorkoutID: workoutID, UserID: userToken.ID})
	if err != nil {
		return nil, err
	}
	out := []*WorkoutStretchEffort{}
	times := map[int32][]float64{}
	for _, r := range rows {
		times[r.StretchID] = append(times[r.StretchID], r.DurationSeconds)
	}
	if len(rows) == 0 {
		return out, nil
	}
	var routeID *int32
	if id, err := q.GetWorkoutRouteID(ctx, &db.GetWorkoutRouteIDParams{WorkoutID: workoutID, UserID: userToken.ID}); err == nil {
		routeID = &id
	} else if !IsNotFound(err) {
		return nil, err
	}
	for _, r := range rows {
		if r.WorkoutID != workoutID {
			continue
		}
		effort := &WorkoutStretchEffort{StretchID: r.StretchID, Name: r.Name, StretchMeters: r.Meters, BestSeconds: r.DurationSeconds, Rank: 1, EffortCount: len(times[r.StretchID]), StretchEffort: StretchEffort{
			WorkoutID: r.WorkoutID, WorkoutName: workout.Name, StartTime: workout.StartTime, StartOffsetSeconds: r.StartOffsetSeconds, EndOffsetSeconds: r.EndOffsetSeconds,
			DurationSeconds: r.DurationSeconds, StartMeters: r.StartMeters, DistanceMeters: r.DistanceMeters, WalkedFraction: r.WalkedFraction, RouteID: routeID,
		}}
		for _, seconds := range times[r.StretchID] {
			effort.BestSeconds = min(effort.BestSeconds, seconds)
			if seconds < r.DurationSeconds {
				effort.Rank++
			}
		}
		out = append(out, effort)
	}
	return out, nil
}

// alongRoute is how far into the route a stretch is first come to, given how far into each run its first pass began.
// The representative's own distance is the route's; without a pass by it, the middle of the other runs', whose watches may have started a little off.
func alongRoute(firstPass map[int32]float64, representative *int32) float64 {
	if representative != nil {
		if meters, ok := firstPass[*representative]; ok {
			return meters
		}
	}
	all := make([]float64, 0, len(firstPass))
	for _, meters := range firstPass {
		all = append(all, meters)
	}
	if len(all) == 0 {
		return 0
	}
	slices.Sort(all)
	return all[len(all)/2]
}

// RouteStretches lists the stretches the route's runs (in the window) pass, in the order the route comes to them, with which of its runs didn't.
func (m *storeModel) RouteStretches(ctx context.Context, id int32, window Window, userToken *tokens.UserToken) ([]*RouteStretch, error) {
	route, err := m.Route(ctx, id, window, userToken)
	if err != nil {
		return nil, err
	}
	out := []*RouteStretch{}
	if len(route.Workouts) == 0 {
		return out, nil
	}
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	rows, err := q.ListStretchEffortsByRoute(ctx, &db.ListStretchEffortsByRouteParams{RouteID: id, UserID: userToken.ID})
	if err != nil {
		return nil, err
	}
	// The route was read a moment ago, and its runs are what the answer is about.
	inWindow := make(map[int32]bool, len(route.Workouts))
	for _, w := range route.Workouts {
		inWindow[w.ID] = true
	}
	efforts := map[int32][]*StretchEffort{}
	stretches := map[int32]*db.Stretch{}
	passed := map[int32]map[int32]bool{}
	// Each run's first pass over each stretch, from any date, so the order doesn't move with the window.
	firstPass := map[int32]map[int32]float64{}
	for _, r := range rows {
		if firstPass[r.StretchID] == nil {
			firstPass[r.StretchID] = map[int32]float64{}
			passed[r.StretchID] = map[int32]bool{}
		}
		// A run's passes come in the order it made them.
		if _, seen := firstPass[r.StretchID][r.WorkoutID]; !seen {
			firstPass[r.StretchID][r.WorkoutID] = r.StartMeters
		}
		if !inWindow[r.WorkoutID] {
			continue
		}
		efforts[r.StretchID] = append(efforts[r.StretchID], &StretchEffort{
			WorkoutID: r.WorkoutID, WorkoutName: r.WorkoutName, StartTime: r.StartTime, StartOffsetSeconds: r.StartOffsetSeconds, EndOffsetSeconds: r.EndOffsetSeconds,
			DurationSeconds: r.DurationSeconds, StartMeters: r.StartMeters, DistanceMeters: r.DistanceMeters, WalkedFraction: r.WalkedFraction, RouteID: &route.ID,
		})
		passed[r.StretchID][r.WorkoutID] = true
		stretches[r.StretchID] = &db.Stretch{ID: r.StretchID, Name: r.StretchName, Sport: r.StretchSport, Meters: r.StretchMeters, SourceWorkoutID: r.StretchSourceWorkoutID, CreatedAt: r.StretchCreatedAt}
	}
	along := map[int32]float64{}
	for stretchID, stretchEfforts := range efforts {
		rs := &RouteStretch{StretchSummary: *summarizeStretch(stretches[stretchID], stretchEfforts), RunCount: len(route.Workouts), MissedWorkoutIDs: []int32{}}
		for _, w := range route.Workouts {
			if !passed[stretchID][w.ID] {
				rs.MissedWorkoutIDs = append(rs.MissedWorkoutIDs, w.ID)
			}
		}
		along[stretchID] = alongRoute(firstPass[stretchID], route.RepresentativeWorkoutID)
		out = append(out, rs)
	}
	slices.SortFunc(out, func(a, b *RouteStretch) int {
		return cmp.Or(cmp.Compare(along[a.ID], along[b.ID]), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}
