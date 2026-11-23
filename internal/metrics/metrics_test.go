package metrics_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/metrics"
)

// observed is how many observations (or the counter's value) the metric has under labels, summed over any other labels.
func observed(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	require.NoError(t, err)
	total := 0.0
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
	metric:
		for _, m := range f.GetMetric() {
			have := map[string]string{}
			for _, l := range m.GetLabel() {
				have[l.GetName()] = l.GetValue()
			}
			for k, v := range labels {
				if have[k] != v {
					continue metric
				}
			}
			total += value(m)
		}
	}
	return total
}

func value(m *dto.Metric) float64 {
	switch {
	case m.Histogram != nil:
		return float64(m.Histogram.GetSampleCount())
	case m.Counter != nil:
		return m.Counter.GetValue()
	case m.Gauge != nil:
		return m.Gauge.GetValue()
	}
	return 0
}

func TestRequestsAreLabelledByRoutePatternNotPath(t *testing.T) {
	inner := chi.NewRouter()
	inner.Get("/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	api := chi.NewRouter()
	api.Mount("/widgets", inner)
	r := chi.NewRouter()
	r.Use(metrics.Middleware)
	r.Mount("/", api)

	before := observed(t, "taiga_http_request_duration_seconds", map[string]string{"route": "/widgets/{id}", "status": "418", "method": "GET"})
	for _, id := range []string{"1", "2", "3"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/widgets/"+id, nil))
	}
	assert.Equal(t, before+3, observed(t, "taiga_http_request_duration_seconds", map[string]string{"route": "/widgets/{id}", "status": "418", "method": "GET"}))
	assert.Zero(t, observed(t, "taiga_http_request_duration_seconds", map[string]string{"route": "/widgets/1"}))

	// A path nothing matched is its mount's wildcard, whatever the path.
	before = observed(t, "taiga_http_request_duration_seconds", map[string]string{"route": "/*", "status": "404"})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nothing/here", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/or/here", nil))
	assert.Equal(t, before+2, observed(t, "taiga_http_request_duration_seconds", map[string]string{"route": "/*", "status": "404"}))
}

func TestTransportCountsByDestinationAndOutcome(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken" {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: metrics.Transport("test-dest", nil)}

	for _, path := range []string{"/ok", "/broken"} {
		resp, err := client.Get(srv.URL + path)
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
	_, err := client.Get("http://127.0.0.1:1/refused")
	require.Error(t, err)

	for outcome, want := range map[string]float64{"2xx": 1, "5xx": 1, "error": 1} {
		assert.Equal(t, want, observed(t, "taiga_outbound_request_duration_seconds", map[string]string{"destination": "test-dest", "outcome": outcome}), outcome)
	}
}

type repo struct{ err error }

func (r repo) Store(context.Context, io.Reader) (string, error) { return "h", r.err }
func (r repo) Exists(context.Context, string) (bool, error)     { return true, r.err }
func (r repo) Fetch(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), r.err
}

func TestFileRepoCountsOperations(t *testing.T) {
	ctx := context.Background()
	good := metrics.FileRepo(repo{}, "test-backend")
	_, _ = good.Store(ctx, strings.NewReader("x"))
	_, _ = good.Exists(ctx, "h")
	bad := metrics.FileRepo(repo{err: errors.New("disk")}, "test-backend")
	_, _ = bad.Fetch(ctx, "h")

	assert.Equal(t, 1.0, observed(t, "taiga_filerepo_operation_duration_seconds", map[string]string{"backend": "test-backend", "op": "store", "outcome": "ok"}))
	assert.Equal(t, 1.0, observed(t, "taiga_filerepo_operation_duration_seconds", map[string]string{"backend": "test-backend", "op": "exists", "outcome": "ok"}))
	assert.Equal(t, 1.0, observed(t, "taiga_filerepo_operation_duration_seconds", map[string]string{"backend": "test-backend", "op": "fetch", "outcome": "error"}))
}

type snapshotter struct {
	snap *metrics.Snapshot
	err  error
}

func (s snapshotter) MetricsSnapshot(context.Context, time.Time) (*metrics.Snapshot, error) {
	return s.snap, s.err
}

func TestStateIsReadAtEachScrape(t *testing.T) {
	metrics.SetState(snapshotter{snap: &metrics.Snapshot{
		Jobs:        []metrics.JobCount{{Kind: "sync_source", State: "dead", Jobs: 2}},
		OldestDue:   map[string]time.Duration{"process_workout": 90 * time.Second},
		Connections: []metrics.ConnectionCount{{Kind: "coros", State: "needs_reconnect", Connections: 1}},
		Allowances:  []metrics.SourceAllowance{{Kind: "coros", Used: 12, Limit: 45}},
		Pool:        metrics.PoolStats{Max: 25, InUse: 3, Idle: 2, Waits: 4, WaitTime: 2 * time.Second},
	}})
	assert.Equal(t, 1.0, observed(t, "taiga_state_up", nil))
	assert.Equal(t, 2.0, observed(t, "taiga_jobs", map[string]string{"kind": "sync_source", "state": "dead"}))
	assert.Equal(t, 90.0, observed(t, "taiga_jobs_oldest_due_seconds", map[string]string{"kind": "process_workout"}))
	assert.Equal(t, 1.0, observed(t, "taiga_source_connections", map[string]string{"state": "needs_reconnect"}))
	assert.Equal(t, 12.0, observed(t, "taiga_source_daily_files_used", map[string]string{"kind": "coros"}))
	assert.Equal(t, 45.0, observed(t, "taiga_source_daily_files_limit", map[string]string{"kind": "coros"}))
	assert.Equal(t, 3.0, observed(t, "taiga_db_connections", map[string]string{"state": "in_use"}))
	assert.Equal(t, 2.0, observed(t, "taiga_db_wait_seconds_total", nil))

	// A database that can't answer leaves the rest of the scrape working.
	metrics.SetState(snapshotter{err: errors.New("db down")})
	t.Cleanup(func() { metrics.SetState(nil) })
	assert.Equal(t, 0.0, observed(t, "taiga_state_up", nil))
	assert.Zero(t, observed(t, "taiga_jobs", nil))
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "go_goroutines")
}

func TestSyncCountsActivitiesAndFiles(t *testing.T) {
	metrics.Sync("test-kind", "done", time.Second, metrics.SyncActivities{Listed: 5, Imported: 2, Files: 3})
	assert.Equal(t, 1.0, observed(t, "taiga_source_sync_duration_seconds", map[string]string{"kind": "test-kind", "outcome": "done"}))
	assert.Equal(t, 5.0, observed(t, "taiga_source_activities_total", map[string]string{"kind": "test-kind", "result": "listed"}))
	assert.Equal(t, 2.0, observed(t, "taiga_source_activities_total", map[string]string{"kind": "test-kind", "result": "imported"}))
	assert.Equal(t, 3.0, observed(t, "taiga_source_files_requested_total", map[string]string{"kind": "test-kind"}))
}
