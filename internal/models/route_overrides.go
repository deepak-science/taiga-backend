package models

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

// ErrNoTrack means the workout can't be placed on a route, since routes are made of GPS tracks.
var ErrNoTrack = errors.New("workout has no GPS track to place on a route: only outdoor runs and walks that count have one")

// ErrUnknownRoute means a request named a route that isn't the user's.
var ErrUnknownRoute = errors.New("no such route")

// ErrRouteSport means a move or merge would mix sports on one route.
var ErrRouteSport = errors.New("a route's runs all share one sport")

// ErrSameRoute means a route was asked to merge into itself.
var ErrSameRoute = errors.New("a route can't be merged into itself")

// RouteOverride is the user's placement of one run, which route detection applies over its own grouping.
type RouteOverride struct {
	WorkoutID int32 `json:"workout_id"`
	// RouteID is the route the run was moved onto; null keeps it off every route.
	RouteID *int32 `json:"route_id"`
}

// routeSport is the sport of the route's runs, or empty for a route with none.
// Any run of it will do when the representative was just deleted and detection hasn't picked another yet.
func routeSport(ctx context.Context, q db.Querier, route *db.Route) (string, error) {
	ids := []int32{}
	if route.RepresentativeWorkoutID != nil {
		ids = append(ids, *route.RepresentativeWorkoutID)
	}
	memberships, err := q.ListWorkoutRoutesByUser(ctx, route.UserID)
	if err != nil {
		return "", err
	}
	for _, wr := range memberships {
		if wr.RouteID == route.ID {
			ids = append(ids, wr.WorkoutID)
		}
	}
	for _, id := range ids {
		w, err := q.GetWorkout(ctx, &db.GetWorkoutParams{ID: id, UserID: route.UserID})
		if IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		return w.Sport, nil
	}
	return "", nil
}

// RouteOverride is the user's placement of the workout; not-found when detection placed it.
// A run that came along in a merge has none of its own.
func (m *storeModel) RouteOverride(ctx context.Context, workoutID int32, userToken *tokens.UserToken) (*RouteOverride, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	o, err := q.GetRouteOverride(ctx, &db.GetRouteOverrideParams{WorkoutID: workoutID, UserID: userToken.ID})
	if err != nil {
		return nil, err
	}
	if o.WithGroup {
		return nil, errNoOverride
	}
	return &RouteOverride{WorkoutID: o.WorkoutID, RouteID: o.RouteID}, nil
}

// errNoOverride reads as not-found, like a missing row.
var errNoOverride = fmt.Errorf("workout has no route override of its own: %w", sql.ErrNoRows)

// SetRouteOverride moves the workout onto a route, or with a nil route keeps it off all of them, and regroups the user's routes.
func (m *storeModel) SetRouteOverride(ctx context.Context, workoutID int32, routeID *int32, userToken *tokens.UserToken) (*RouteOverride, error) {
	err := m.routesTx(ctx, userToken.ID, func(q db.Querier) error {
		workout, err := q.GetWorkout(ctx, &db.GetWorkoutParams{ID: workoutID, UserID: userToken.ID})
		if err != nil {
			return err
		}
		if _, err := q.GetWorkoutTrack(ctx, &db.GetWorkoutTrackParams{WorkoutID: workoutID, UserID: userToken.ID}); err != nil {
			if IsNotFound(err) {
				return ErrNoTrack
			}
			return err
		}
		if routeID != nil {
			route, err := q.GetRoute(ctx, &db.GetRouteParams{ID: *routeID, UserID: userToken.ID})
			if IsNotFound(err) {
				return ErrUnknownRoute
			}
			if err != nil {
				return err
			}
			sport, err := routeSport(ctx, q, route)
			if err != nil {
				return err
			}
			if sport != "" && sport != workout.Sport {
				return ErrRouteSport
			}
		}
		if err := q.UpsertRouteOverride(ctx, &db.UpsertRouteOverrideParams{WorkoutID: workoutID, UserID: userToken.ID, RouteID: routeID}); err != nil {
			return err
		}
		return detectRoutes(ctx, q, userToken.ID, 0)
	})
	if err != nil {
		return nil, err
	}
	return &RouteOverride{WorkoutID: workoutID, RouteID: routeID}, nil
}

// ClearRouteOverride hands the workout back to detection; it does nothing for a workout the user never placed.
func (m *storeModel) ClearRouteOverride(ctx context.Context, workoutID int32, userToken *tokens.UserToken) error {
	return m.routesTx(ctx, userToken.ID, func(q db.Querier) error {
		if _, err := q.GetWorkout(ctx, &db.GetWorkoutParams{ID: workoutID, UserID: userToken.ID}); err != nil {
			return err
		}
		o, err := q.GetRouteOverride(ctx, &db.GetRouteOverrideParams{WorkoutID: workoutID, UserID: userToken.ID})
		if IsNotFound(err) || (err == nil && o.WithGroup) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := q.DeleteRouteOverride(ctx, &db.DeleteRouteOverrideParams{WorkoutID: workoutID, UserID: userToken.ID}); err != nil {
			return err
		}
		// Detection keeps a group on the route most of its runs were on, so a run still listed on the route it was moved to could hold a kept route open by itself.
		if err := q.DeleteWorkoutRoute(ctx, &db.DeleteWorkoutRouteParams{WorkoutID: workoutID, UserID: userToken.ID}); err != nil {
			return err
		}
		return detectRoutes(ctx, q, userToken.ID, 0)
	})
}

// MergeRoutes puts every run of the route from onto the route into, along with the runs detection groups with them later, and deletes from.
// The merged route keeps into's name, or takes from's if it has none.
func (m *storeModel) MergeRoutes(ctx context.Context, into, from int32, userToken *tokens.UserToken) (*RouteSummary, error) {
	if into == from {
		return nil, ErrSameRoute
	}
	err := m.routesTx(ctx, userToken.ID, func(q db.Querier) error {
		target, err := q.GetRoute(ctx, &db.GetRouteParams{ID: into, UserID: userToken.ID})
		if err != nil {
			return err
		}
		source, err := q.GetRoute(ctx, &db.GetRouteParams{ID: from, UserID: userToken.ID})
		if IsNotFound(err) {
			return ErrUnknownRoute
		}
		if err != nil {
			return err
		}
		targetSport, err := routeSport(ctx, q, target)
		if err != nil {
			return err
		}
		sourceSport, err := routeSport(ctx, q, source)
		if err != nil {
			return err
		}
		if targetSport != "" && sourceSport != "" && targetSport != sourceSport {
			return ErrRouteSport
		}

		// Runs moved or merged onto from follow it; the rest of its runs are the ones detection put there, and now carry the merge.
		if err := q.RetargetRouteOverrides(ctx, &db.RetargetRouteOverridesParams{FromRouteID: &from, ToRouteID: &into, UserID: userToken.ID}); err != nil {
			return err
		}
		overrides, err := q.ListRouteOverridesByUser(ctx, userToken.ID)
		if err != nil {
			return err
		}
		overridden := make(map[int32]bool, len(overrides))
		for _, o := range overrides {
			overridden[o.WorkoutID] = true
		}
		memberships, err := q.ListWorkoutRoutesByUser(ctx, userToken.ID)
		if err != nil {
			return err
		}
		for _, wr := range memberships {
			if wr.RouteID != from || overridden[wr.WorkoutID] {
				continue
			}
			if err := q.UpsertRouteOverride(ctx, &db.UpsertRouteOverrideParams{WorkoutID: wr.WorkoutID, UserID: userToken.ID, RouteID: &into, WithGroup: true}); err != nil {
				return err
			}
		}
		if err := q.MoveWorkoutRoutes(ctx, &db.MoveWorkoutRoutesParams{FromRouteID: from, ToRouteID: into, UserID: userToken.ID}); err != nil {
			return err
		}
		if target.Name == nil && source.Name != nil {
			if _, err := q.RenameRoute(ctx, &db.RenameRouteParams{ID: into, UserID: userToken.ID, Name: source.Name}); err != nil {
				return err
			}
		}
		return detectRoutes(ctx, q, userToken.ID, 0)
	})
	if err != nil {
		return nil, err
	}
	detail, err := m.Route(ctx, into, Window{}, userToken)
	if err != nil {
		return nil, err
	}
	return &detail.RouteSummary, nil
}

// UnmergeRoute undoes every merge into the route: the runs that came with one go back to the routes detection gives them.
func (m *storeModel) UnmergeRoute(ctx context.Context, id int32, userToken *tokens.UserToken) error {
	return m.routesTx(ctx, userToken.ID, func(q db.Querier) error {
		if _, err := q.GetRoute(ctx, &db.GetRouteParams{ID: id, UserID: userToken.ID}); err != nil {
			return err
		}
		if err := detectRoutes(ctx, q, userToken.ID, id); err != nil {
			return err
		}
		// Regrouping only sees runs that count right now; one that doesn't would bring its merge back when it counts again.
		return q.DeleteMergeOverridesByRoute(ctx, &db.DeleteMergeOverridesByRouteParams{RouteID: &id, UserID: userToken.ID})
	})
}

// routesTx is the transaction every regrouping of the user's routes runs in, taking turns on the user's row.
// Each rebuilds all the memberships from what it read at its start, so two at once on PostgreSQL would overwrite each other or collide.
func (m *storeModel) routesTx(ctx context.Context, userID int32, fn func(q db.Querier) error) error {
	return m.store.InTx(ctx, func(q db.Querier) error {
		if err := q.LockUser(ctx, userID); err != nil {
			return err
		}
		return fn(q)
	})
}
