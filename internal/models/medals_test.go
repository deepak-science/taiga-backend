package models_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

func TestMedals(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	bob := createUser(t, m, "bob@example.com")
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	run := func(start time.Time, seconds float64) *models.Workout {
		return uploadWorkout(t, m, repo, alice, start, []testsupport.Split{{Meters: 2000, Seconds: seconds}})
	}

	beaten := run(time.Date(2025, 3, 1, 13, 0, 0, 0, time.UTC), 700)
	march := run(time.Date(2025, 3, 15, 13, 0, 0, 0, time.UTC), 600)
	june := run(time.Date(2025, 6, 1, 13, 0, 0, 0, time.UTC), 650)
	// 2026-01-01 03:00 UTC is still New Year's Eve in Chicago.
	eve := run(time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC), 500)
	february := run(time.Date(2026, 2, 1, 13, 0, 0, 0, time.UTC), 550)
	tied := run(time.Date(2026, 2, 10, 13, 0, 0, 0, time.UTC), 550)
	excluded := run(time.Date(2026, 2, 20, 13, 0, 0, 0, time.UTC), 400)
	_, err = m.CorrectWorkout(ctx, excluded.ID, models.Corrections{ExcludedReason: "ran with a friend"}, alice, repo)
	require.NoError(t, err)
	walk := run(time.Date(2026, 3, 1, 13, 0, 0, 0, time.UTC), 1400)
	_, err = m.CorrectWorkout(ctx, walk.ID, models.Corrections{Sport: workouts.SportWalking}, alice, repo)
	require.NoError(t, err)
	uploadWorkout(t, m, repo, bob, time.Date(2025, 3, 20, 13, 0, 0, 0, time.UTC), []testsupport.Split{{Meters: 2000, Seconds: 300}})

	medalAt := func(medals map[int32][]*models.SegmentMedal, w *models.Workout, segment string) string {
		for _, medal := range medals[w.ID] {
			if medal.Segment == segment {
				return medal.Medal
			}
		}
		return ""
	}

	t.Run("in Chicago", func(t *testing.T) {
		medals, err := m.Medals(ctx, chicago, alice)
		require.NoError(t, err)
		assert.Equal(t, models.MedalGold, medalAt(medals, eve, "1k"), "the fastest ever is gold, not silver too")
		assert.Equal(t, models.MedalBronze, medalAt(medals, march, "1k"), "New Year's Eve is 2025's best in Chicago")
		assert.Equal(t, models.MedalBronze, medalAt(medals, june, "1k"))
		assert.Equal(t, models.MedalSilver, medalAt(medals, february, "1k"))
		assert.NotContains(t, medals, beaten.ID, "beaten in its own month")
		assert.NotContains(t, medals, tied.ID, "the earlier run keeps a tie")
		assert.NotContains(t, medals, excluded.ID, "an excluded run holds nothing and takes nothing")
		assert.Equal(t, models.MedalGold, medalAt(medals, walk, "1k"), "walks are ranked apart from runs")
		require.NotEmpty(t, medals[eve.ID])
		assert.Equal(t, "1k", medals[eve.ID][0].Segment, "in the workout's segment order")
	})

	t.Run("in UTC", func(t *testing.T) {
		medals, err := m.Medals(ctx, time.UTC, alice)
		require.NoError(t, err)
		assert.Equal(t, models.MedalGold, medalAt(medals, eve, "1k"))
		assert.Equal(t, models.MedalSilver, medalAt(medals, march, "1k"), "New Year's Eve is 2026's in UTC")
		assert.Equal(t, models.MedalBronze, medalAt(medals, february, "1k"))
	})

	t.Run("a faster run takes the gold", func(t *testing.T) {
		faster := run(time.Date(2026, 4, 1, 13, 0, 0, 0, time.UTC), 450)
		medals, err := m.Medals(ctx, chicago, alice)
		require.NoError(t, err)
		assert.Equal(t, models.MedalGold, medalAt(medals, faster, "1k"))
		assert.Equal(t, models.MedalSilver, medalAt(medals, eve, "1k"), "still 2025's best")
	})

	t.Run("bob's runs are his", func(t *testing.T) {
		medals, err := m.Medals(ctx, chicago, bob)
		require.NoError(t, err)
		assert.Len(t, medals, 1)
	})
}

// The segments route reads medals and segments apart, so a reprocess can land between them.
func TestMedalsMatchSegmentsAcrossReprocess(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	w := uploadWorkout(t, m, repo, alice, time.Date(2026, 3, 1, 13, 0, 0, 0, time.UTC), []testsupport.Split{{Meters: 2000, Seconds: 600}})

	before, err := m.WorkoutSegments(ctx, w.ID, alice)
	require.NoError(t, err)
	medals, err := m.Medals(ctx, time.UTC, alice)
	require.NoError(t, err)
	_, err = m.ReprocessWorkout(ctx, w.ID, alice, repo)
	require.NoError(t, err)
	after, err := m.WorkoutSegments(ctx, w.ID, alice)
	require.NoError(t, err)

	require.Len(t, after, len(before))
	require.NotEqual(t, before[0].ID, after[0].ID, "reprocessing replaces the rows, so ids can't join them")
	byName := map[string]string{}
	for _, medal := range medals[w.ID] {
		byName[medal.Segment] = medal.Medal
	}
	for _, s := range after {
		assert.Equal(t, models.MedalGold, byName[s.Name], s.Name)
	}
}
