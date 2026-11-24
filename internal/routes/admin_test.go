package routes_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/routes"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

func newAdminAPI(t *testing.T, opts ...routes.Option) (*httptest.Server, models.Model, tokens.Toker) {
	m, repo, toker := testsupport.NewModel(t), testsupport.NewFileRepo(t), testsupport.NewToker(t)
	srv := httptest.NewServer(routes.New(m, toker, repo, opts...))
	t.Cleanup(srv.Close)
	return srv, m, toker
}

func me(t *testing.T, c *apiClient) (admin bool) {
	t.Helper()
	status, body := c.get("/auth/me")
	require.Equal(t, http.StatusOK, status, string(body))
	var resp struct {
		Admin bool `json:"admin"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	return resp.Admin
}

func TestAdminRoutesAreHiddenFromOthers(t *testing.T) {
	srv, m, toker := newAdminAPI(t)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	bob := newUserClient(t, srv, m, toker, "bob@example.com")
	_, err := m.SetAdmin(t.Context(), "alice@example.com", true)
	require.NoError(t, err)

	paths := []string{"/admin/deployment", "/admin/accounts", "/admin/jobs", "/admin/sources"}
	for _, p := range paths {
		status, _ := bob.get(p)
		assert.Equal(t, http.StatusNotFound, status, "%s for someone else", p)
		status, _ = (&apiClient{t: t, srv: srv}).get(p)
		assert.Equal(t, http.StatusUnauthorized, status, "%s with no token", p)
		status, body := alice.get(p)
		assert.Equal(t, http.StatusOK, status, "%s for the admin: %s", p, body)
	}
	status, _ := bob.postJSON("/admin/reprocess", `{}`)
	assert.Equal(t, http.StatusNotFound, status)

	assert.True(t, me(t, alice))
	assert.False(t, me(t, bob))

	t.Run("revoking works at once, with the same token", func(t *testing.T) {
		_, err := m.SetAdmin(t.Context(), "alice@example.com", false)
		require.NoError(t, err)
		status, _ := alice.get("/admin/accounts")
		assert.Equal(t, http.StatusNotFound, status)
		assert.False(t, me(t, alice))
		_, err = m.SetAdmin(t.Context(), "alice@example.com", true)
		require.NoError(t, err)
	})
}

func TestConfiguredAdminsNeedAVerifiedEmail(t *testing.T) {
	srv, m, toker := newAdminAPI(t, routes.WithAdmins(models.NewAdmins([]string{" Carol@Example.com ", "dave@example.com"})))
	// Open registration doesn't verify an email, so this is anyone claiming to be dave.
	dave := newUserClient(t, srv, m, toker, "dave@example.com")
	resp, err := m.CreateUser(t.Context(), &models.CreateUserRequest{Email: "carol@example.com", DisplayName: "Carol", Password: "password123", EmailVerified: true})
	require.NoError(t, err)
	token, err := toker.EncodeUser(&tokens.UserToken{ID: resp.Id, Email: resp.Email})
	require.NoError(t, err)
	carol := &apiClient{t: t, srv: srv, token: token}

	assert.True(t, me(t, carol))
	status, _ := carol.get("/admin/accounts")
	assert.Equal(t, http.StatusOK, status)
	assert.False(t, me(t, dave))
	status, _ = dave.get("/admin/accounts")
	assert.Equal(t, http.StatusNotFound, status)
}

type adminAccount struct {
	ID                  int32      `json:"id"`
	Email               string     `json:"email"`
	Admin               bool       `json:"admin"`
	Password            bool       `json:"password"`
	Identities          []string   `json:"identities"`
	Workouts            int64      `json:"workouts"`
	Files               int64      `json:"files"`
	FilesWithoutWorkout int64      `json:"files_without_workout"`
	CLISessions         int64      `json:"cli_sessions"`
	LastSeenAt          *time.Time `json:"last_seen_at"`
}

func adminAccounts(t *testing.T, c *apiClient) map[string]adminAccount {
	t.Helper()
	status, body := c.get("/admin/accounts")
	require.Equal(t, http.StatusOK, status, string(body))
	var list []adminAccount
	require.NoError(t, json.Unmarshal(body, &list))
	out := map[string]adminAccount{}
	for _, a := range list {
		out[a.Email] = a
	}
	return out
}

func TestAdminAccounts(t *testing.T) {
	srv, m, toker := newAdminAPI(t)
	root := newUserClient(t, srv, m, toker, "root@example.com")
	_, err := m.SetAdmin(t.Context(), "root@example.com", true)
	require.NoError(t, err)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	alice.uploadWorkout(testsupport.EncodeFIT(t, routeDay, []testsupport.Split{{Meters: 2000, Seconds: 600}}))
	status, _ := alice.uploadFile([]byte("not an activity file"))
	require.Equal(t, http.StatusCreated, status)

	got := adminAccounts(t, root)
	require.Len(t, got, 2)
	assert.True(t, got["root@example.com"].Admin)
	a := got["alice@example.com"]
	assert.False(t, a.Admin)
	assert.True(t, a.Password)
	assert.Equal(t, []string{}, a.Identities)
	assert.Equal(t, int64(1), a.Workouts)
	assert.Equal(t, int64(2), a.Files)
	assert.Equal(t, int64(1), a.FilesWithoutWorkout)
	assert.Nil(t, a.LastSeenAt, "the test's token wasn't issued by signing in")

	t.Run("signing in is seen", func(t *testing.T) {
		status, body := (&apiClient{t: t, srv: srv}).postJSON("/auth/tokens", `{"email":"alice@example.com","password":"password123"}`)
		require.Equal(t, http.StatusOK, status, string(body))
		assert.NotNil(t, adminAccounts(t, root)["alice@example.com"].LastSeenAt)
	})

	t.Run("nothing an admin sees is the account's own data", func(t *testing.T) {
		_, body := root.get("/admin/accounts")
		for _, secret := range []string{"password123", "$2a$", "start_time", "file_repo_hash"} {
			assert.NotContains(t, string(body), secret)
		}
	})

	t.Run("creating an account", func(t *testing.T) {
		status, body := root.postJSON("/admin/accounts", `{"email":"erin@example.com","password":"long enough"}`)
		require.Equal(t, http.StatusCreated, status, string(body))
		erin := adminAccounts(t, root)["erin@example.com"]
		assert.False(t, erin.Admin, "never through the API")

		status, _ = root.postJSON("/admin/accounts", `{"email":"Erin@Example.com","password":"long enough"}`)
		assert.Equal(t, http.StatusConflict, status)
		status, _ = root.postJSON("/admin/accounts", `{"email":"frank@example.com","password":"short"}`)
		assert.Equal(t, http.StatusBadRequest, status)
		status, _ = root.postJSON("/admin/accounts", `{"password":"long enough"}`)
		assert.Equal(t, http.StatusBadRequest, status)

		status, body = (&apiClient{t: t, srv: srv}).postJSON("/auth/tokens", `{"email":"erin@example.com","password":"long enough"}`)
		assert.Equal(t, http.StatusOK, status, string(body))
	})

	t.Run("ending an account's CLI sessions", func(t *testing.T) {
		cli := &apiClient{t: t, srv: srv}
		start := startDevice(t, cli, `{"client_name":"laptop"}`)
		status, body := alice.postJSON("/auth/device/approve", fmt.Sprintf(`{"user_code":%q}`, start.UserCode))
		require.Equal(t, http.StatusNoContent, status, string(body))
		status, body = cli.postJSON("/auth/device/token", fmt.Sprintf(`{"device_code":%q}`, start.DeviceCode))
		require.Equal(t, http.StatusOK, status, string(body))
		var grant cliTokens
		require.NoError(t, json.Unmarshal(body, &grant))
		assert.Equal(t, int64(1), adminAccounts(t, root)["alice@example.com"].CLISessions)

		status, body = root.delete(fmt.Sprintf("/admin/accounts/%d/sessions", a.ID))
		require.Equal(t, http.StatusOK, status, string(body))
		assert.JSONEq(t, `{"ended":1}`, string(body))
		status, body = cli.postJSON("/auth/tokens/refresh", fmt.Sprintf(`{"refresh_token":%q}`, grant.RefreshToken))
		assert.Equal(t, "invalid_grant", oauthErrorOf(t, status, body))

		status, _ = root.delete("/admin/accounts/9999/sessions")
		assert.Equal(t, http.StatusNotFound, status)
	})
}

type adminJob struct {
	ID        int32   `json:"id"`
	UserID    int32   `json:"user_id"`
	Email     string  `json:"email"`
	Kind      string  `json:"kind"`
	State     string  `json:"state"`
	LastError *string `json:"last_error"`
}

func adminJobs(t *testing.T, c *apiClient, query string) []adminJob {
	t.Helper()
	status, body := c.get("/admin/jobs" + query)
	require.Equal(t, http.StatusOK, status, string(body))
	var jobs []adminJob
	require.NoError(t, json.Unmarshal(body, &jobs))
	return jobs
}

func TestAdminJobs(t *testing.T) {
	srv, m, toker := newAdminAPI(t)
	root := newUserClient(t, srv, m, toker, "root@example.com")
	_, err := m.SetAdmin(t.Context(), "root@example.com", true)
	require.NoError(t, err)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	bob := newUserClient(t, srv, m, toker, "bob@example.com")
	alice.uploadWorkout(testsupport.EncodeFIT(t, routeDay, []testsupport.Split{{Meters: 2000, Seconds: 600}}))
	bob.uploadWorkout(testsupport.EncodeFIT(t, routeDay, []testsupport.Split{{Meters: 2000, Seconds: 600}}))

	all := adminJobs(t, root, "")
	require.NotEmpty(t, all)
	emails := map[string]bool{}
	for _, j := range all {
		emails[j.Email] = true
	}
	assert.True(t, emails["alice@example.com"] && emails["bob@example.com"], "every user's jobs: %v", all)

	aliceID := adminAccounts(t, root)["alice@example.com"].ID
	for _, j := range adminJobs(t, root, fmt.Sprintf("?user_id=%d&kind=detect_routes&state=queued", aliceID)) {
		assert.Equal(t, aliceID, j.UserID)
		assert.Equal(t, "detect_routes", j.Kind)
		assert.Equal(t, "queued", j.State)
	}
	for _, q := range []string{"?state=lost", "?kind=nope", "?user_id=x", "?user_id=0"} {
		status, _ := root.get("/admin/jobs" + q)
		assert.Equal(t, http.StatusBadRequest, status, q)
	}

	t.Run("reprocessing one account or all", func(t *testing.T) {
		status, body := root.postJSON("/admin/reprocess", fmt.Sprintf(`{"user_id":%d}`, aliceID))
		require.Equal(t, http.StatusAccepted, status, string(body))
		assert.JSONEq(t, `{"enqueued":1}`, string(body))
		status, body = root.postJSON("/admin/reprocess", `{"user_id":null}`)
		require.Equal(t, http.StatusAccepted, status, string(body))
		assert.JSONEq(t, `{"enqueued":1}`, string(body), "alice's is already queued")
		status, _ = root.postJSON("/admin/reprocess", `{"user_id":9999}`)
		assert.Equal(t, http.StatusUnprocessableEntity, status)
	})

	t.Run("retrying and dropping", func(t *testing.T) {
		queued := adminJobs(t, root, fmt.Sprintf("?user_id=%d&kind=process_workout&state=queued", aliceID))
		require.Len(t, queued, 1)
		id := queued[0].ID

		status, _ := root.post(fmt.Sprintf("/admin/jobs/%d/retry", id))
		assert.Equal(t, http.StatusConflict, status, "only a dead job is retried")

		now := time.Now()
		claimed, err := m.ClaimJob(t.Context(), now.Add(time.Hour), now.Add(2*time.Hour))
		require.NoError(t, err)
		require.NotNil(t, claimed)
		status, _ = root.delete(fmt.Sprintf("/admin/jobs/%d", claimed.ID))
		assert.Equal(t, http.StatusConflict, status, "a running job is its worker's")
		require.NoError(t, m.KillJob(t.Context(), claimed.ID, fmt.Errorf("the file went missing"), now))

		dead := adminJobs(t, root, "?state=dead")
		require.Len(t, dead, 1)
		require.NotNil(t, dead[0].LastError)
		assert.Equal(t, "the file went missing", *dead[0].LastError)

		status, body := root.post(fmt.Sprintf("/admin/jobs/%d/retry", claimed.ID))
		require.Equal(t, http.StatusOK, status, string(body))
		assert.Empty(t, adminJobs(t, root, "?state=dead"))

		status, _ = root.delete(fmt.Sprintf("/admin/jobs/%d", claimed.ID))
		assert.Equal(t, http.StatusNoContent, status)
		status, _ = root.delete(fmt.Sprintf("/admin/jobs/%d", claimed.ID))
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = root.post("/admin/jobs/9999/retry")
		assert.Equal(t, http.StatusNotFound, status)
	})
}

func TestAdminDeployment(t *testing.T) {
	srv, m, toker := newAdminAPI(t, routes.WithDeployment(routes.Deployment{Build: routes.Build{Version: "1.2.3", Commit: "abc"}, Weather: "open-meteo"}))
	root := newUserClient(t, srv, m, toker, "root@example.com")
	_, err := m.SetAdmin(t.Context(), "root@example.com", true)
	require.NoError(t, err)
	root.uploadWorkout(testsupport.EncodeFIT(t, routeDay, []testsupport.Split{{Meters: 2000, Seconds: 600}}))

	status, body := root.get("/admin/deployment")
	require.Equal(t, http.StatusOK, status, string(body))
	var d struct {
		Build struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"build"`
		ProcessorVersion string `json:"processor_version"`
		WorkoutsBehind   int64  `json:"workouts_behind"`
		Accounts         int64  `json:"accounts"`
		Workouts         int64  `json:"workouts"`
		MigrationVersion int64  `json:"migration_version"`
		MigrationDirty   bool   `json:"migration_dirty"`
		Features         struct {
			RegistrationOpen bool     `json:"registration_open"`
			Weather          string   `json:"weather"`
			Sources          []string `json:"sources"`
			OIDCProviders    []string `json:"oidc_providers"`
		} `json:"features"`
		Queue struct {
			Jobs []struct {
				Kind  string `json:"kind"`
				State string `json:"state"`
				Jobs  int64  `json:"jobs"`
			} `json:"jobs"`
		} `json:"queue"`
	}
	require.NoError(t, json.Unmarshal(body, &d))
	assert.Equal(t, "1.2.3", d.Build.Version)
	assert.Equal(t, "abc", d.Build.Commit)
	assert.NotEmpty(t, d.ProcessorVersion)
	assert.Zero(t, d.WorkoutsBehind)
	assert.Equal(t, int64(1), d.Accounts)
	assert.Equal(t, int64(1), d.Workouts)
	assert.GreaterOrEqual(t, d.MigrationVersion, int64(24))
	assert.False(t, d.MigrationDirty)
	assert.True(t, d.Features.RegistrationOpen)
	assert.Equal(t, "open-meteo", d.Features.Weather)
	assert.Equal(t, []string{}, d.Features.Sources)
	assert.Equal(t, []string{}, d.Features.OIDCProviders)
	var detect int64
	for _, j := range d.Queue.Jobs {
		if j.Kind == "detect_routes" && j.State == "queued" {
			detect = j.Jobs
		}
	}
	assert.Equal(t, int64(1), detect)
	assert.False(t, strings.Contains(string(body), "root@example.com"))
}
