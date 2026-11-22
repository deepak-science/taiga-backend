package coros_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"gitea.deepak.science/deepak/taiga/internal/coros"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
)

const redirectURL = "http://taiga.test/api/sources/coros/callback"

var day = time.Date(2026, 11, 19, 21, 51, 32, 0, time.UTC)

// signIn registers with the fake and takes a user through its sign-in, as the link flow does.
func signIn(t *testing.T, fake *testsupport.FakeCOROS) (*coros.Service, *coros.Client, *oauth2.Token) {
	t.Helper()
	ctx := context.Background()
	svc := coros.New(fake.Endpoint(), redirectURL)
	client, err := svc.Discover(ctx)
	require.NoError(t, err)
	require.NoError(t, svc.Register(ctx, client))

	verifier := oauth2.GenerateVerifier()
	// The browser's part: follow the authorize URL as far as the redirect back.
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := browser.Get(svc.AuthCodeURL(client, "state-1", verifier))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusFound, resp.StatusCode)
	back, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "state-1", back.Query().Get("state"))

	token, err := svc.Exchange(ctx, client, back.Query().Get("code"), verifier)
	require.NoError(t, err)
	return svc, client, token
}

func TestDiscoverAndRegister(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	_, client, token := signIn(t, fake)

	assert.Equal(t, fake.Endpoint(), client.Resource)
	assert.Equal(t, fake.Server.URL, client.Issuer)
	assert.NotEmpty(t, client.ClientID)
	assert.Empty(t, client.ClientSecret, "COROS registers a client with no secret, and the fake does as it does")
	assert.NotEmpty(t, token.AccessToken)
	assert.NotEmpty(t, token.RefreshToken)
}

func TestExchangeNeedsTheVerifier(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	ctx := context.Background()
	svc := coros.New(fake.Endpoint(), redirectURL)
	client, err := svc.Discover(ctx)
	require.NoError(t, err)
	require.NoError(t, svc.Register(ctx, client))

	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := browser.Get(svc.AuthCodeURL(client, "s", oauth2.GenerateVerifier()))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	back, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)

	_, err = svc.Exchange(ctx, client, back.Query().Get("code"), oauth2.GenerateVerifier())
	assert.Error(t, err)
}

func TestListAndFiles(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	fake.Add(
		testsupport.FakeActivity{ID: "481875557710070260", SportType: 100, Start: day, End: day.Add(20 * time.Minute), File: []byte("the run")},
		testsupport.FakeActivity{ID: "481854916634116197", SportType: 1004, Name: "Pickleball", Start: day.Add(-26 * time.Hour), End: day.Add(-25 * time.Hour), File: []byte("the game")},
		testsupport.FakeActivity{ID: "481000000000000000", SportType: 100, Start: day.AddDate(0, 0, -30), End: day.AddDate(0, 0, -30).Add(time.Hour), File: []byte("last month")},
	)
	svc, client, token := signIn(t, fake)
	ctx := context.Background()

	for name, plain := range map[string]bool{"as a JSON string, the way COROS sends it": false, "plain": true} {
		t.Run(name, func(t *testing.T) {
			fake.PlainList = plain
			session, err := svc.Open(ctx, client, token)
			require.NoError(t, err)
			defer session.Close()

			activities, err := session.List(ctx, day.AddDate(0, 0, -7), day.Add(time.Hour))
			require.NoError(t, err)
			require.Len(t, activities, 2, "last month's is outside the window")
			assert.Equal(t, coros.Activity{ID: "481854916634116197", SportType: 1004, Name: "Pickleball — 2026-11-18", Start: day.Add(-26 * time.Hour), End: day.Add(-25 * time.Hour)}, activities[0], "oldest first")
			assert.Equal(t, "481875557710070260", activities[1].ID)

			files, err := session.Files(ctx, activities[1])
			require.NoError(t, err)
			assert.Equal(t, [][]byte{[]byte("the run")}, files)
		})
	}
}

func TestListCutsToTheWindow(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	// COROS is asked for whole days either side, so it returns these; the window is to the second.
	fake.Add(
		testsupport.FakeActivity{ID: "1", SportType: 100, Start: day.Add(-time.Second), End: day},
		testsupport.FakeActivity{ID: "2", SportType: 100, Start: day, End: day.Add(time.Minute)},
		testsupport.FakeActivity{ID: "3", SportType: 100, Start: day.Add(time.Hour + time.Second), End: day.Add(2 * time.Hour)},
	)
	svc, client, token := signIn(t, fake)
	session, err := svc.Open(context.Background(), client, token)
	require.NoError(t, err)
	defer session.Close()

	activities, err := session.List(context.Background(), day, day.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, activities, 1)
	assert.Equal(t, "2", activities[0].ID)
}

func TestNothingListed(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	svc, client, token := signIn(t, fake)
	session, err := svc.Open(context.Background(), client, token)
	require.NoError(t, err)
	defer session.Close()

	activities, err := session.List(context.Background(), day.AddDate(0, 0, -7), day)
	require.NoError(t, err)
	assert.Empty(t, activities)
}

func TestNewWordingIsAnErrorNotAnEmptyList(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	svc, client, token := signIn(t, fake)
	session, err := svc.Open(context.Background(), client, token)
	require.NoError(t, err)
	defer session.Close()

	for name, reply := range map[string]string{
		"reworded":                "Here are your recent workouts! You ran twice this week.",
		"no ids":                  "Sport Records — 2026-11-15 to 2026-11-21 (1 records)\n\n1. Outdoor Run — 2026-11-21\n   Duration: 51:40\n",
		"fewer than it says":      "Sport Records — 2026-11-15 to 2026-11-21 (2 records)\n\n1. Outdoor Run — 2026-11-21\n   LabelId: 5 | SportType: 100\n",
		"temporarily unavailable": "Sorry, COROS API is temporarily unavailable. Please try again later.",
	} {
		t.Run(name, func(t *testing.T) {
			fake.ListReply = reply
			_, err := session.List(context.Background(), day.AddDate(0, 0, -7), day)
			assert.ErrorIs(t, err, coros.ErrUnexpectedReply)
		})
	}
}

func TestFilesThatCannotBeHad(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	fake.DailyLimit = 1
	fake.Add(
		testsupport.FakeActivity{ID: "1", SportType: 100, Start: day, End: day.Add(time.Minute), File: []byte("one")},
		testsupport.FakeActivity{ID: "2", SportType: 100, Start: day, End: day.Add(time.Minute), File: []byte("two")},
		testsupport.FakeActivity{ID: "3", SportType: 100, Start: day, InProgress: true},
	)
	svc, client, token := signIn(t, fake)
	ctx := context.Background()
	session, err := svc.Open(ctx, client, token)
	require.NoError(t, err)
	defer session.Close()

	_, err = session.Files(ctx, coros.Activity{ID: "3", SportType: 100})
	assert.ErrorIs(t, err, coros.ErrInProgress)

	_, err = session.Files(ctx, coros.Activity{ID: "1", SportType: 100})
	require.NoError(t, err)
	_, err = session.Files(ctx, coros.Activity{ID: "2", SportType: 100})
	assert.ErrorIs(t, err, coros.ErrDailyLimit)

	_, err = session.Files(ctx, coros.Activity{ID: "nope", SportType: 100})
	require.Error(t, err)
	assert.NotErrorIs(t, err, coros.ErrDailyLimit)
}

func TestRefreshedTokensAreHandedBack(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	// Tokens this short are spent on arrival, so every session has to refresh.
	fake.AccessTTL = time.Second
	svc, client, token := signIn(t, fake)
	ctx := context.Background()

	for range 3 {
		session, err := svc.Open(ctx, client, token)
		require.NoError(t, err)
		_, err = session.List(ctx, day.AddDate(0, 0, -1), day)
		require.NoError(t, err)
		fresh, err := session.Token()
		require.NoError(t, err)
		session.Close()

		assert.NotEqual(t, token.RefreshToken, fresh.RefreshToken, "COROS issues a new refresh token each time, and the old one is spent")
		token = fresh
	}

	// Carrying on with a refresh token already used is what losing a save looks like.
	stale := *token
	session, err := svc.Open(ctx, client, token)
	require.NoError(t, err)
	_, err = session.Token()
	require.NoError(t, err)
	session.Close()
	_, err = svc.Open(ctx, client, &stale)
	assert.ErrorIs(t, err, coros.ErrAuthorization)
}

func TestRevokedGrantIsAnAuthorizationError(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	svc, client, token := signIn(t, fake)
	fake.RevokeAll()

	_, err := svc.Open(context.Background(), client, token)
	assert.ErrorIs(t, err, coros.ErrAuthorization)
}

func TestRevoke(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	svc, client, token := signIn(t, fake)
	ctx := context.Background()

	require.NoError(t, svc.Revoke(ctx, client, token))
	_, err := svc.Open(ctx, client, token)
	assert.ErrorIs(t, err, coros.ErrAuthorization)
}

func TestAnUnreachableServer(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	svc, client, token := signIn(t, fake)
	fake.Server.Close()

	_, err := svc.Open(context.Background(), client, token)
	require.Error(t, err)
	assert.NotErrorIs(t, err, coros.ErrAuthorization, "an outage mustn't look like a revoked grant, or the user is told to reconnect for nothing")
}

func TestAClientGivenASecretUsesIt(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	fake.IssueSecret = true
	fake.AccessTTL = time.Second
	svc, client, token := signIn(t, fake)
	require.NotEmpty(t, client.ClientSecret)

	// The exchange has already used it; so must the refresh this session opens with, and the revoke.
	ctx := context.Background()
	session, err := svc.Open(ctx, client, token)
	require.NoError(t, err)
	fresh, err := session.Token()
	require.NoError(t, err)
	session.Close()
	assert.NotEqual(t, token.AccessToken, fresh.AccessToken)
	require.NoError(t, svc.Revoke(ctx, client, fresh))
}
