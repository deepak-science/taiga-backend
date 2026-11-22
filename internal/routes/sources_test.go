package routes_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/coros"
	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/jobs"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/routes"
	"gitea.deepak.science/deepak/taiga/internal/sources"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

// sourcesAPI is the API with COROS turned on against a fake, and a worker to run its sync jobs.
type sourcesAPI struct {
	t      *testing.T
	srv    *httptest.Server
	m      models.Model
	toker  tokens.Toker
	fake   *testsupport.FakeCOROS
	worker *jobs.Worker
	repo   filerepo.FileRepo
	// now is the clock the worker and the connections go by.
	now time.Time
}

func newSourcesAPI(t *testing.T) *sourcesAPI {
	t.Helper()
	m, repo, toker := testsupport.NewModel(t), testsupport.NewFileRepo(t), testsupport.NewToker(t)
	// A little ahead of the real clock, which is what the sync endpoint queues by, so what it queues is always due.
	a := &sourcesAPI{t: t, m: m, repo: repo, toker: toker, fake: testsupport.NewFakeCOROS(t), now: time.Now().UTC().Truncate(time.Second).Add(time.Minute)}
	// The callback URL needs the server's address, which needs a handler first.
	var handler http.Handler
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(a.srv.Close)

	manager, err := sources.New(m, a.fake.Endpoint(), "https://"+frontendHost, a.srv.URL, []byte("test secret"))
	require.NoError(t, err)
	manager.Now = func() time.Time { return a.now }
	handler = routes.New(m, toker, repo, routes.WithSources(manager))
	a.worker = jobs.NewWorker(m, repo)
	a.worker.Sources = manager
	a.worker.Now = func() time.Time { return a.now }
	return a
}

func (a *sourcesAPI) user(email string) *apiClient {
	return newUserClient(a.t, a.srv, a.m, a.toker, email)
}

// begin asks to connect as c and returns the browser holding the flow cookie, and where COROS's sign-in is.
func (a *sourcesAPI) begin(c *apiClient) (*http.Client, string) {
	a.t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(a.t, err)
	browser := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Host == frontendHost {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	req, err := http.NewRequest(http.MethodPost, a.srv.URL+"/sources/coros", nil)
	require.NoError(a.t, err)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := browser.Do(req)
	require.NoError(a.t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(a.t, http.StatusOK, resp.StatusCode)
	var link struct {
		URL string `json:"url"`
	}
	require.NoError(a.t, json.NewDecoder(resp.Body).Decode(&link))
	return browser, link.URL
}

// landed is the query the callback sent the browser back to the frontend's #/account with.
func (a *sourcesAPI) landed(resp *http.Response) url.Values {
	a.t.Helper()
	_ = resp.Body.Close()
	require.Equal(a.t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(a.t, err)
	require.Equal(a.t, frontendHost, loc.Host)
	route, query, _ := strings.Cut(loc.EscapedFragment(), "?")
	require.Equal(a.t, "/account", route)
	params, err := url.ParseQuery(query)
	require.NoError(a.t, err)
	return params
}

// connect takes c through the whole sign-in at the fake.
func (a *sourcesAPI) connect(c *apiClient) {
	a.t.Helper()
	browser, address := a.begin(c)
	resp, err := browser.Get(address)
	require.NoError(a.t, err)
	params := a.landed(resp)
	require.Empty(a.t, params.Get("source_error"))
	require.Equal(a.t, "coros", params.Get("source"))
}

type sourcesBody struct {
	Kinds []struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"kinds"`
	Connections []struct {
		ID             int32      `json:"id"`
		Kind           string     `json:"kind"`
		Name           string     `json:"name"`
		LastSyncedAt   *time.Time `json:"last_synced_at"`
		LastError      string     `json:"last_error"`
		NeedsReconnect bool       `json:"needs_reconnect"`
		Activities     int64      `json:"activities"`
	} `json:"connections"`
}

func (a *sourcesAPI) sources(c *apiClient) sourcesBody {
	a.t.Helper()
	status, body := c.get("/sources")
	require.Equal(a.t, http.StatusOK, status)
	var out sourcesBody
	require.NoError(a.t, json.Unmarshal(body, &out), string(body))
	return out
}

// sync runs every job that's due, the queued sync among them.
func (a *sourcesAPI) sync() {
	a.t.Helper()
	for {
		found, err := a.worker.RunOnce(a.t.Context())
		require.NoError(a.t, err)
		if !found {
			return
		}
	}
}

// syncAgain queues a sync as the user asking for one would, and runs it.
func (a *sourcesAPI) syncAgain(c *apiClient) {
	a.t.Helper()
	status, body := c.post(fmt.Sprintf("/sources/%d/sync", a.sources(c).Connections[0].ID))
	require.Equal(a.t, http.StatusAccepted, status, string(body))
	a.sync()
}

func (a *sourcesAPI) workouts(c *apiClient) []models.Workout {
	a.t.Helper()
	status, body := c.get("/workouts")
	require.Equal(a.t, http.StatusOK, status)
	var out []models.Workout
	require.NoError(a.t, json.Unmarshal(body, &out))
	return out
}

func (a *sourcesAPI) fileRequests() int {
	_, _, requests, _, _ := a.fake.Counts()
	return requests
}

// run is a fake activity whose file is a real run, a different one for each n.
func (a *sourcesAPI) run(n int, ago time.Duration) testsupport.FakeActivity {
	start := a.now.Add(-ago)
	return testsupport.FakeActivity{
		ID:        fmt.Sprintf("48187555771007%04d", n),
		SportType: 100,
		Start:     start,
		End:       start.Add(20 * time.Minute),
		File:      testsupport.EncodeFIT(a.t, routeDay.AddDate(0, 0, n), []testsupport.Split{{Meters: 1000, Seconds: 300}}),
	}
}

// junk is a fake activity whose file isn't one taiga can read: cheap to make many of.
func (a *sourcesAPI) junk(n int, ago time.Duration) testsupport.FakeActivity {
	start := a.now.Add(-ago)
	return testsupport.FakeActivity{ID: fmt.Sprintf("9%d", n), SportType: 1004, Name: "Pickleball", Start: start, End: start.Add(time.Minute), File: fmt.Appendf(nil, "not a FIT file %d", n)}
}

func TestConnectingImportsTheLastWeek(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.fake.Add(a.run(1, 2*time.Hour), a.run(2, 3*24*time.Hour), a.junk(3, 26*time.Hour), a.run(4, 30*24*time.Hour))

	before := a.sources(alice)
	require.Len(t, before.Kinds, 1)
	assert.Equal(t, "coros", before.Kinds[0].Kind)
	assert.Equal(t, "COROS", before.Kinds[0].Name)
	assert.Empty(t, before.Connections)

	a.connect(alice)
	connected := a.sources(alice)
	require.Len(t, connected.Connections, 1)
	assert.Equal(t, "COROS", connected.Connections[0].Name)
	assert.Nil(t, connected.Connections[0].LastSyncedAt, "the first sync is queued, not run in the callback")
	assert.Empty(t, a.workouts(alice))

	a.sync()
	assert.Len(t, a.workouts(alice), 2, "the two runs this week; last month's is left to an export")
	synced := a.sources(alice).Connections[0]
	assert.Equal(t, int64(3), synced.Activities, "the unreadable one is dealt with too")
	require.NotNil(t, synced.LastSyncedAt)
	assert.Empty(t, synced.LastError)
	assert.Equal(t, 3, a.fileRequests())

	// The unreadable file is kept where other failed uploads are.
	status, body := alice.get("/activity_files?without_workout=true")
	require.Equal(t, http.StatusOK, status)
	var pending []models.ActivityFile
	require.NoError(t, json.Unmarshal(body, &pending))
	assert.Len(t, pending, 1)

	// Nothing new: COROS is asked what there is, and for no files.
	_, lists, _, _, _ := a.fake.Counts()
	a.syncAgain(alice)
	_, listsAfter, _, _, _ := a.fake.Counts()
	assert.Equal(t, lists+1, listsAfter)
	assert.Len(t, a.workouts(alice), 2)
	assert.Equal(t, 3, a.fileRequests())

	// A run after the first sync comes in with the next.
	a.now = a.now.Add(time.Hour)
	a.fake.Add(a.run(5, 10*time.Minute))
	a.syncAgain(alice)
	assert.Len(t, a.workouts(alice), 3)
	assert.Equal(t, 4, a.fileRequests())
}

func TestAFileAlreadyUploadedIsNotImportedTwice(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	run := a.run(1, 2*time.Hour)
	a.fake.Add(run)
	alice.uploadWorkout(run.File)

	a.connect(alice)
	a.sync()
	assert.Len(t, a.workouts(alice), 1)
	assert.Equal(t, int64(1), a.sources(alice).Connections[0].Activities)
}

func TestADeletedImportStaysDeleted(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.fake.Add(a.run(1, 2*time.Hour))
	a.connect(alice)
	a.sync()
	imported := a.workouts(alice)
	require.Len(t, imported, 1)

	status, _ := alice.delete(fmt.Sprintf("/workouts/%d", imported[0].ID))
	require.Equal(t, http.StatusNoContent, status)
	status, _ = alice.delete(fmt.Sprintf("/activity_files/%d", *imported[0].ActivityFileID))
	require.Equal(t, http.StatusNoContent, status)

	a.syncAgain(alice)
	assert.Empty(t, a.workouts(alice))
	assert.Equal(t, 1, a.fileRequests())

	// Nor does connecting afresh bring it back.
	status, _ = alice.delete(fmt.Sprintf("/sources/%d", a.sources(alice).Connections[0].ID))
	require.Equal(t, http.StatusNoContent, status)
	a.connect(alice)
	a.sync()
	assert.Empty(t, a.workouts(alice))
	assert.Equal(t, 1, a.fileRequests())
}

func TestSyncStopsAtTheDailyLimitAndCarriesOn(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	const more = 3
	for n := range models.SourceDailyFiles + more {
		a.fake.Add(a.junk(n, time.Duration(n+1)*time.Hour))
	}
	a.connect(alice)
	a.sync()
	assert.Equal(t, models.SourceDailyFiles, a.fileRequests(), "COROS allows 50 a day, and some are left for the user")
	conn := a.sources(alice).Connections[0]
	assert.Equal(t, int64(models.SourceDailyFiles), conn.Activities)
	assert.Empty(t, conn.LastError, "stopping at the limit isn't a failure")

	// Asking again the same day asks for no more files.
	a.syncAgain(alice)
	assert.Equal(t, models.SourceDailyFiles, a.fileRequests())

	// A day on, the rest come: the oldest were taken first, so none was skipped.
	a.now = a.now.Add(25 * time.Hour)
	a.syncAgain(alice)
	assert.Equal(t, models.SourceDailyFiles+more, a.fileRequests())
	assert.Equal(t, int64(models.SourceDailyFiles+more), a.sources(alice).Connections[0].Activities)
}

func TestCOROSsOwnLimitIsNotAFailure(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	// The user spent most of the day's files elsewhere.
	a.fake.DailyLimit = 1
	a.fake.Add(a.run(1, 3*time.Hour), a.run(2, 2*time.Hour))
	a.connect(alice)
	a.sync()
	assert.Len(t, a.workouts(alice), 1)
	assert.Empty(t, a.sources(alice).Connections[0].LastError)

	a.fake.DailyLimit = 0
	a.now = a.now.Add(time.Hour)
	a.syncAgain(alice)
	assert.Len(t, a.workouts(alice), 2)
}

func TestAnActivityStillBeingRecordedWaits(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	running := a.run(1, 30*time.Minute)
	running.InProgress = true
	a.fake.Add(running, a.run(2, 5*time.Minute))
	a.connect(alice)
	a.sync()
	assert.Len(t, a.workouts(alice), 1, "the one after it isn't held up")

	// Two days on it's long finished, and further back than a sync would otherwise look.
	a.now = a.now.Add(48 * time.Hour)
	a.syncAgain(alice)
	assert.Len(t, a.workouts(alice), 1, "COROS still says it's in progress")

	a.fake.Finish(running.ID)
	a.syncAgain(alice)
	assert.Len(t, a.workouts(alice), 2)
}

func TestARevokedGrantAsksToReconnect(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.fake.Add(a.run(1, 2*time.Hour))
	a.connect(alice)
	a.sync()

	a.fake.RevokeAll()
	a.fake.Add(a.run(2, time.Hour))
	a.syncAgain(alice)
	conn := a.sources(alice).Connections[0]
	assert.True(t, conn.NeedsReconnect)
	assert.NotEmpty(t, conn.LastError)
	assert.Len(t, a.workouts(alice), 1)

	// It isn't retried, by the job or the hourly sweep or by asking.
	status, body := alice.get("/jobs?state=dead")
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, "[]", string(body))
	a.now = a.now.Add(2 * time.Hour)
	_, lists, _, _, _ := a.fake.Counts()
	a.worker.SweepSources(t.Context())
	a.sync()
	_, listsAfter, _, _, _ := a.fake.Counts()
	assert.Equal(t, lists, listsAfter)
	status, _ = alice.post(fmt.Sprintf("/sources/%d/sync", conn.ID))
	assert.Equal(t, http.StatusConflict, status)

	// Connecting again picks up where it left off.
	a.connect(alice)
	a.sync()
	conn = a.sources(alice).Connections[0]
	assert.False(t, conn.NeedsReconnect)
	assert.Empty(t, conn.LastError)
	assert.Len(t, a.workouts(alice), 2)
	assert.Equal(t, 2, a.fileRequests(), "the run it already had wasn't fetched again")
}

func TestRefreshedTokensAreKept(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	// Every sync has to refresh, and each refresh token works once: losing one would end the connection.
	a.fake.AccessTTL = time.Second
	a.connect(alice)
	for n := range 3 {
		a.fake.Add(a.run(n, time.Minute))
		a.syncAgain(alice)
		conn := a.sources(alice).Connections[0]
		require.False(t, conn.NeedsReconnect, conn.LastError)
		require.Len(t, a.workouts(alice), n+1)
		a.now = a.now.Add(time.Hour)
	}
}

func TestAnOutageIsRetriedAndShown(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.fake.Add(a.run(1, 2*time.Hour))
	a.connect(alice)
	a.fake.ListReply = "Sorry, COROS API is temporarily unavailable. Please try again later."
	a.sync()
	conn := a.sources(alice).Connections[0]
	assert.Contains(t, conn.LastError, "doesn't know how to read")
	assert.False(t, conn.NeedsReconnect, "the grant is fine; COROS isn't")
	assert.Empty(t, a.workouts(alice))

	// The job waits and tries again by itself.
	a.fake.ListReply = ""
	a.now = a.now.Add(time.Minute)
	a.sync()
	assert.Len(t, a.workouts(alice), 1)
	assert.Empty(t, a.sources(alice).Connections[0].LastError)
}

func TestTheSweepSyncsEveryConnection(t *testing.T) {
	a := newSourcesAPI(t)
	alice, bob := a.user("alice@example.com"), a.user("bob@example.com")
	a.connect(alice)
	a.connect(bob)
	a.sync()
	registrations, lists, _, _, _ := a.fake.Counts()
	assert.Equal(t, 1, registrations, "the deployment registers once, however many users connect")

	a.now = a.now.Add(time.Hour)
	a.worker.SweepSources(t.Context())
	a.sync()
	_, listsAfter, _, _, _ := a.fake.Counts()
	assert.Equal(t, lists+2, listsAfter)
}

func TestConnectionsAreTheUsersOwn(t *testing.T) {
	a := newSourcesAPI(t)
	alice, bob := a.user("alice@example.com"), a.user("bob@example.com")
	a.fake.Add(a.run(1, 2*time.Hour))
	a.connect(alice)
	a.sync()
	id := a.sources(alice).Connections[0].ID

	assert.Empty(t, a.sources(bob).Connections)
	assert.Empty(t, a.workouts(bob))
	status, _ := bob.post(fmt.Sprintf("/sources/%d/sync", id))
	assert.Equal(t, http.StatusNotFound, status)
	status, _ = bob.delete(fmt.Sprintf("/sources/%d", id))
	assert.Equal(t, http.StatusNotFound, status)
	assert.Len(t, a.sources(alice).Connections, 1)
}

func TestDisconnectingEndsTheGrantAndKeepsTheWorkouts(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.fake.Add(a.run(1, 2*time.Hour))
	a.connect(alice)
	a.sync()

	status, _ := alice.delete(fmt.Sprintf("/sources/%d", a.sources(alice).Connections[0].ID))
	require.Equal(t, http.StatusNoContent, status)
	assert.Empty(t, a.sources(alice).Connections)
	assert.Len(t, a.workouts(alice), 1)
	_, _, _, _, revocations := a.fake.Counts()
	assert.Equal(t, 1, revocations)

	// Nothing is left to sync.
	a.now = a.now.Add(time.Hour)
	_, lists, _, _, _ := a.fake.Counts()
	a.worker.SweepSources(t.Context())
	a.sync()
	_, listsAfter, _, _, _ := a.fake.Counts()
	assert.Equal(t, lists, listsAfter)
}

func TestWhatIsStoredIsSealed(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.connect(alice)
	user, err := a.toker.DecodeTokenString(alice.token)
	require.NoError(t, err)

	conn, err := a.m.SourceConnectionByKind(t.Context(), coros.Kind, user)
	require.NoError(t, err)
	// The fake's tokens and secrets all say what they are.
	for _, plain := range []string{"access-", "refresh-"} {
		assert.NotContains(t, conn.Credentials, plain)
	}
	client, err := a.m.SourceClientByID(t.Context(), conn.ClientID)
	require.NoError(t, err)
	for _, plain := range []string{"client-", "oauth2"} {
		assert.NotContains(t, client.Registration, plain)
	}

	// And the API never shows them.
	_, body := alice.get("/sources")
	for _, plain := range []string{"credentials", "access", "refresh", conn.Credentials} {
		assert.NotContains(t, string(body), plain)
	}
}

func TestCallbacksThatDoNotConnect(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")

	t.Run("declined at COROS", func(t *testing.T) {
		browser, address := a.begin(alice)
		a.fake.DenyNext = "access_denied"
		resp, err := browser.Get(address)
		require.NoError(t, err)
		assert.Contains(t, a.landed(resp).Get("source_error"), "declined")
	})
	t.Run("no flow cookie", func(t *testing.T) {
		_, address := a.begin(alice)
		// Another browser entirely, as a link forwarded to someone else would be.
		other := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Host == frontendHost {
				return http.ErrUseLastResponse
			}
			return nil
		}}
		resp, err := other.Get(address)
		require.NoError(t, err)
		assert.Contains(t, a.landed(resp).Get("source_error"), "try again")
	})
	t.Run("another flow's state", func(t *testing.T) {
		browser, _ := a.begin(alice)
		resp, err := browser.Get(a.srv.URL + "/sources/coros/callback?state=forged&code=whatever")
		require.NoError(t, err)
		assert.Contains(t, a.landed(resp).Get("source_error"), "try again")
	})
	t.Run("a callback replayed", func(t *testing.T) {
		browser, address := a.begin(alice)
		var callback string
		browser.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if strings.HasPrefix(req.URL.String(), a.srv.URL) {
				callback = req.URL.String()
			}
			if req.URL.Host == frontendHost {
				return http.ErrUseLastResponse
			}
			return nil
		}
		resp, err := browser.Get(address)
		require.NoError(t, err)
		require.Empty(t, a.landed(resp).Get("source_error"))

		resp, err = browser.Get(callback)
		require.NoError(t, err)
		assert.NotEmpty(t, a.landed(resp).Get("source_error"))
	})
	t.Run("a source there isn't", func(t *testing.T) {
		resp, err := http.Get(a.srv.URL + "/sources/garmin/callback?state=x&code=y")
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		status, _ := alice.post("/sources/garmin")
		assert.Equal(t, http.StatusNotFound, status)
	})

	assert.Empty(t, a.sources(alice).Connections[1:], "only the replayed flow's first pass connected anything")
}

func TestSourcesNeedSigningIn(t *testing.T) {
	a := newSourcesAPI(t)
	anonymous := &apiClient{t: t, srv: a.srv}
	for _, path := range []string{"/sources"} {
		status, _ := anonymous.get(path)
		assert.Equal(t, http.StatusUnauthorized, status)
	}
	status, _ := anonymous.post("/sources/coros")
	assert.Equal(t, http.StatusUnauthorized, status)
	status, _ = anonymous.post("/sources/1/sync")
	assert.Equal(t, http.StatusUnauthorized, status)
	status, _ = anonymous.delete("/sources/1")
	assert.Equal(t, http.StatusUnauthorized, status)
}

func TestSourcesOff(t *testing.T) {
	srv, m, toker := newAPI(t)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")

	status, body := alice.get("/sources")
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `{"kinds": [], "connections": []}`, string(body))
	status, _ = alice.post("/sources/coros")
	assert.Equal(t, http.StatusNotFound, status)
	resp, err := http.Get(srv.URL + "/sources/coros/callback?state=x&code=y")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestAskingToSyncRevivesADeadOne(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.fake.Add(a.run(1, 2*time.Hour))
	a.connect(alice)
	a.fake.ListReply = "Sorry, COROS API is temporarily unavailable. Please try again later."
	// Long enough for every retry to come due and fail.
	for range jobs.MaxAttempts {
		a.sync()
		a.now = a.now.Add(time.Hour)
	}
	status, body := alice.get("/jobs?state=dead")
	require.Equal(t, http.StatusOK, status)
	var dead []models.Job
	require.NoError(t, json.Unmarshal(body, &dead))
	require.Len(t, dead, 1)

	// The sweep leaves it be, so an outage doesn't pile up dead jobs.
	a.fake.ListReply = ""
	a.worker.SweepSources(t.Context())
	a.sync()
	assert.Empty(t, a.workouts(alice))

	a.syncAgain(alice)
	assert.Len(t, a.workouts(alice), 1)
	_, body = alice.get("/jobs?state=dead")
	assert.JSONEq(t, "[]", string(body))

	// And the sweep takes over again.
	a.now = a.now.Add(time.Hour)
	a.fake.Add(a.run(2, time.Minute))
	a.worker.SweepSources(t.Context())
	a.sync()
	assert.Len(t, a.workouts(alice), 2)
}

func TestARefreshIsKeptWhenCOROSThenFails(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.fake.Add(a.run(1, 2*time.Hour))
	// The first sync finds its token run out, refreshes, and then can't reach the server.
	a.fake.AccessTTL = time.Second
	a.connect(alice)
	a.fake.AccessTTL = 30 * 24 * time.Hour
	a.fake.Down = true
	a.sync()
	conn := a.sources(alice).Connections[0]
	require.NotEmpty(t, conn.LastError)
	require.False(t, conn.NeedsReconnect)

	// The retry has the new refresh token; with the old one, spent, COROS would turn it away.
	a.fake.Down = false
	a.now = a.now.Add(time.Minute)
	a.sync()
	conn = a.sources(alice).Connections[0]
	assert.False(t, conn.NeedsReconnect, conn.LastError)
	assert.Len(t, a.workouts(alice), 1)
}

// stubSource is a source that lists what it's given and hands over files as told.
type stubSource struct {
	activities []coros.Activity
	files      func(coros.Activity) ([][]byte, error)
	asked      int
}

func (s *stubSource) List(_ context.Context, from, to time.Time) ([]coros.Activity, error) {
	return s.activities, nil
}

func (s *stubSource) Files(_ context.Context, a coros.Activity) ([][]byte, error) {
	s.asked++
	return s.files(a)
}

func (s *stubSource) Detail(context.Context, coros.Activity) (*coros.Detail, error) {
	return &coros.Detail{Reply: "Activity Details"}, nil
}

func (a *sourcesAPI) connection(c *apiClient) (*models.SourceConnection, *tokens.UserToken) {
	a.t.Helper()
	user, err := a.toker.DecodeTokenString(c.token)
	require.NoError(a.t, err)
	conn, err := a.m.SourceConnectionByKind(a.t.Context(), coros.Kind, user)
	require.NoError(a.t, err)
	return conn, user
}

func TestLinksIssuedCountEvenWhenTheImportFails(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.connect(alice)
	a.sync()
	conn, user := a.connection(alice)
	repo := testsupport.NewFileRepo(t)
	activity := coros.Activity{ID: "1", SportType: 100, Start: a.now.Add(-time.Hour)}

	// COROS issued the day's worth of links and then none would download.
	failing := &stubSource{activities: []coros.Activity{activity}, files: func(coros.Activity) ([][]byte, error) {
		return nil, &coros.SpentError{Files: models.SourceDailyFiles, Err: errors.New("status 500")}
	}}
	_, err := a.m.SyncSource(t.Context(), conn, failing, repo, a.now, user)
	require.Error(t, err)

	working := &stubSource{activities: []coros.Activity{activity}, files: func(coros.Activity) ([][]byte, error) {
		return [][]byte{a.run(1, time.Hour).File}, nil
	}}
	res, err := a.m.SyncSource(t.Context(), conn, working, repo, a.now, user)
	require.NoError(t, err)
	assert.True(t, res.LimitReached, "the links that failed were spent all the same")
	assert.Zero(t, working.asked)

	// A day on they no longer count, and the activity that failed is still waiting.
	res, err = a.m.SyncSource(t.Context(), conn, working, repo, a.now.Add(25*time.Hour), user)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Imported)
}

func TestRoutesAreDetectedForWhatAFailedSyncBroughtIn(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.connect(alice)
	a.sync()
	conn, user := a.connection(alice)
	queued := func() []string {
		_, body := alice.get("/jobs?state=queued")
		var jobs []models.Job
		require.NoError(t, json.Unmarshal(body, &jobs), string(body))
		var kinds []string
		for _, job := range jobs {
			kinds = append(kinds, job.Kind)
		}
		return kinds
	}
	require.NotContains(t, queued(), "detect_routes")

	// The first activity comes in, and the second fails the sync.
	src := &stubSource{
		activities: []coros.Activity{{ID: "1", SportType: 100, Start: a.now.Add(-2 * time.Hour)}, {ID: "2", SportType: 100, Start: a.now.Add(-time.Hour)}},
		files: func(activity coros.Activity) ([][]byte, error) {
			if activity.ID == "2" {
				return nil, errors.New("COROS is down")
			}
			return [][]byte{a.run(1, 2*time.Hour).File}, nil
		},
	}
	res, err := a.m.SyncSource(t.Context(), conn, src, testsupport.NewFileRepo(t), a.now, user)
	require.Error(t, err)
	require.Equal(t, 1, res.Imported)
	assert.Contains(t, queued(), "detect_routes", "the next sync skips the one that came in, so it wouldn't queue this")
}

func TestARegistrationSealedUnderAnOldKeyIsReplaced(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.connect(alice)
	registrations := func() int {
		n, _, _, _, _ := a.fake.Counts()
		return n
	}
	require.Equal(t, 1, registrations())
	_, user := a.connection(alice)

	// The token signing key was replaced, and with it the key everything stored is sealed under.
	rotated, err := sources.New(a.m, a.fake.Endpoint(), "https://"+frontendHost, a.srv.URL, []byte("another secret"))
	require.NoError(t, err)
	address, err := rotated.Begin(t.Context(), httptest.NewRecorder(), user)
	require.NoError(t, err, "connecting has to work again without anyone editing the database")
	assert.NotEmpty(t, address)
	assert.Equal(t, 2, registrations())

	// Once is enough.
	_, err = rotated.Begin(t.Context(), httptest.NewRecorder(), user)
	require.NoError(t, err)
	assert.Equal(t, 2, registrations())
}

func TestAWindowTooFullForOneListIsTakenInPieces(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	// More in the week than COROS lists at once, an activity every 45 minutes.
	const listed = 210
	for n := range listed {
		a.fake.Add(a.junk(n, time.Duration(n+1)*45*time.Minute))
	}
	a.connect(alice)
	a.sync()
	conn := a.sources(alice).Connections[0]
	assert.Empty(t, conn.LastError, "a full list is asked for again in shorter stretches, not failed on for good")
	assert.Equal(t, int64(models.SourceDailyFiles), conn.Activities)
}

func TestAnActivityWithNoStartDoesNotWalkTheCursorBack(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.connect(alice)
	a.sync()
	conn, user := a.connection(alice)
	through := conn.SyncedThrough.Time

	// Still being recorded, and listed without its times.
	src := &stubSource{activities: []coros.Activity{{ID: "1", SportType: 100}}, files: func(coros.Activity) ([][]byte, error) {
		return nil, coros.ErrInProgress
	}}
	for range 3 {
		_, err := a.m.SyncSource(t.Context(), conn, src, testsupport.NewFileRepo(t), a.now, user)
		require.NoError(t, err)
		conn, _ = a.connection(alice)
		assert.True(t, conn.SyncedThrough.Time.Equal(through), "each sync would otherwise start a day before the last: %v", conn.SyncedThrough.Time)
	}
}

func TestASyncAfterTheKeyChangedAsksToReconnect(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.connect(alice)
	a.sync()

	rotated, err := sources.New(a.m, a.fake.Endpoint(), "https://"+frontendHost, a.srv.URL, []byte("another secret"))
	require.NoError(t, err)
	rotated.Now = func() time.Time { return a.now }
	a.worker.Sources = rotated
	a.syncAgain(alice)
	conn := a.sources(alice).Connections[0]
	assert.True(t, conn.NeedsReconnect, "retrying can't read what's stored; connecting again is the way out")
	_, body := alice.get("/jobs?state=dead")
	assert.JSONEq(t, "[]", string(body))
}

func TestTheFirstSyncGoesBackAWeekAndNoFurther(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.fake.Add(a.run(1, 7*24*time.Hour-time.Hour), a.run(2, 7*24*time.Hour+12*time.Hour))
	a.connect(alice)
	a.sync()
	assert.Len(t, a.workouts(alice), 1, "the one from seven and a half days ago is left to an export")
	assert.Equal(t, 1, a.fileRequests())
}

func TestOneFileMakesOneWorkoutHoweverManyAsk(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	_, file := alice.uploadFile(a.run(1, time.Hour).File)
	user, err := a.toker.DecodeTokenString(alice.token)
	require.NoError(t, err)
	repo := a.repo

	// A sync and an upload of the same run, and a few more for good measure.
	var wg sync.WaitGroup
	created := make([]bool, 6)
	for i := range created {
		wg.Go(func() {
			_, made, err := a.m.CreateWorkoutFromActivityFile(t.Context(), file.ID, user, repo)
			assert.NoError(t, err)
			created[i] = made
		})
	}
	wg.Wait()
	made := 0
	for _, c := range created {
		if c {
			made++
		}
	}
	assert.Equal(t, 1, made)
	assert.Len(t, a.workouts(alice), 1)
}

func TestTwoReplacingARegistrationKeepTheFirst(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.connect(alice)
	conn, _ := a.connection(alice)
	row, err := a.m.SourceClientByID(t.Context(), conn.ClientID)
	require.NoError(t, err)

	// Both read the same unreadable registration and registered anew; the second to write finds it gone.
	first, err := a.m.ReplaceSourceClient(t.Context(), row.ID, row.Registration, "first")
	require.NoError(t, err)
	assert.Equal(t, "first", first.Registration)
	second, err := a.m.ReplaceSourceClient(t.Context(), row.ID, row.Registration, "second")
	require.NoError(t, err)
	assert.Equal(t, "first", second.Registration, "a sign-in started with the first still finds it at its callback")
}
