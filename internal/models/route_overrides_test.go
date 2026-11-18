package models_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/muktihari/fit/profile/typedef"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

func TestRouteOverrides(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	bob := createUser(t, m, "bob@example.com")

	n1 := uploadHeading(t, m, repo, alice, 1, north, 300)
	n2 := uploadHeading(t, m, repo, alice, 2, north, 300)
	n3 := uploadHeading(t, m, repo, alice, 3, north, 300)
	e1 := uploadHeading(t, m, repo, alice, 4, east, 300)
	uploadHeading(t, m, repo, alice, 5, east, 300)
	require.NoError(t, m.DetectRoutes(ctx, alice))
	northRoute, err := m.WorkoutRoute(ctx, n1.ID, alice)
	require.NoError(t, err)
	eastRoute, err := m.WorkoutRoute(ctx, e1.ID, alice)
	require.NoError(t, err)

	runCount := func(routeID int32) int {
		t.Helper()
		detail, err := m.Route(ctx, routeID, models.Window{}, alice)
		require.NoError(t, err)
		return detail.RunCount
	}

	_, err = m.RouteOverride(ctx, n3.ID, alice)
	assert.True(t, models.IsNotFound(err), "detection placed it: %v", err)

	t.Run("a moved run stays moved through detection", func(t *testing.T) {
		override, err := m.SetRouteOverride(ctx, n3.ID, &eastRoute.ID, alice)
		require.NoError(t, err)
		assert.Equal(t, eastRoute.ID, *override.RouteID)

		onRoute, err := m.WorkoutRoute(ctx, n3.ID, alice)
		require.NoError(t, err, "regrouped before returning")
		assert.Equal(t, eastRoute.ID, onRoute.ID)
		assert.Equal(t, 2, runCount(northRoute.ID))

		uploadHeading(t, m, repo, alice, 6, north, 300)
		require.NoError(t, m.DetectRoutes(ctx, alice))
		detail, err := m.Route(ctx, eastRoute.ID, models.Window{}, alice)
		require.NoError(t, err)
		assert.Equal(t, 3, detail.RunCount)
		assert.Equal(t, []int32{n3.ID}, detail.MovedWorkoutIDs)
		assert.Empty(t, detail.MergedWorkoutIDs)
		assert.NotEqual(t, n3.ID, *detail.RepresentativeWorkoutID, "the track is one of the runs detected there")
		assert.Equal(t, 3, runCount(northRoute.ID))

		stored, err := m.RouteOverride(ctx, n3.ID, alice)
		require.NoError(t, err)
		assert.Equal(t, eastRoute.ID, *stored.RouteID)
	})

	t.Run("a detached run is on no route", func(t *testing.T) {
		override, err := m.SetRouteOverride(ctx, n3.ID, nil, alice)
		require.NoError(t, err)
		assert.Nil(t, override.RouteID)
		require.NoError(t, m.DetectRoutes(ctx, alice))
		_, err = m.WorkoutRoute(ctx, n3.ID, alice)
		assert.ErrorIs(t, err, models.ErrNoRoute)
		assert.Equal(t, 2, runCount(eastRoute.ID))
		assert.Equal(t, 3, runCount(northRoute.ID))
	})

	t.Run("clearing hands it back to detection", func(t *testing.T) {
		require.NoError(t, m.ClearRouteOverride(ctx, n3.ID, alice))
		onRoute, err := m.WorkoutRoute(ctx, n3.ID, alice)
		require.NoError(t, err)
		assert.Equal(t, northRoute.ID, onRoute.ID)
		_, err = m.RouteOverride(ctx, n3.ID, alice)
		assert.True(t, models.IsNotFound(err))
		require.NoError(t, m.ClearRouteOverride(ctx, n3.ID, alice), "nothing to clear is fine")
	})

	t.Run("a route down to runs moved onto it is kept", func(t *testing.T) {
		_, err := m.SetRouteOverride(ctx, n2.ID, &eastRoute.ID, alice)
		require.NoError(t, err)
		for _, w := range mustRoute(t, m, eastRoute.ID, alice).Workouts {
			if w.ID != n2.ID {
				require.NoError(t, m.DeleteWorkout(ctx, w.ID, alice))
			}
		}
		require.NoError(t, m.DetectRoutes(ctx, alice))
		detail := mustRoute(t, m, eastRoute.ID, alice)
		assert.Equal(t, 1, detail.RunCount)
		assert.Equal(t, n2.ID, *detail.RepresentativeWorkoutID)

		require.NoError(t, m.ClearRouteOverride(ctx, n2.ID, alice))
		_, err = m.Route(ctx, eastRoute.ID, models.Window{}, alice)
		assert.True(t, models.IsNotFound(err), "nothing holds it open now: %v", err)
		assert.Equal(t, 4, runCount(northRoute.ID))
	})

	t.Run("what can't be placed", func(t *testing.T) {
		missing := int32(9999)
		_, err := m.SetRouteOverride(ctx, n1.ID, &missing, alice)
		assert.ErrorIs(t, err, models.ErrUnknownRoute)
		_, err = m.SetRouteOverride(ctx, n1.ID, &northRoute.ID, bob)
		assert.True(t, models.IsNotFound(err), "not bob's workout: %v", err)

		upload := func(day int, sport typedef.Sport, subSport typedef.SubSport) *models.Workout {
			start := time.Date(2026, 11, day, 7, 0, 0, 0, time.UTC)
			content := testsupport.EncodeFITSubSport(t, start, []testsupport.Split{{Meters: 3000, Seconds: 900}}, sport, subSport)
			af, _, err := m.AddActivityFile(ctx, bytes.NewReader(content), start, alice, repo)
			require.NoError(t, err)
			w, _, err := m.CreateWorkoutFromActivityFile(ctx, af.ID, alice, repo)
			require.NoError(t, err)
			return w
		}
		treadmill := upload(20, typedef.SportRunning, typedef.SubSportTreadmill)
		_, err = m.SetRouteOverride(ctx, treadmill.ID, &northRoute.ID, alice)
		assert.ErrorIs(t, err, models.ErrNoTrack)
		walk := upload(21, typedef.SportWalking, typedef.SubSportGeneric)
		_, err = m.SetRouteOverride(ctx, walk.ID, &northRoute.ID, alice)
		assert.ErrorIs(t, err, models.ErrRouteSport)
	})
}

func mustRoute(t *testing.T, m models.Model, id int32, user *tokens.UserToken) *models.RouteDetail {
	t.Helper()
	detail, err := m.Route(context.Background(), id, models.Window{}, user)
	require.NoError(t, err)
	return detail
}

func TestMergeRoutes(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")

	n1 := uploadHeading(t, m, repo, alice, 1, north, 300)
	uploadHeading(t, m, repo, alice, 2, north, 300)
	e1 := uploadHeading(t, m, repo, alice, 3, east, 300)
	e2 := uploadHeading(t, m, repo, alice, 4, east, 300)
	require.NoError(t, m.DetectRoutes(ctx, alice))
	northRoute, err := m.WorkoutRoute(ctx, n1.ID, alice)
	require.NoError(t, err)
	eastRoute, err := m.WorkoutRoute(ctx, e1.ID, alice)
	require.NoError(t, err)
	name := "East"
	_, err = m.RenameRoute(ctx, eastRoute.ID, &name, alice)
	require.NoError(t, err)

	_, err = m.MergeRoutes(ctx, northRoute.ID, northRoute.ID, alice)
	assert.ErrorIs(t, err, models.ErrSameRoute)
	_, err = m.MergeRoutes(ctx, northRoute.ID, 9999, alice)
	assert.ErrorIs(t, err, models.ErrUnknownRoute)
	_, err = m.MergeRoutes(ctx, 9999, northRoute.ID, alice)
	assert.True(t, models.IsNotFound(err))

	merged, err := m.MergeRoutes(ctx, northRoute.ID, eastRoute.ID, alice)
	require.NoError(t, err)
	assert.Equal(t, northRoute.ID, merged.ID)
	assert.Equal(t, 4, merged.RunCount)
	require.NotNil(t, merged.Name)
	assert.Equal(t, name, *merged.Name, "an unnamed route takes the name of the one merged into it")
	_, err = m.Route(ctx, eastRoute.ID, models.Window{}, alice)
	assert.True(t, models.IsNotFound(err), "the merged route is gone: %v", err)
	routes, err := m.Routes(ctx, models.Window{}, alice)
	require.NoError(t, err)
	assert.Len(t, routes, 1)

	// A later run on the merged-in route follows it, and carries the merge past the runs it was made with.
	e3 := uploadHeading(t, m, repo, alice, 5, east, 300)
	require.NoError(t, m.DetectRoutes(ctx, alice))
	detail := mustRoute(t, m, northRoute.ID, alice)
	assert.Equal(t, 5, detail.RunCount)
	assert.ElementsMatch(t, []int32{e1.ID, e2.ID, e3.ID}, detail.MergedWorkoutIDs)
	require.NoError(t, m.DeleteWorkout(ctx, e1.ID, alice))
	require.NoError(t, m.DeleteWorkout(ctx, e2.ID, alice))
	require.NoError(t, m.DetectRoutes(ctx, alice))
	onRoute, err := m.WorkoutRoute(ctx, e3.ID, alice)
	require.NoError(t, err)
	assert.Equal(t, northRoute.ID, onRoute.ID)

	e4 := uploadHeading(t, m, repo, alice, 6, east, 300)
	require.NoError(t, m.DetectRoutes(ctx, alice))
	require.NoError(t, m.UnmergeRoute(ctx, northRoute.ID, alice))
	detail = mustRoute(t, m, northRoute.ID, alice)
	assert.Equal(t, 2, detail.RunCount)
	assert.Empty(t, detail.MergedWorkoutIDs)
	apart, err := m.WorkoutRoute(ctx, e4.ID, alice)
	require.NoError(t, err)
	assert.NotEqual(t, northRoute.ID, apart.ID, "the eastward runs are a route of their own again")
	assert.Equal(t, 2, apart.RunCount)
	require.NoError(t, m.DetectRoutes(ctx, alice))
	assert.Equal(t, 2, mustRoute(t, m, northRoute.ID, alice).RunCount, "and stay apart")
}
