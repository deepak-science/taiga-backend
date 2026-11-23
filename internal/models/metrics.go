package models

import (
	"context"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/coros"
	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/metrics"
)

// JobKinds is every kind of job the worker runs.
var JobKinds = []string{JobProcessWorkout, JobFetchWeather, JobSyncSource, JobDetectRoutes}

var jobStates = []string{JobQueued, JobRunning, JobDone, JobDead}

// MetricsSnapshot reads the queue, connections and pool across every user, for a scrape.
// Every known kind and state is given, zero or not, so a graph or alert has a series to read before anything happens.
func (m *storeModel) MetricsSnapshot(ctx context.Context, now time.Time) (*metrics.Snapshot, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	snap := &metrics.Snapshot{OldestDue: map[string]time.Duration{}, Pool: m.store.PoolStats()}

	counts := map[[2]string]int64{}
	for _, kind := range JobKinds {
		for _, state := range jobStates {
			counts[[2]string{kind, state}] = 0
		}
	}
	rows, err := q.CountJobsByKindAndState(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		counts[[2]string{r.Kind, r.State}] = r.Jobs
	}
	for k, n := range counts {
		snap.Jobs = append(snap.Jobs, metrics.JobCount{Kind: k[0], State: k[1], Jobs: n})
	}

	for _, kind := range JobKinds {
		runAt, err := q.OldestDueJob(ctx, &db.OldestDueJobParams{Kind: kind, RunAt: timestamptz(now)})
		switch {
		case IsNotFound(err):
			snap.OldestDue[kind] = 0
		case err != nil:
			return nil, err
		default:
			snap.OldestDue[kind] = max(now.Sub(runAt.Time), 0)
		}
	}

	conns := map[[2]string]int64{}
	for _, state := range []string{"ok", "failing", "needs_reconnect"} {
		conns[[2]string{coros.Kind, state}] = 0
	}
	connRows, err := q.CountSourceConnectionsByState(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range connRows {
		conns[[2]string{r.Kind, r.State}] = r.Connections
	}
	for k, n := range conns {
		snap.Connections = append(snap.Connections, metrics.ConnectionCount{Kind: k[0], State: k[1], Connections: n})
	}

	used, err := q.MaxSourceFilesSince(ctx, &db.MaxSourceFilesSinceParams{Kind: coros.Kind, RequestedAt: timestamptz(now.Add(-24 * time.Hour))})
	if err != nil {
		return nil, err
	}
	snap.Allowances = []metrics.SourceAllowance{{Kind: coros.Kind, Used: int(used), Limit: SourceDailyFiles}}
	return snap, nil
}
