package models_test

import (
	"bytes"
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

// A jittery Google Fit run over a route's ground stays off the route, but counts in totals at the route's length.
func TestJitteryRunCountsAtItsRouteLength(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	for day := 1; day <= 3; day++ {
		uploadHeading(t, m, repo, alice, day, north, 300)
	}

	// The same 3km north, zigzagging 3.3m either side of it (20% longer), and Google Fit's total 6% over that.
	start := time.Date(2026, 11, 5, 7, 0, 0, 0, time.UTC)
	x := testsupport.NewTCX(start, []testsupport.Split{{Meters: 3000, Seconds: 900}}, "Running")
	lonPerMeter := 1 / (111_320 * math.Cos(testsupport.StartLatitude*math.Pi/180))
	for i := range x.Points {
		x.Points[i].Lon += 3.3 * lonPerMeter * float64(1-2*(i%2))
		x.Points[i].Meters *= 1.27
	}
	af, _, err := m.AddActivityFile(ctx, bytes.NewReader(x.Encode(t)), start, alice, repo)
	require.NoError(t, err)
	jittery, _, err := m.CreateWorkoutFromActivityFile(ctx, af.ID, alice, repo)
	require.NoError(t, err)
	require.Equal(t, workouts.ExcludedJitteryTrack, jittery.ExcludedReason)

	require.NoError(t, m.DetectRoutes(ctx, alice))
	routes, err := m.Routes(ctx, models.Window{}, alice)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	assert.Equal(t, 3, routes[0].RunCount, "the jittery run isn't one of the route's runs")

	marked, err := m.Workout(ctx, jittery.ID, alice)
	require.NoError(t, err)
	require.NotNil(t, marked.JitterRouteID)
	assert.Equal(t, routes[0].ID, *marked.JitterRouteID)
	require.NotNil(t, marked.JitterRouteMiles)
	assert.InEpsilon(t, 3000/1609.344, *marked.JitterRouteMiles, 0.01)

	running := func() *models.SportTotals {
		t.Helper()
		totals, err := m.WorkoutTotals(ctx, models.Window{}, alice)
		require.NoError(t, err)
		require.Len(t, totals.Sports, 1)
		return totals.Sports[0]
	}
	s := running()
	assert.EqualValues(t, 4, s.Workouts)
	assert.InEpsilon(t, 4*3000/1609.344, s.DistanceMiles, 0.01, "at the route's length, not its own")

	t.Run("a new stretch doesn't time it", func(t *testing.T) {
		stretch, err := m.CreateStretch(ctx, models.NewStretch{Name: "middle", WorkoutID: *routes[0].BestWorkoutID, StartMeters: 1000, EndMeters: 2000}, alice)
		require.NoError(t, err)
		assert.Len(t, stretch.Efforts, 3)
		for _, e := range stretch.Efforts {
			assert.NotEqual(t, jittery.ID, e.WorkoutID)
		}
	})

	t.Run("longest goes by the route's length", func(t *testing.T) {
		routeMiles := *marked.JitterRouteMiles
		require.Greater(t, *marked.DistanceMiles, 3300/1609.344, "its own distance is over the clean run's")
		only := models.Window{From: start, To: start.Add(24 * time.Hour)}
		totals, err := m.WorkoutTotals(ctx, only, alice)
		require.NoError(t, err)
		require.Len(t, totals.Sports, 1)
		longest := totals.Sports[0].LongestByDistance
		require.NotNil(t, longest)
		assert.Equal(t, jittery.ID, longest.WorkoutID)
		assert.InDelta(t, routeMiles, *longest.DistanceMiles, 1e-9)

		// A clean run longer than the route but shorter than the jittery run's own distance.
		day6 := time.Date(2026, 11, 6, 7, 0, 0, 0, time.UTC)
		af, _, err := m.AddActivityFile(ctx, bytes.NewReader(testsupport.EncodeFITHeading(t, day6, []testsupport.Split{{Meters: 3300, Seconds: 990}}, east)), day6, alice, repo)
		require.NoError(t, err)
		clean, _, err := m.CreateWorkoutFromActivityFile(ctx, af.ID, alice, repo)
		require.NoError(t, err)
		both := models.Window{From: start, To: day6.Add(24 * time.Hour)}
		totals, err = m.WorkoutTotals(ctx, both, alice)
		require.NoError(t, err)
		assert.Equal(t, clean.ID, totals.Sports[0].LongestByDistance.WorkoutID)
		assert.InDelta(t, routeMiles+*clean.DistanceMiles, totals.Sports[0].DistanceMiles, 1e-9)

		years, err := m.PeriodicTotals(ctx, "year", both, time.UTC, alice)
		require.NoError(t, err)
		require.Len(t, years, 1)
		assert.Equal(t, clean.ID, years[0].Sports[0].LongestByDistance.WorkoutID)
		require.NoError(t, m.DeleteWorkout(ctx, clean.ID, alice))
	})

	t.Run("it can't be moved onto a route", func(t *testing.T) {
		_, err := m.SetRouteOverride(ctx, jittery.ID, &routes[0].ID, alice)
		assert.ErrorIs(t, err, models.ErrNoTrack)
	})

	t.Run("the user's exclusion still keeps it out", func(t *testing.T) {
		_, err := m.CorrectWorkout(ctx, jittery.ID, models.Corrections{ExcludedReason: "bad trace"}, alice, repo)
		require.NoError(t, err)
		assert.EqualValues(t, 3, running().Workouts)
		_, err = m.CorrectWorkout(ctx, jittery.ID, models.Corrections{}, alice, repo)
		require.NoError(t, err)
		assert.EqualValues(t, 4, running().Workouts)
	})

	t.Run("no route, no total", func(t *testing.T) {
		// Every run of the (unnamed) route gone but one: no route, so nothing to place the jittery run on.
		all, err := m.Workouts(ctx, models.WorkoutQuery{}, alice)
		require.NoError(t, err)
		for _, w := range all {
			if w.ID != jittery.ID && w.StartTime.Time.Day() != 1 {
				require.NoError(t, m.DeleteWorkout(ctx, w.ID, alice))
			}
		}
		require.NoError(t, m.DetectRoutes(ctx, alice))
		cleared, err := m.Workout(ctx, jittery.ID, alice)
		require.NoError(t, err)
		assert.Nil(t, cleared.JitterRouteID)
		assert.Nil(t, cleared.JitterRouteMiles)
		assert.EqualValues(t, 1, running().Workouts)
	})
}
