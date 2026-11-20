package routes_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/models"
)

type deviceStart struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type cliTokens struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// startDevice is a CLI with no token asking to sign in.
func startDevice(t *testing.T, cli *apiClient, body string) deviceStart {
	t.Helper()
	status, resp := cli.postJSON("/auth/device", body)
	require.Equal(t, http.StatusOK, status, string(resp))
	var start deviceStart
	require.NoError(t, json.Unmarshal(resp, &start))
	return start
}

func oauthErrorOf(t *testing.T, status int, body []byte) string {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, status, string(body))
	var e struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &e), string(body))
	return e.Error
}

func TestDeviceSignIn(t *testing.T) {
	srv, m, toker := newAPI(t)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	bob := newUserClient(t, srv, m, toker, "bob@example.com")
	cli := &apiClient{t: t, srv: srv}

	status, body := cli.get("/auth/providers")
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, string(body), `"device_login":true`)

	start := startDevice(t, cli, `{"client_name":"laptop"}`)
	assert.Regexp(t, `^[A-Z]{4}-[A-Z]{4}$`, start.UserCode)
	assert.NotEmpty(t, start.DeviceCode)
	assert.Equal(t, 600, start.ExpiresIn)
	assert.Equal(t, 5, start.Interval)
	assert.True(t, strings.HasSuffix(start.VerificationURI, "/#/device"), start.VerificationURI)
	assert.True(t, strings.HasSuffix(start.VerificationURIComplete, "/#/device?code="+start.UserCode), start.VerificationURIComplete)

	t.Run("polling before anyone approves", func(t *testing.T) {
		waiting := startDevice(t, cli, `{}`)
		poll := fmt.Sprintf(`{"device_code":%q}`, waiting.DeviceCode)
		status, body := cli.postJSON("/auth/device/token", poll)
		assert.Equal(t, "authorization_pending", oauthErrorOf(t, status, body))
		status, body = cli.postJSON("/auth/device/token", poll)
		assert.Equal(t, "slow_down", oauthErrorOf(t, status, body), "straight away again, with five seconds asked for")
		status, body = cli.postJSON("/auth/device/token", `{"device_code":"made-up"}`)
		assert.Equal(t, "invalid_grant", oauthErrorOf(t, status, body))
	})

	t.Run("approving needs a signed-in user", func(t *testing.T) {
		status, _ := cli.get("/auth/device/" + start.UserCode)
		assert.Equal(t, http.StatusUnauthorized, status)
		status, _ = cli.postJSON("/auth/device/approve", fmt.Sprintf(`{"user_code":%q}`, start.UserCode))
		assert.Equal(t, http.StatusUnauthorized, status)
	})

	status, body = alice.get("/auth/device/" + strings.ToLower(start.UserCode))
	require.Equal(t, http.StatusOK, status, string(body))
	var waiting models.DeviceRequest
	require.NoError(t, json.Unmarshal(body, &waiting))
	assert.Equal(t, "laptop", waiting.ClientName)
	assert.Equal(t, start.UserCode, waiting.UserCode)
	assert.NotContains(t, string(body), start.DeviceCode, "the browser never sees the CLI's secret")

	status, body = alice.postJSON("/auth/device/approve", fmt.Sprintf(`{"user_code":%q}`, start.UserCode))
	require.Equal(t, http.StatusNoContent, status, string(body))
	status, _ = alice.get("/auth/device/" + start.UserCode)
	assert.Equal(t, http.StatusNotFound, status, "decided")
	status, _ = bob.postJSON("/auth/device/approve", fmt.Sprintf(`{"user_code":%q}`, start.UserCode))
	assert.Equal(t, http.StatusNotFound, status, "a code is approved once")

	status, body = cli.postJSON("/auth/device/token", fmt.Sprintf(`{"device_code":%q}`, start.DeviceCode))
	require.Equal(t, http.StatusOK, status, string(body))
	var granted cliTokens
	require.NoError(t, json.Unmarshal(body, &granted))
	require.NotEmpty(t, granted.Token)
	require.NotEmpty(t, granted.RefreshToken)
	assert.Equal(t, 7200, granted.ExpiresIn)

	// The token is the same kind every other sign-in gets.
	signedIn := &apiClient{t: t, srv: srv, token: granted.Token}
	status, body = signedIn.get("/auth/sessions")
	require.Equal(t, http.StatusOK, status, string(body))
	var sessions []models.CLISession
	require.NoError(t, json.Unmarshal(body, &sessions))
	require.Len(t, sessions, 1)
	assert.Equal(t, "laptop", sessions[0].Name)
	assert.NotContains(t, string(body), granted.RefreshToken)

	status, body = cli.postJSON("/auth/device/token", fmt.Sprintf(`{"device_code":%q}`, start.DeviceCode))
	assert.Equal(t, "invalid_grant", oauthErrorOf(t, status, body), "a device code is good for one grant")

	t.Run("refresh", func(t *testing.T) {
		status, body := cli.postJSON("/auth/tokens/refresh", fmt.Sprintf(`{"refresh_token":%q}`, granted.RefreshToken))
		require.Equal(t, http.StatusOK, status, string(body))
		var fresh cliTokens
		require.NoError(t, json.Unmarshal(body, &fresh))
		assert.Empty(t, fresh.RefreshToken, "the refresh token stays the one it was")
		again := &apiClient{t: t, srv: srv, token: fresh.Token}
		status, _ = again.get("/workouts")
		assert.Equal(t, http.StatusOK, status)

		status, body = cli.postJSON("/auth/tokens/refresh", `{"refresh_token":"made-up"}`)
		assert.Equal(t, "invalid_grant", oauthErrorOf(t, status, body))
	})

	t.Run("sessions are the user's own", func(t *testing.T) {
		status, body := bob.get("/auth/sessions")
		require.Equal(t, http.StatusOK, status)
		assert.JSONEq(t, `[]`, string(body))
		status, _ = bob.delete(fmt.Sprintf("/auth/sessions/%d", sessions[0].ID))
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("revoking from the account page", func(t *testing.T) {
		status, _ := alice.delete(fmt.Sprintf("/auth/sessions/%d", sessions[0].ID))
		require.Equal(t, http.StatusNoContent, status)
		status, body := cli.postJSON("/auth/tokens/refresh", fmt.Sprintf(`{"refresh_token":%q}`, granted.RefreshToken))
		assert.Equal(t, "invalid_grant", oauthErrorOf(t, status, body))
	})
}

func TestDeviceSignInDeniedAndSignOut(t *testing.T) {
	srv, m, toker := newAPI(t)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	cli := &apiClient{t: t, srv: srv}

	// A CLI that sends no body is still a CLI.
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/auth/device", nil)
	require.NoError(t, err)
	status, body := cli.do(req)
	require.Equal(t, http.StatusOK, status, string(body))
	var turned deviceStart
	require.NoError(t, json.Unmarshal(body, &turned))

	status, _ = alice.postJSON("/auth/device/deny", fmt.Sprintf(`{"user_code":%q}`, turned.UserCode))
	require.Equal(t, http.StatusNoContent, status)
	status, body = cli.postJSON("/auth/device/token", fmt.Sprintf(`{"device_code":%q}`, turned.DeviceCode))
	assert.Equal(t, "access_denied", oauthErrorOf(t, status, body))
	status, _ = alice.postJSON("/auth/device/deny", `{"user_code":"ZZZZ-ZZZZ"}`)
	assert.Equal(t, http.StatusNotFound, status)

	start := startDevice(t, cli, `{"client_name":"box"}`)
	status, _ = alice.postJSON("/auth/device/approve", fmt.Sprintf(`{"user_code":%q}`, start.UserCode))
	require.Equal(t, http.StatusNoContent, status)
	status, body = cli.postJSON("/auth/device/token", fmt.Sprintf(`{"device_code":%q}`, start.DeviceCode))
	require.Equal(t, http.StatusOK, status, string(body))
	var granted cliTokens
	require.NoError(t, json.Unmarshal(body, &granted))

	// taco logout: holding the refresh token is enough to end the session.
	status, _ = cli.postJSON("/auth/tokens/revoke", fmt.Sprintf(`{"refresh_token":%q}`, granted.RefreshToken))
	require.Equal(t, http.StatusNoContent, status)
	status, body = cli.postJSON("/auth/tokens/refresh", fmt.Sprintf(`{"refresh_token":%q}`, granted.RefreshToken))
	assert.Equal(t, "invalid_grant", oauthErrorOf(t, status, body))
	status, body = alice.get("/auth/sessions")
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[]`, string(body))
}

func TestDeviceSignInWorksWithPasswordLoginOff(t *testing.T) {
	a := newOIDCAPI(t, false, nil)
	status, body := a.call(http.DefaultClient, nil, http.MethodPost, "/auth/device")
	require.Equal(t, http.StatusOK, status, string(body))
	var start deviceStart
	require.NoError(t, json.Unmarshal(body, &start))
	assert.True(t, strings.HasPrefix(start.VerificationURI, "http"), "an absolute link when auth.public_base_url is set: %s", start.VerificationURI)
}
