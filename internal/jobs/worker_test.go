package jobs_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/jobs"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/weather"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

var (
	start = time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	fourK = []testsupport.Split{{Meters: 1000, Seconds: 300}, {Meters: 1000, Seconds: 200}, {Meters: 2000, Seconds: 600}}
)

// clock is a settable time for the worker, so retries and leases don't need real waiting.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newWorker(m models.Model, repo filerepo.FileRepo) (*jobs.Worker, *clock) {
	c := &clock{t: start}
	w := jobs.NewWorker(m, repo)
	w.Now = c.now
	return w, c
}

func createUser(t *testing.T, m models.Model, email string) *tokens.UserToken {
	t.Helper()
	resp, err := m.CreateUser(t.Context(), &models.CreateUserRequest{Email: email, DisplayName: email, Password: "password123"})
	require.NoError(t, err)
	return &tokens.UserToken{ID: resp.Id, Email: email}
}

// legacyWorkout is a workout linked to a file but never processed, like one from before processor versions existed.
func legacyWorkout(t *testing.T, m models.Model, repo filerepo.FileRepo, user *tokens.UserToken, content []byte) *models.Workout {
	t.Helper()
	af, _, err := m.AddActivityFile(t.Context(), bytes.NewReader(content), start, user, repo)
	require.NoError(t, err)
	w, err := m.AddWorkout(t.Context(), &models.Workout{StartTime: pgtype.Timestamptz{Time: start, Valid: true}, ActivityFileID: &af.ID}, user)
	require.NoError(t, err)
	return w
}

func onlyJob(t *testing.T, m models.Model, user *tokens.UserToken) *models.Job {
	t.Helper()
	all, err := m.Jobs(t.Context(), "", 10, user)
	require.NoError(t, err)
	require.Len(t, all, 1)
	return all[0]
}

func TestStaleWorkoutsAreReprocessed(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	stale := legacyWorkout(t, m, repo, alice, testsupport.EncodeFIT(t, start, fourK))
	_, err := m.AddWorkout(ctx, &models.Workout{StartTime: pgtype.Timestamptz{Time: start, Valid: true}}, alice)
	require.NoError(t, err)
	w, c := newWorker(m, repo)

	n, err := m.EnqueueStaleWorkouts(ctx, c.now())
	require.NoError(t, err)
	assert.EqualValues(t, 1, n, "only the file-backed workout; the hand-entered one has nothing to reprocess")
	n, err = m.EnqueueStaleWorkouts(ctx, c.now())
	require.NoError(t, err)
	assert.Zero(t, n, "already queued")

	found, err := w.RunOnce(ctx)
	require.NoError(t, err)
	require.True(t, found)

	all, err := m.Jobs(ctx, "", 10, alice)
	require.NoError(t, err)
	require.Len(t, all, 2, "the reprocess, and route detection queued after it")
	detect, job := all[0], all[1]
	assert.Equal(t, models.JobDone, job.State)
	assert.Equal(t, stale.ID, *job.WorkoutID)
	assert.Equal(t, models.JobDetectRoutes, detect.Kind)
	assert.Equal(t, models.JobQueued, detect.State)
	got, err := m.Workout(ctx, stale.ID, alice)
	require.NoError(t, err)
	assert.Equal(t, workouts.ProcessorVersion, got.ProcessorVersion)
	segments, err := m.WorkoutSegments(ctx, stale.ID, alice)
	require.NoError(t, err)
	assert.NotEmpty(t, segments)

	found, err = w.RunOnce(ctx)
	require.NoError(t, err)
	require.True(t, found)
	detect, err = m.Job(ctx, detect.ID, alice)
	require.NoError(t, err)
	assert.Equal(t, models.JobDone, detect.State, "%v", detect.LastError)

	found, err = w.RunOnce(ctx)
	require.NoError(t, err)
	assert.False(t, found)
	n, err = m.EnqueueStaleWorkouts(ctx, c.now())
	require.NoError(t, err)
	assert.Zero(t, n, "now current")
}

func TestUnreadableFileGoesStraightToDead(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	legacyWorkout(t, m, repo, alice, []byte("not a FIT file"))
	w, c := newWorker(m, repo)
	_, err := m.EnqueueReprocessAll(ctx, c.now(), alice)
	require.NoError(t, err)

	_, err = w.RunOnce(ctx)
	require.NoError(t, err)
	job := onlyJob(t, m, alice)
	assert.Equal(t, models.JobDead, job.State)
	assert.EqualValues(t, 1, job.Attempts)
	require.NotNil(t, job.LastError)
	assert.Contains(t, *job.LastError, workouts.ErrUnreadableFile.Error())

	retried, err := m.RetryDeadJob(ctx, job.ID, c.now(), alice)
	require.NoError(t, err)
	assert.Equal(t, models.JobQueued, retried.State)
	assert.Zero(t, retried.Attempts)
	_, err = m.RetryDeadJob(ctx, job.ID, c.now(), alice)
	assert.ErrorIs(t, err, models.ErrJobNotDead)
}

func TestTransientFailuresBackOffThenDie(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	legacyWorkout(t, m, repo, alice, testsupport.EncodeFIT(t, start, fourK))
	// A worker reading from an empty repo can't fetch the content: an outage, not a bad file.
	w, c := newWorker(m, testsupport.NewFileRepo(t))
	_, err := m.EnqueueReprocessAll(ctx, c.now(), alice)
	require.NoError(t, err)

	backoff := 30 * time.Second
	for attempt := 1; attempt < jobs.MaxAttempts; attempt++ {
		found, err := w.RunOnce(ctx)
		require.NoError(t, err)
		require.True(t, found, "attempt %d", attempt)
		job := onlyJob(t, m, alice)
		assert.Equal(t, models.JobQueued, job.State)
		assert.EqualValues(t, attempt, job.Attempts)
		assert.True(t, job.RunAt.Time.Equal(c.now().Add(backoff)), "attempt %d retries at %v, want %v", attempt, job.RunAt.Time, c.now().Add(backoff))

		found, err = w.RunOnce(ctx)
		require.NoError(t, err)
		assert.False(t, found, "not due until the backoff passes")
		c.advance(backoff)
		backoff *= 2
	}

	_, err = w.RunOnce(ctx)
	require.NoError(t, err)
	job := onlyJob(t, m, alice)
	assert.Equal(t, models.JobDead, job.State)
	assert.EqualValues(t, jobs.MaxAttempts, job.Attempts)
}

func TestExpiredLeaseIsClaimedAgain(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	legacyWorkout(t, m, repo, alice, testsupport.EncodeFIT(t, start, fourK))
	_, err := m.EnqueueReprocessAll(ctx, start, alice)
	require.NoError(t, err)

	// A worker that claims and then crashes.
	claimed, err := m.ClaimJob(ctx, start, start.Add(jobs.Lease))
	require.NoError(t, err)
	require.NotNil(t, claimed)

	again, err := m.ClaimJob(ctx, start.Add(time.Minute), start.Add(time.Minute+jobs.Lease))
	require.NoError(t, err)
	assert.Nil(t, again, "still leased")

	later := start.Add(jobs.Lease + time.Second)
	again, err = m.ClaimJob(ctx, later, later.Add(jobs.Lease))
	require.NoError(t, err)
	require.NotNil(t, again)
	assert.Equal(t, claimed.ID, again.ID)
	assert.EqualValues(t, 2, again.Attempts)
}

func TestJobsAreScopedToOwner(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice, bob := createUser(t, m, "alice@example.com"), createUser(t, m, "bob@example.com")
	legacyWorkout(t, m, repo, alice, testsupport.EncodeFIT(t, start, fourK))
	_, err := m.EnqueueReprocessAll(ctx, start, alice)
	require.NoError(t, err)
	job := onlyJob(t, m, alice)

	bobs, err := m.Jobs(ctx, "", 10, bob)
	require.NoError(t, err)
	assert.Empty(t, bobs)
	_, err = m.Job(ctx, job.ID, bob)
	assert.True(t, models.IsNotFound(err))
	n, err := m.EnqueueReprocessAll(ctx, start, bob)
	require.NoError(t, err)
	assert.Zero(t, n, "bob has no workouts")
}

func TestDeletingAWorkoutDropsItsJobs(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	stale := legacyWorkout(t, m, repo, alice, testsupport.EncodeFIT(t, start, fourK))
	_, err := m.EnqueueReprocessAll(ctx, start, alice)
	require.NoError(t, err)

	require.NoError(t, m.DeleteWorkout(ctx, stale.ID, alice))
	remaining, err := m.Jobs(ctx, "", 10, alice)
	require.NoError(t, err)
	assert.Empty(t, remaining)
}

func TestRunStopsWithContext(t *testing.T) {
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	stale := legacyWorkout(t, m, repo, alice, testsupport.EncodeFIT(t, start, fourK))
	w := jobs.NewWorker(m, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	// Run enqueues the stale workout on start and processes it straight away.
	require.Eventually(t, func() bool {
		got, err := m.Workout(context.Background(), stale.ID, alice)
		return err == nil && got.ProcessorVersion == workouts.ProcessorVersion
	}, 5*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't return after cancel")
	}
}

// drain runs jobs until none is due.
func drain(t *testing.T, w *jobs.Worker) {
	t.Helper()
	for {
		found, err := w.RunOnce(t.Context())
		require.NoError(t, err)
		if !found {
			return
		}
	}
}

func upload(t *testing.T, m models.Model, repo filerepo.FileRepo, user *tokens.UserToken, content []byte) *models.Workout {
	t.Helper()
	af, _, err := m.AddActivityFile(t.Context(), bytes.NewReader(content), start, user, repo)
	require.NoError(t, err)
	wo, _, err := m.CreateWorkoutFromActivityFile(t.Context(), af.ID, user, repo)
	require.NoError(t, err)
	return wo
}

func TestCopiesFromAnotherSourceStopCounting(t *testing.T) {
	ctx := context.Background()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	alice := createUser(t, m, "alice@example.com")
	watch := upload(t, m, repo, alice, testsupport.EncodeFITDevice(t, start, fourK, 0, 0, "COROS PACE 3"))
	phone := upload(t, m, repo, alice, testsupport.NewTCX(start.Add(time.Minute), fourK, "Running").Encode(t))
	require.Empty(t, phone.ExcludedReason)
	w, c := newWorker(m, repo)

	require.NoError(t, m.EnqueueDetectRoutes(ctx, c.now(), alice))
	drain(t, w)
	got, err := m.Workout(ctx, phone.ID, alice)
	require.NoError(t, err)
	require.NotNil(t, got.DuplicateOf)
	assert.Equal(t, watch.ID, *got.DuplicateOf)
	assert.Equal(t, "duplicate of the COROS PACE 3 recording", got.ExcludedReason)
	efforts, err := m.BestSegments(ctx, "1k", workouts.SportRunning, 10, models.Window{}, alice)
	require.NoError(t, err)
	require.Len(t, efforts, 1)
	assert.Equal(t, watch.ID, efforts[0].WorkoutID)

	// Without the watch's recording, the phone's counts again.
	require.NoError(t, m.DeleteWorkout(ctx, watch.ID, alice))
	require.NoError(t, m.EnqueueDetectRoutes(ctx, c.now(), alice))
	drain(t, w)
	got, err = m.Workout(ctx, phone.ID, alice)
	require.NoError(t, err)
	assert.Nil(t, got.DuplicateOf)
	assert.Empty(t, got.ExcludedReason)
	efforts, err = m.BestSegments(ctx, "1k", workouts.SportRunning, 10, models.Window{}, alice)
	require.NoError(t, err)
	require.Len(t, efforts, 1)
	assert.Equal(t, phone.ID, efforts[0].WorkoutID)
}

// weatherWorker is a worker that asks a fake Open-Meteo, with one tracked run already uploaded.
func weatherWorker(t *testing.T) (*jobs.Worker, *clock, *testsupport.FakeWeather, models.Model, *tokens.UserToken, *models.Workout) {
	t.Helper()
	m, repo := testsupport.NewModel(t), testsupport.NewFileRepo(t)
	user := createUser(t, m, "alice@example.com")
	fake := testsupport.NewFakeWeather(t)
	w, c := newWorker(m, repo)
	w.Weather = weather.NewOpenMeteo(fake.URL())
	return w, c, fake, m, user, upload(t, m, repo, user, testsupport.EncodeFIT(t, start, fourK))
}

func TestWeatherIsFetchedFromEachSourceWhenItHasIt(t *testing.T) {
	w, c, fake, m, user, wo := weatherWorker(t)

	w.SweepWeather(t.Context())
	drain(t, w)
	assert.Empty(t, fake.Requests(), "no source has a run that just ended")
	_, err := m.WorkoutWeather(t.Context(), wo.ID, user)
	assert.ErrorIs(t, err, models.ErrNoWeather)

	c.advance(25 * time.Hour)
	w.SweepWeather(t.Context())
	drain(t, w)
	found, err := m.WorkoutWeather(t.Context(), wo.ID, user)
	require.NoError(t, err)
	assert.Equal(t, weather.SourceDefault, found.Source)
	assert.InDelta(t, 8.0, found.Start.TemperatureC, 1e-9, "07:00 from the default blend")
	assert.InDelta(t, 8.0+1100.0/3600, found.End.TemperatureC, 1e-9, "18 minutes 20 later")
	assert.InDelta(t, 70, found.Start.HumidityPct, 1e-9)
	assert.True(t, found.FetchedAt.Equal(c.now()))
	requests := fake.Requests()
	require.Len(t, requests, 1)
	assert.Equal(t, "45", requests[0].Get("latitude"))
	assert.Equal(t, "-93", requests[0].Get("longitude"))

	w.SweepWeather(t.Context())
	drain(t, w)
	assert.Len(t, fake.Requests(), 1, "nothing to ask until ERA5 has caught up")

	c.advance(7 * 24 * time.Hour)
	w.SweepWeather(t.Context())
	drain(t, w)
	found, err = m.WorkoutWeather(t.Context(), wo.ID, user)
	require.NoError(t, err)
	assert.Equal(t, weather.SourceERA5, found.Source, "the better source wins once it's there")
	assert.InDelta(t, 7.0, found.Start.TemperatureC, 1e-9)
	require.Len(t, found.Sources, 2, "the first reading is kept beside it")
	assert.Equal(t, weather.SourceDefault, found.Sources[1].Source)
	requests = fake.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, "era5", requests[1].Get("models"))

	w.SweepWeather(t.Context())
	drain(t, w)
	assert.Len(t, fake.Requests(), 2, "a workout with every source is left alone")
}

func TestWeatherOffQueuesNothing(t *testing.T) {
	w, c, fake, m, user, _ := weatherWorker(t)
	w.Weather = nil
	c.advance(30 * 24 * time.Hour)
	w.SweepWeather(t.Context())
	all, err := m.Jobs(t.Context(), "", 10, user)
	require.NoError(t, err)
	assert.Empty(t, all)
	assert.Empty(t, fake.Requests())
}

func TestWeatherNotThereYetIsAskedForAgain(t *testing.T) {
	w, c, fake, m, user, wo := weatherWorker(t)
	fake.SetEmpty(true)
	c.advance(25 * time.Hour)
	w.SweepWeather(t.Context())
	drain(t, w)
	assert.Equal(t, models.JobDone, onlyJob(t, m, user).State, "nothing yet is no failure")
	_, err := m.WorkoutWeather(t.Context(), wo.ID, user)
	assert.ErrorIs(t, err, models.ErrNoWeather)

	fake.SetEmpty(false)
	c.advance(time.Hour)
	w.SweepWeather(t.Context())
	drain(t, w)
	_, err = m.WorkoutWeather(t.Context(), wo.ID, user)
	assert.NoError(t, err)
}

func TestWeatherFailuresDieAndWaitForARetry(t *testing.T) {
	w, c, fake, m, user, wo := weatherWorker(t)
	fake.SetStatus(503)
	c.advance(25 * time.Hour)
	w.SweepWeather(t.Context())
	for range jobs.MaxAttempts {
		drain(t, w)
		c.advance(time.Hour)
	}
	job := onlyJob(t, m, user)
	require.Equal(t, models.JobDead, job.State)
	require.NotNil(t, job.LastError)
	assert.Contains(t, *job.LastError, "503")

	w.SweepWeather(t.Context())
	onlyJob(t, m, user)

	fake.SetStatus(0)
	_, err := m.RetryDeadJob(t.Context(), job.ID, c.now(), user)
	require.NoError(t, err)
	drain(t, w)
	_, err = m.WorkoutWeather(t.Context(), wo.ID, user)
	assert.NoError(t, err)
}

func TestReprocessingQueuesAWeatherFetch(t *testing.T) {
	w, c, fake, m, user, wo := weatherWorker(t)
	c.advance(25 * time.Hour)
	_, err := m.EnqueueReprocessAll(t.Context(), c.now(), user)
	require.NoError(t, err)
	drain(t, w)
	_, err = m.WorkoutWeather(t.Context(), wo.ID, user)
	require.NoError(t, err)

	_, err = m.EnqueueReprocessAll(t.Context(), c.now(), user)
	require.NoError(t, err)
	drain(t, w)
	assert.Len(t, fake.Requests(), 1, "a workout whose span hasn't moved keeps the reading it has")
}
