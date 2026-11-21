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

func TestWorkoutsAreNamedFromWhereAndWhen(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	w := uploadWorkout(t, m, repo, alice, time.Date(2026, 11, 21, 20, 30, 0, 0, time.UTC), briskWalk)

	assert.Equal(t, "America/Chicago", w.Timezone)
	require.NotNil(t, w.UtcOffsetSeconds)
	assert.Equal(t, "North Saint Paul", w.Place, "where the synthetic track starts")
	assert.Equal(t, int32(-6*3600), *w.UtcOffsetSeconds)
	assert.Equal(t, "North Saint Paul afternoon run", w.Name)

	walked, err := m.CorrectWorkout(ctx, w.ID, models.Corrections{Sport: workouts.SportWalking}, alice, repo)
	require.NoError(t, err)
	assert.Equal(t, "North Saint Paul afternoon walk", walked.Name, "named as corrected")

	efforts, err := m.BestSegments(ctx, "1k", workouts.SportWalking, 10, models.Window{}, alice)
	require.NoError(t, err)
	require.Len(t, efforts, 1)
	assert.Equal(t, walked.Name, efforts[0].WorkoutName)
}

func TestHandEnteredWorkoutIsJustAWorkout(t *testing.T) {
	m := testsupport.NewModel(t)
	alice := createUser(t, m, "alice@example.com")
	w, err := m.AddWorkout(context.Background(), &models.Workout{}, alice)
	require.NoError(t, err)
	assert.Equal(t, "Workout", w.Name)
	run, err := m.AddWorkout(context.Background(), &models.Workout{Sport: workouts.SportRunning}, alice)
	require.NoError(t, err)
	assert.Equal(t, "Run", run.Name)
	assert.Empty(t, w.Place)
	assert.Nil(t, w.UtcOffsetSeconds)
}
