package models_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/coros"
	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
)

// listed is a source that lists what it's given; nothing here needs its files.
type listed []coros.Activity

func (l listed) List(context.Context, time.Time, time.Time) ([]coros.Activity, error) {
	return l, nil
}

func (l listed) Files(context.Context, coros.Activity) ([][]byte, error) {
	return nil, coros.ErrInProgress
}

func (l listed) Detail(context.Context, coros.Activity) (*coros.Detail, error) {
	load := 120.0
	return &coros.Detail{Reply: "Activity Details", TrainingLoad: &load}, nil
}

func TestDeletingAWorkoutTakesItsLapsAndCOROSFigures(t *testing.T) {
	ctx := context.Background()
	s := testsupport.NewStore(t)
	m, repo := models.NewFromStore(s), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	q, err := s.GetQuerier()
	require.NoError(t, err)

	file, _, err := m.AddActivityFile(ctx, bytes.NewReader(testsupport.EncodeFITFromWatch(t, day1, fourK)), day1, alice, repo)
	require.NoError(t, err)
	w, _, err := m.CreateWorkoutFromActivityFile(ctx, file.ID, alice, repo)
	require.NoError(t, err)
	require.NoError(t, q.UpsertCOROSMetadata(ctx, &db.UpsertCOROSMetadataParams{WorkoutID: w.ID, UserID: alice.ID, ExternalID: "1", FetchedAt: pgtype.Timestamptz{Time: day1, Valid: true}, Reply: "Activity Details"}))
	_, err = q.GetWorkoutLaps(ctx, &db.GetWorkoutLapsParams{WorkoutID: w.ID, UserID: alice.ID})
	require.NoError(t, err)

	require.NoError(t, m.DeleteWorkout(ctx, w.ID, alice))

	// SQLite enforces no cascade here, so they go only if the delete takes them.
	_, err = q.GetWorkoutLaps(ctx, &db.GetWorkoutLapsParams{WorkoutID: w.ID, UserID: alice.ID})
	assert.True(t, models.IsNotFound(err), "laps: %v", err)
	_, err = q.GetCOROSMetadata(ctx, &db.GetCOROSMetadataParams{WorkoutID: w.ID, UserID: alice.ID})
	assert.True(t, models.IsNotFound(err), "COROS's figures: %v", err)
}

func TestAnActivityKeptBeforeItsSportCodeGetsOneWhenListed(t *testing.T) {
	ctx := context.Background()
	s := testsupport.NewStore(t)
	m, repo := models.NewFromStore(s), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	q, err := s.GetQuerier()
	require.NoError(t, err)
	now := day1.Add(time.Hour)

	file, _, err := m.AddActivityFile(ctx, bytes.NewReader(testsupport.EncodeFIT(t, day1, fourK)), day1, alice, repo)
	require.NoError(t, err)
	w, _, err := m.CreateWorkoutFromActivityFile(ctx, file.ID, alice, repo)
	require.NoError(t, err)
	// As a sync before the column existed left it.
	require.NoError(t, q.CreateSourceActivity(ctx, &db.CreateSourceActivityParams{UserID: alice.ID, Kind: coros.Kind, ExternalID: "77", ActivityFileID: &file.ID, StartTime: pgtype.Timestamptz{Time: day1, Valid: true}, Files: 1, FetchedAt: pgtype.Timestamptz{Time: day1, Valid: true}}))
	client, err := m.SaveSourceClient(ctx, coros.Kind, "https://coros.example.com", "https://taiga.example.com/cb", "sealed")
	require.NoError(t, err)
	conn, err := m.ConnectSource(ctx, coros.Kind, client.ID, "sealed", now, alice)
	require.NoError(t, err)

	_, err = m.WorkoutCOROSMetadata(ctx, w.ID, alice)
	require.ErrorIs(t, err, models.ErrNoCOROSMetadata)

	res, err := m.SyncSource(ctx, conn, listed{{ID: "77", SportType: 100, Start: day1}}, repo, now, alice)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Detailed, "listed again, so its sport code is known and COROS can be asked")
	figures, err := m.WorkoutCOROSMetadata(ctx, w.ID, alice)
	require.NoError(t, err)
	assert.Equal(t, 120.0, *figures.TrainingLoad)
}
