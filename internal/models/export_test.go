package models_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/backup"
	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
)

func TestExportKeepsWhatSourcesHaveDealtWith(t *testing.T) {
	ctx := context.Background()
	s := testsupport.NewStore(t)
	m, repo := models.NewFromStore(s), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	q, err := s.GetQuerier()
	require.NoError(t, err)
	at := func(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

	file, _, err := m.AddActivityFile(ctx, bytes.NewReader(testsupport.EncodeFITFromWatch(t, day1, fourK)), day1, alice, repo)
	require.NoError(t, err)
	w, _, err := m.CreateWorkoutFromActivityFile(ctx, file.ID, alice, repo)
	require.NoError(t, err)
	hash := *file.FileRepoHash
	sport := int32(100)
	load := 120.0

	require.NoError(t, q.CreateSourceActivity(ctx, &db.CreateSourceActivityParams{UserID: alice.ID, Kind: "coros", ExternalID: "1", ActivityFileID: &file.ID, StartTime: at(day1), Files: 1, FetchedAt: at(day1), SportType: &sport}))
	require.NoError(t, q.CreateSourceActivity(ctx, &db.CreateSourceActivityParams{UserID: alice.ID, Kind: "coros", ExternalID: "2", FetchedAt: at(day1)}))
	require.NoError(t, q.MarkSourceMatchTried(ctx, &db.MarkSourceMatchTriedParams{WorkoutID: w.ID, Kind: "coros", UserID: alice.ID, TriedAt: at(day1)}))
	require.NoError(t, q.UpsertCOROSMetadata(ctx, &db.UpsertCOROSMetadataParams{WorkoutID: w.ID, UserID: alice.ID, ExternalID: "1", FetchedAt: at(day1), Reply: "Activity Details", TrainingLoad: &load}))

	a, err := m.Export(ctx, true, day1.Add(time.Hour), alice)
	require.NoError(t, err)

	start := day1.UTC()
	assert.Equal(t, []backup.SourceActivity{
		{Kind: "coros", ExternalID: "1", File: &hash, StartTime: &start, Files: 1, FetchedAt: day1.UTC(), SportType: &sport},
		{Kind: "coros", ExternalID: "2", FetchedAt: day1.UTC()},
	}, a.SourceActivities)
	assert.Equal(t, []backup.SourceMatchAttempt{{File: hash, Kind: "coros", TriedAt: day1.UTC()}}, a.SourceMatchAttempts)
	require.Len(t, a.COROSMetadata, 1)
	assert.Equal(t, hash, a.COROSMetadata[0].File)
	assert.Equal(t, "Activity Details", a.COROSMetadata[0].Reply)
	assert.Equal(t, &load, a.COROSMetadata[0].TrainingLoad)

	a, err = m.Export(ctx, false, day1.Add(time.Hour), alice)
	require.NoError(t, err)
	assert.Empty(t, a.COROSMetadata, "fetched data left out")
	assert.Len(t, a.SourceActivities, 2, "what sources dealt with stays: it's what keeps a restore from fetching again")
}
