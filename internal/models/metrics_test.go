package models_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/coros"
	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/metrics"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
)

func TestMetricsSnapshotCountsAcrossUsers(t *testing.T) {
	ctx := context.Background()
	s := testsupport.NewStore(t)
	m := models.NewFromStore(s)
	alice := createUser(t, m, "alice@example.com")
	bob := createUser(t, m, "bob@example.com")
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

	// An empty deployment still gives every kind and state, at zero.
	snap, err := m.MetricsSnapshot(ctx, t0)
	require.NoError(t, err)
	assert.Contains(t, snap.Jobs, metrics.JobCount{Kind: models.JobSyncSource, State: models.JobDead})
	assert.Equal(t, time.Duration(0), snap.OldestDue[models.JobDetectRoutes])
	assert.Contains(t, snap.Connections, metrics.ConnectionCount{Kind: coros.Kind, State: "needs_reconnect"})
	assert.Equal(t, []metrics.SourceAllowance{{Kind: coros.Kind, Used: 0, Limit: models.SourceDailyFiles}}, snap.Allowances)

	require.NoError(t, m.EnqueueDetectRoutes(ctx, t0, alice))
	require.NoError(t, m.EnqueueDetectRoutes(ctx, t0.Add(5*time.Minute), bob))
	// Each queues a sync at t0.
	client, err := m.SaveSourceClient(ctx, coros.Kind, "https://coros.example.com", "https://taiga.example.com/cb", "sealed")
	require.NoError(t, err)
	aliceConn, err := m.ConnectSource(ctx, coros.Kind, client.ID, "sealed", t0, alice)
	require.NoError(t, err)
	bobConn, err := m.ConnectSource(ctx, coros.Kind, client.ID, "sealed", t0, bob)
	require.NoError(t, err)

	// Alice's routes job waits out a backoff, so it isn't late; her sync dies.
	job, err := m.ClaimJob(ctx, t0, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, models.JobDetectRoutes, job.Kind)
	require.NoError(t, m.RetryJobLater(ctx, job.ID, t0.Add(time.Hour), errors.New("later"), t0))
	job, err = m.ClaimJob(ctx, t0, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, models.JobSyncSource, job.Kind)
	require.NoError(t, m.KillJob(ctx, job.ID, errors.New("no"), t0))
	require.NoError(t, m.FailSourceSync(ctx, aliceConn.ID, "COROS was down", false, alice))
	require.NoError(t, m.FailSourceSync(ctx, bobConn.ID, "turned away", true, bob))

	q, err := s.GetQuerier()
	require.NoError(t, err)
	for _, r := range []struct {
		user  int32
		files int32
		at    time.Time
	}{
		{alice.ID, 3, t0.Add(-time.Hour)},
		{alice.ID, 4, t0.Add(-2 * time.Hour)},
		{bob.ID, 5, t0.Add(-time.Hour)},
		// Older than a day, so it no longer counts.
		{bob.ID, 40, t0.Add(-25 * time.Hour)},
	} {
		require.NoError(t, q.RecordSourceFileRequest(ctx, &db.RecordSourceFileRequestParams{UserID: r.user, Kind: coros.Kind, ExternalID: "x", Files: r.files, RequestedAt: pgtype.Timestamptz{Time: r.at, Valid: true}}))
	}

	snap, err = m.MetricsSnapshot(ctx, t0.Add(10*time.Minute))
	require.NoError(t, err)
	jobs := map[[2]string]int64{}
	for _, j := range snap.Jobs {
		jobs[[2]string{j.Kind, j.State}] = j.Jobs
	}
	assert.Equal(t, map[[2]string]int64{
		{models.JobDetectRoutes, models.JobQueued}: 2,
		{models.JobSyncSource, models.JobQueued}:   1,
		{models.JobSyncSource, models.JobDead}:     1,
	}, nonZero(jobs))
	assert.Equal(t, 5*time.Minute, snap.OldestDue[models.JobDetectRoutes])
	assert.Equal(t, 10*time.Minute, snap.OldestDue[models.JobSyncSource])
	assert.Equal(t, time.Duration(0), snap.OldestDue[models.JobProcessWorkout])

	conns := map[string]int64{}
	for _, c := range snap.Connections {
		conns[c.State] = c.Connections
	}
	assert.Equal(t, map[string]int64{"ok": 0, "failing": 1, "needs_reconnect": 1}, conns)
	assert.Equal(t, []metrics.SourceAllowance{{Kind: coros.Kind, Used: 7, Limit: models.SourceDailyFiles}}, snap.Allowances)
	assert.Positive(t, snap.Pool.Max)
}

func nonZero(m map[[2]string]int64) map[[2]string]int64 {
	out := map[[2]string]int64{}
	for k, v := range m {
		if v != 0 {
			out[k] = v
		}
	}
	return out
}
