package models

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
	"github.com/jackc/pgx/v5/pgtype"
)

const JobDetectRoutes = "detect_routes"

// ErrNoRoute means the workout isn't on any route: it has no GPS, or no other run matches it yet.
var ErrNoRoute = errors.New("workout is not on a route")

// RouteSummary is a route with figures over its runs, or just those starting in a window.
type RouteSummary struct {
	ID   int32   `json:"id"`
	Name *string `json:"name"`
	// RepresentativeWorkoutID is the run whose track stands for the route; null briefly after that run is deleted.
	RepresentativeWorkoutID *int32 `json:"representative_workout_id"`
	RunCount                int    `json:"run_count"`
	// Sport is shared by every run on the route.
	Sport string `json:"sport"`
	// DistanceMiles is the representative run's.
	DistanceMiles   *float64           `json:"distance_miles"`
	BestTimeSeconds *float64           `json:"best_time_seconds"`
	BestWorkoutID   *int32             `json:"best_workout_id"`
	LatestStartTime pgtype.Timestamptz `json:"latest_start_time"`
}

// RouteDetail is a route with its runs (in the window, if one was given), newest first, and the representative's track.
type RouteDetail struct {
	RouteSummary
	Workouts []*Workout `json:"workouts"`
	// [lat, lon] pairs; empty if the representative run is gone.
	Track [][2]float64 `json:"track"`
	// MovedWorkoutIDs are the runs the user moved here one by one, in or out of the window.
	MovedWorkoutIDs []int32 `json:"moved_workout_ids"`
	// MergedWorkoutIDs are the runs here through a merge.
	MergedWorkoutIDs []int32 `json:"merged_workout_ids"`
}

// summarize takes the sport and distance from the representative run, wherever it falls, and the figures from the runs starting in the window.
func summarize(route *db.Route, runs []*Workout, window Window) *RouteSummary {
	s := &RouteSummary{ID: route.ID, Name: route.Name, RepresentativeWorkoutID: route.RepresentativeWorkoutID}
	for _, w := range runs {
		if s.RepresentativeWorkoutID != nil && w.ID == *s.RepresentativeWorkoutID {
			s.DistanceMiles = w.DistanceMiles
			s.Sport = w.Sport
		}
		if !window.Contains(w.StartTime.Time) {
			continue
		}
		s.RunCount++
		if w.TimeSeconds != nil && (s.BestTimeSeconds == nil || *w.TimeSeconds < *s.BestTimeSeconds) {
			s.BestTimeSeconds, s.BestWorkoutID = w.TimeSeconds, &w.ID
		}
		if !s.LatestStartTime.Valid || w.StartTime.Time.After(s.LatestStartTime.Time) {
			s.LatestStartTime = w.StartTime
		}
	}
	return s
}

// routeRuns groups the user's routed workouts by route, each newest first.
func routeRuns(ctx context.Context, q db.Querier, userToken *tokens.UserToken) (map[int32][]*Workout, error) {
	memberships, err := q.ListWorkoutRoutesByUser(ctx, userToken.ID)
	if err != nil {
		return nil, err
	}
	routeOf := make(map[int32]int32, len(memberships))
	for _, wr := range memberships {
		routeOf[wr.WorkoutID] = wr.RouteID
	}
	// Already newest first.
	all, err := q.ListWorkoutsByUser(ctx, userToken.ID)
	if err != nil {
		return nil, err
	}
	runs := make(map[int32][]*Workout)
	for _, w := range all {
		if r, ok := routeOf[w.ID]; ok {
			runs[r] = append(runs[r], w)
		}
	}
	return runs, nil
}

// Routes lists the user's routes, most recently run first; given a window, only those run in it.
func (m *storeModel) Routes(ctx context.Context, window Window, userToken *tokens.UserToken) ([]*RouteSummary, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	routes, err := q.ListRoutesByUser(ctx, userToken.ID)
	if err != nil {
		return nil, err
	}
	runs, err := routeRuns(ctx, q, userToken)
	if err != nil {
		return nil, err
	}
	out := make([]*RouteSummary, 0, len(routes))
	for _, r := range routes {
		s := summarize(r, runs[r.ID], window)
		if s.RunCount == 0 && window != (Window{}) {
			continue
		}
		out = append(out, s)
	}
	slices.SortStableFunc(out, func(a, b *RouteSummary) int {
		return b.LatestStartTime.Time.Compare(a.LatestStartTime.Time)
	})
	return out, nil
}

func (m *storeModel) Route(ctx context.Context, id int32, window Window, userToken *tokens.UserToken) (*RouteDetail, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	route, err := q.GetRoute(ctx, &db.GetRouteParams{ID: id, UserID: userToken.ID})
	if err != nil {
		return nil, err
	}
	runs, err := routeRuns(ctx, q, userToken)
	if err != nil {
		return nil, err
	}
	detail := &RouteDetail{
		RouteSummary: *summarize(route, runs[id], window), Workouts: []*Workout{}, Track: [][2]float64{},
		MovedWorkoutIDs: []int32{}, MergedWorkoutIDs: []int32{},
	}
	onRoute := make(map[int32]bool, len(runs[id]))
	for _, w := range runs[id] {
		onRoute[w.ID] = true
		if window.Contains(w.StartTime.Time) {
			detail.Workouts = append(detail.Workouts, w)
		}
	}
	overrides, err := q.ListRouteOverridesByUser(ctx, userToken.ID)
	if err != nil {
		return nil, err
	}
	for _, o := range overrides {
		// An override detection couldn't apply (the run lost its track, say) isn't listed.
		switch {
		case o.RouteID == nil || *o.RouteID != id || !onRoute[o.WorkoutID]:
		case o.WithGroup:
			detail.MergedWorkoutIDs = append(detail.MergedWorkoutIDs, o.WorkoutID)
		default:
			detail.MovedWorkoutIDs = append(detail.MovedWorkoutIDs, o.WorkoutID)
		}
	}
	if route.RepresentativeWorkoutID != nil {
		raw, err := q.GetWorkoutTrack(ctx, &db.GetWorkoutTrackParams{WorkoutID: *route.RepresentativeWorkoutID, UserID: userToken.ID})
		if err != nil && !IsNotFound(err) {
			return nil, err
		}
		if err == nil {
			var track workouts.Track
			if err := json.Unmarshal(raw, &track); err != nil {
				return nil, fmt.Errorf("decoding track of workout %d: %w", *route.RepresentativeWorkoutID, err)
			}
			detail.Track = track.Points
		}
	}
	return detail, nil
}

// RenameRoute sets the route's name, or clears it with nil.
func (m *storeModel) RenameRoute(ctx context.Context, id int32, name *string, userToken *tokens.UserToken) (*RouteSummary, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	route, err := q.RenameRoute(ctx, &db.RenameRouteParams{ID: id, UserID: userToken.ID, Name: name})
	if err != nil {
		return nil, err
	}
	runs, err := routeRuns(ctx, q, userToken)
	if err != nil {
		return nil, err
	}
	return summarize(route, runs[id], Window{}), nil
}

// WorkoutRoute is the route the workout is on; ErrNoRoute if none, not-found if the workout isn't the user's.
func (m *storeModel) WorkoutRoute(ctx context.Context, workoutID int32, userToken *tokens.UserToken) (*RouteSummary, error) {
	if _, err := m.Workout(ctx, workoutID, userToken); err != nil {
		return nil, err
	}
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	routeID, err := q.GetWorkoutRouteID(ctx, &db.GetWorkoutRouteIDParams{WorkoutID: workoutID, UserID: userToken.ID})
	if IsNotFound(err) {
		return nil, ErrNoRoute
	}
	if err != nil {
		return nil, err
	}
	detail, err := m.Route(ctx, routeID, Window{}, userToken)
	if err != nil {
		return nil, err
	}
	return &detail.RouteSummary, nil
}

// EnqueueDetectRoutes queues route detection for the user, unless it's already pending.
func (m *storeModel) EnqueueDetectRoutes(ctx context.Context, now time.Time, userToken *tokens.UserToken) error {
	q, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	return q.EnqueueDetectRoutes(ctx, &db.EnqueueDetectRoutesParams{UserID: userToken.ID, RunAt: timestamptz(now)})
}

// DetectRoutes regroups all the user's tracked runs into routes from scratch, then applies their overrides.
// Each new group keeps the id, and so the name, of the old route it shares the most runs with; old routes left with no runs are deleted.
func (m *storeModel) DetectRoutes(ctx context.Context, userToken *tokens.UserToken) error {
	return m.store.InTx(ctx, func(q db.Querier) error {
		return detectRoutes(ctx, q, userToken.ID, 0)
	})
}

// routeCluster is a set of runs grouped by their tracks, as indices into the user's tracked runs, less any the user moved elsewhere.
type routeCluster struct {
	members []int
	// forced is the route a merge puts the cluster on; 0 leaves it to claim one.
	forced int32
}

// builtRoute is a route's runs after overrides.
type builtRoute struct {
	members []int
	// core is the largest cluster on the route, which its representative comes from; empty when every run was moved here.
	core []int
}

// detectRoutes is DetectRoutes inside a transaction.
// A non-zero unmerge drops the merges into that route, leaving those runs to group on their own again.
func detectRoutes(ctx context.Context, q db.Querier, userID int32, unmerge int32) error {
	all, err := q.ListWorkoutTracksByUser(ctx, userID)
	if err != nil {
		return err
	}
	rows := all[:0]
	var tracks []*workouts.Track
	index := make(map[int32]int, len(all))
	for _, row := range all {
		var t workouts.Track
		if err := json.Unmarshal(row.Track, &t); err != nil {
			log.Printf("Skipping undecodable track of workout %d: %v", row.WorkoutID, err)
			continue
		}
		index[row.WorkoutID] = len(rows)
		rows = append(rows, row)
		tracks = append(tracks, &t)
	}

	old, err := q.ListWorkoutRoutesByUser(ctx, userID)
	if err != nil {
		return err
	}
	oldRoute := make(map[int32]int32, len(old))
	// Routes never mix sports, so an override onto a route of another sport (the run's sport was corrected since) is ignored.
	routeSport := make(map[int32]string)
	for _, wr := range old {
		oldRoute[wr.WorkoutID] = wr.RouteID
		if i, ok := index[wr.WorkoutID]; ok {
			routeSport[wr.RouteID] = rows[i].Sport
		}
	}
	existing, err := q.ListRoutesByUser(ctx, userID)
	if err != nil {
		return err
	}
	routeByID := make(map[int32]*db.Route, len(existing))
	for _, r := range existing {
		routeByID[r.ID] = r
	}
	overrideRows, err := q.ListRouteOverridesByUser(ctx, userID)
	if err != nil {
		return err
	}
	overrides := make(map[int32]*db.RouteOverride, len(overrideRows))
	for _, o := range overrideRows {
		overrides[o.WorkoutID] = o
	}
	applies := func(o *db.RouteOverride, sport string) bool {
		if o.RouteID == nil {
			return !o.WithGroup
		}
		if _, ok := routeByID[*o.RouteID]; !ok {
			return false
		}
		s, known := routeSport[*o.RouteID]
		return !known || s == sport
	}

	// Clusters come from every tracked run, moved or not, so taking a run off a route leaves the rest of it as it was.
	var clusters []*routeCluster
	var moved []int
	for _, sport := range workouts.ScoredSports {
		var idx []int
		var sub []*workouts.Track
		for i, row := range rows {
			if row.Sport == sport {
				idx = append(idx, i)
				sub = append(sub, tracks[i])
			}
		}
		for _, g := range workouts.ClusterRoutes(sub) {
			c := &routeCluster{}
			votes := make(map[int32]int)
			released := false
			for _, k := range g.Members {
				i := idx[k]
				id := rows[i].WorkoutID
				o := overrides[id]
				switch {
				case o == nil || !applies(o, sport):
					c.members = append(c.members, i)
				case !o.WithGroup:
					if o.RouteID != nil {
						moved = append(moved, i)
					}
				case *o.RouteID == unmerge:
					if err := q.DeleteRouteOverride(ctx, &db.DeleteRouteOverrideParams{WorkoutID: id, UserID: userID}); err != nil {
						return err
					}
					delete(overrides, id)
					released = true
					c.members = append(c.members, i)
				default:
					votes[*o.RouteID]++
					c.members = append(c.members, i)
				}
			}
			for r, n := range votes {
				if c.forced == 0 || n > votes[c.forced] || (n == votes[c.forced] && r < c.forced) {
					c.forced = r
				}
			}
			for _, i := range c.members {
				id := rows[i].WorkoutID
				if released {
					// Or the cluster would claim the route it was just split from.
					delete(oldRoute, id)
				}
				if c.forced == 0 {
					continue
				}
				// Every run of a merged cluster carries the merge, so it outlives the runs it was made with.
				if o := overrides[id]; o == nil || (o.WithGroup && *o.RouteID != c.forced) {
					if err := q.UpsertRouteOverride(ctx, &db.UpsertRouteOverrideParams{WorkoutID: id, UserID: userID, RouteID: &c.forced, WithGroup: true}); err != nil {
						return err
					}
				}
			}
			clusters = append(clusters, c)
		}
	}

	built := make(map[int32]*builtRoute)
	var order []int32
	add := func(routeID int32, members []int, cluster bool) {
		b := built[routeID]
		if b == nil {
			b = &builtRoute{}
			built[routeID] = b
			order = append(order, routeID)
		}
		b.members = append(b.members, members...)
		if cluster && len(members) > len(b.core) {
			b.core = members
		}
	}
	var free []*routeCluster
	for _, c := range clusters {
		if c.forced != 0 {
			add(c.forced, c.members, true)
		} else {
			free = append(free, c)
		}
	}
	// A route the user named, moved a run onto or merged into is theirs to keep, even down to one run.
	kept := make(map[int32]bool)
	for _, r := range existing {
		kept[r.ID] = r.Name != nil || built[r.ID] != nil
	}
	for _, i := range moved {
		kept[*overrides[rows[i].WorkoutID].RouteID] = true
	}

	// Bigger clusters pick first, so a route that split keeps its id on the larger part.
	slices.SortStableFunc(free, func(a, b *routeCluster) int { return cmp.Compare(len(b.members), len(a.members)) })
	claimed := make(map[int32]bool)
	for _, c := range free {
		overlap := make(map[int32]int)
		for _, i := range c.members {
			if r, ok := oldRoute[rows[i].WorkoutID]; ok && !claimed[r] && routeByID[r] != nil {
				overlap[r]++
			}
		}
		var routeID int32
		for r, n := range overlap {
			if routeID == 0 || n > overlap[routeID] || (n == overlap[routeID] && r < routeID) {
				routeID = r
			}
		}
		if len(c.members) < 2 && !kept[routeID] {
			continue
		}
		if routeID == 0 {
			route, err := q.CreateRoute(ctx, &db.CreateRouteParams{UserID: userID})
			if err != nil {
				return err
			}
			routeID = route.ID
		}
		claimed[routeID] = true
		add(routeID, c.members, true)
	}
	for _, i := range moved {
		add(*overrides[rows[i].WorkoutID].RouteID, []int{i}, false)
	}

	if err := q.DeleteWorkoutRoutesByUser(ctx, userID); err != nil {
		return err
	}
	for _, routeID := range order {
		b := built[routeID]
		rep := b.members[0]
		if len(b.core) > 0 {
			rep = workouts.Medoid(tracks, b.core)
		}
		if err := q.SetRouteRepresentative(ctx, &db.SetRouteRepresentativeParams{ID: routeID, UserID: userID, RepresentativeWorkoutID: &rows[rep].WorkoutID}); err != nil {
			return err
		}
		for _, i := range b.members {
			if err := q.AddWorkoutRoute(ctx, &db.AddWorkoutRouteParams{WorkoutID: rows[i].WorkoutID, RouteID: routeID, UserID: userID}); err != nil {
				return err
			}
		}
	}
	for _, r := range existing {
		if built[r.ID] != nil {
			continue
		}
		if err := q.DeleteRouteOverridesByRoute(ctx, &db.DeleteRouteOverridesByRouteParams{RouteID: &r.ID, UserID: userID}); err != nil {
			return err
		}
		if err := q.DeleteRoute(ctx, &db.DeleteRouteParams{ID: r.ID, UserID: userID}); err != nil {
			return err
		}
	}
	return nil
}

// storeTrack replaces the workout's track, removing it for a run without GPS or a sport that doesn't get routes.
func storeTrack(ctx context.Context, q db.Querier, workoutID int32, run *workouts.Run, userToken *tokens.UserToken) error {
	var track *workouts.Track
	if run.Summary.Scored() {
		track = workouts.BuildTrack(run)
	}
	if track == nil {
		return q.DeleteWorkoutTrack(ctx, &db.DeleteWorkoutTrackParams{WorkoutID: workoutID, UserID: userToken.ID})
	}
	raw, err := json.Marshal(track)
	if err != nil {
		return fmt.Errorf("encoding track for workout %d: %w", workoutID, err)
	}
	return q.UpsertWorkoutTrack(ctx, &db.UpsertWorkoutTrackParams{WorkoutID: workoutID, UserID: userToken.ID, Track: raw})
}
