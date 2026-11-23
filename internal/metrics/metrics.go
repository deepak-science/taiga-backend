// Package metrics is what taiga tells Prometheus: requests, jobs, syncs, outbound calls, storage, and the state of the queue and connections read at each scrape.
// Label values are kinds, routes, states and outcomes, a fixed handful each; nothing is ever labelled by user.
package metrics

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "taiga"

var (
	requests = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "http_request_duration_seconds",
		Help:      "Requests served, by method, chi route pattern and status.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "route", "status"})

	jobs = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "job_duration_seconds",
		Help:      "Jobs run, by kind and outcome (done, retried, dead).",
		Buckets:   []float64{.01, .05, .1, .5, 1, 5, 10, 30, 60, 120, 300},
	}, []string{"kind", "outcome"})

	syncs = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "source_sync_duration_seconds",
		Help:      "Source syncs, by kind and outcome (done, failed, needs_reconnect).",
		Buckets:   []float64{.1, .5, 1, 5, 10, 30, 60, 120, 300},
	}, []string{"kind", "outcome"})

	sourceActivities = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "source_activities_total",
		Help:      "What syncs did with the source's activities: listed, imported, already_had, unreadable, waiting, detailed, matched.",
	}, []string{"kind", "result"})

	sourceFiles = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "source_files_requested_total",
		Help:      "Files the source issued links for, each counting against its daily limit.",
	}, []string{"kind"})

	tokenRefreshes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "source_token_refreshes_total",
		Help:      "Source token refreshes, by outcome (refreshed, failed).",
	}, []string{"kind", "outcome"})

	outbound = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "outbound_request_duration_seconds",
		Help:      "Calls taiga makes elsewhere, by destination and outcome (2xx..5xx, or error), timed to the response headers.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"destination", "outcome"})

	fileRepo = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "filerepo_operation_duration_seconds",
		Help:      "File repo operations (store, exists, fetch), by backend and outcome; a fetch is timed to opening the file.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"backend", "op", "outcome"})
)

// Registry holds every taiga metric plus Go's runtime and process collectors; it's its own, not the global one, so nothing a library registers leaks in.
var Registry = prometheus.NewRegistry()

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		requests, jobs, syncs, sourceActivities, sourceFiles, tokenRefreshes, outbound, fileRepo,
		state,
	)
}

// Handler serves Registry in Prometheus's text format.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry})
}

// Middleware times each request under the route chi matched, never the path, so ids don't become label values.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		route := ""
		if rc := chi.RouteContext(r.Context()); rc != nil {
			route = rc.RoutePattern()
		}
		if route == "" {
			route = "unmatched"
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		requests.WithLabelValues(r.Method, route, strconv.Itoa(status)).Observe(time.Since(start).Seconds())
	})
}

// Job records one job run; outcome is done, retried or dead.
func Job(kind, outcome string, took time.Duration) {
	jobs.WithLabelValues(kind, outcome).Observe(took.Seconds())
}

// SyncActivities is what one sync did with the source's activities.
type SyncActivities struct {
	Listed, Imported, AlreadyHad, Unreadable, Waiting, Detailed, Matched, Files int
}

// Sync records one sync of a source; outcome is done, failed or needs_reconnect.
func Sync(kind, outcome string, took time.Duration, a SyncActivities) {
	syncs.WithLabelValues(kind, outcome).Observe(took.Seconds())
	for result, n := range map[string]int{
		"listed":      a.Listed,
		"imported":    a.Imported,
		"already_had": a.AlreadyHad,
		"unreadable":  a.Unreadable,
		"waiting":     a.Waiting,
		"detailed":    a.Detailed,
		"matched":     a.Matched,
	} {
		sourceActivities.WithLabelValues(kind, result).Add(float64(n))
	}
	sourceFiles.WithLabelValues(kind).Add(float64(a.Files))
}

// TokenRefresh records a source's tokens being refreshed, or failing to be; outcome is refreshed or failed.
func TokenRefresh(kind, outcome string) {
	tokenRefreshes.WithLabelValues(kind, outcome).Inc()
}

// Transport times every call through base (http.DefaultTransport when nil) under destination, so a slow or failing provider shows apart from taiga's own faults.
func Transport(destination string, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{destination: destination, base: base}
}

type transport struct {
	destination string
	base        http.RoundTripper
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	outcome := "error"
	if err == nil {
		outcome = strconv.Itoa(resp.StatusCode/100) + "xx"
	}
	outbound.WithLabelValues(t.destination, outcome).Observe(time.Since(start).Seconds())
	return resp, err
}

// JobCount is how many jobs of a kind are in a state.
type JobCount struct {
	Kind, State string
	Jobs        int64
}

// ConnectionCount is how many connections of a kind are in a state (ok, failing, needs_reconnect).
type ConnectionCount struct {
	Kind, State string
	Connections int64
}

// SourceAllowance is how much of a source's daily file limit the busiest user has used.
type SourceAllowance struct {
	Kind        string
	Used, Limit int
}

// PoolStats is the database pool as it stands.
type PoolStats struct {
	Max, InUse, Idle int
	// Waits and WaitTime are cumulative: acquires that had to wait for a connection, and how long they waited.
	Waits    int64
	WaitTime time.Duration
}

// Snapshot is the state read from the database at a scrape.
type Snapshot struct {
	Jobs []JobCount
	// OldestDue is, per kind, how long the longest-waiting job that could run has been due; zero when none is.
	OldestDue   map[string]time.Duration
	Connections []ConnectionCount
	Allowances  []SourceAllowance
	Pool        PoolStats
}

// Snapshotter reads a Snapshot; models.Model is one.
type Snapshotter interface {
	MetricsSnapshot(ctx context.Context, now time.Time) (*Snapshot, error)
}

var (
	jobsDesc        = prometheus.NewDesc(namespace+"_jobs", "Jobs in the queue, by kind and state.", []string{"kind", "state"}, nil)
	oldestDueDesc   = prometheus.NewDesc(namespace+"_jobs_oldest_due_seconds", "How long the longest-waiting job of a kind that could run has been due; 0 when none is.", []string{"kind"}, nil)
	connectionsDesc = prometheus.NewDesc(namespace+"_source_connections", "Source connections, by kind and state (ok, failing, needs_reconnect).", []string{"kind", "state"}, nil)
	filesUsedDesc   = prometheus.NewDesc(namespace+"_source_daily_files_used", "Files the busiest user has had issued by the source in the last 24 hours.", []string{"kind"}, nil)
	filesLimitDesc  = prometheus.NewDesc(namespace+"_source_daily_files_limit", "How many files a user may have issued by the source in 24 hours.", []string{"kind"}, nil)
	poolDesc        = prometheus.NewDesc(namespace+"_db_connections", "Database pool connections, by state (in_use, idle).", []string{"state"}, nil)
	poolMaxDesc     = prometheus.NewDesc(namespace+"_db_connections_max", "The database pool's limit on connections.", nil, nil)
	poolWaitsDesc   = prometheus.NewDesc(namespace+"_db_waits_total", "Connection acquires that had to wait.", nil, nil)
	poolWaitDesc    = prometheus.NewDesc(namespace+"_db_wait_seconds_total", "Time spent waiting for a connection.", nil, nil)
	stateUpDesc     = prometheus.NewDesc(namespace+"_state_up", "Whether the last scrape could read the queue and connections from the database.", nil, nil)
)

// scrapeTimeout bounds the database reads a scrape makes, well inside Prometheus's default 10s.
const scrapeTimeout = 5 * time.Second

type stateCollector struct {
	s atomic.Pointer[Snapshotter]
}

var state = &stateCollector{}

// SetState has every scrape read the queue, connections and pool from s; until it's called, or after it's called with nil, those are left out.
func SetState(s Snapshotter) {
	if s == nil {
		state.s.Store(nil)
		return
	}
	state.s.Store(&s)
}

func (c *stateCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{jobsDesc, oldestDueDesc, connectionsDesc, filesUsedDesc, filesLimitDesc, poolDesc, poolMaxDesc, poolWaitsDesc, poolWaitDesc, stateUpDesc} {
		ch <- d
	}
}

func (c *stateCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.s.Load()
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), scrapeTimeout)
	defer cancel()
	snap, err := (*s).MetricsSnapshot(ctx, time.Now())
	if err != nil {
		// The rest of the scrape still serves; taiga_state_up says this part is missing.
		log.Printf("metrics: reading the queue and connections: %v", err)
		ch <- prometheus.MustNewConstMetric(stateUpDesc, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(stateUpDesc, prometheus.GaugeValue, 1)
	for _, j := range snap.Jobs {
		ch <- prometheus.MustNewConstMetric(jobsDesc, prometheus.GaugeValue, float64(j.Jobs), j.Kind, j.State)
	}
	for kind, age := range snap.OldestDue {
		ch <- prometheus.MustNewConstMetric(oldestDueDesc, prometheus.GaugeValue, age.Seconds(), kind)
	}
	for _, conn := range snap.Connections {
		ch <- prometheus.MustNewConstMetric(connectionsDesc, prometheus.GaugeValue, float64(conn.Connections), conn.Kind, conn.State)
	}
	for _, a := range snap.Allowances {
		ch <- prometheus.MustNewConstMetric(filesUsedDesc, prometheus.GaugeValue, float64(a.Used), a.Kind)
		ch <- prometheus.MustNewConstMetric(filesLimitDesc, prometheus.GaugeValue, float64(a.Limit), a.Kind)
	}
	p := snap.Pool
	ch <- prometheus.MustNewConstMetric(poolDesc, prometheus.GaugeValue, float64(p.InUse), "in_use")
	ch <- prometheus.MustNewConstMetric(poolDesc, prometheus.GaugeValue, float64(p.Idle), "idle")
	ch <- prometheus.MustNewConstMetric(poolMaxDesc, prometheus.GaugeValue, float64(p.Max))
	ch <- prometheus.MustNewConstMetric(poolWaitsDesc, prometheus.CounterValue, float64(p.Waits))
	ch <- prometheus.MustNewConstMetric(poolWaitDesc, prometheus.CounterValue, p.WaitTime.Seconds())
}
