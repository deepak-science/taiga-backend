package coros_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/coros"
	"gitea.deepak.science/deepak/taiga/internal/metrics"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
)

func refreshes(t *testing.T, outcome string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != "taiga_source_token_refreshes_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == outcome {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestEveryRefreshIsCountedWhereverItHappens(t *testing.T) {
	fake := testsupport.NewFakeCOROS(t)
	svc, client, token := signIn(t, fake)
	ctx := context.Background()
	refreshed, failed := refreshes(t, "refreshed"), refreshes(t, "failed")

	_, err := svc.Refresh(ctx, client, token)
	require.NoError(t, err)
	assert.Equal(t, refreshed, refreshes(t, "refreshed"), "a token with time left isn't refreshed")

	// Spent on arrival, so the session refreshes on its own, past Refresh.
	fake.AccessTTL = time.Second
	spent := *token
	spent.Expiry = time.Now().Add(-time.Minute)
	session, err := svc.Open(ctx, client, &spent)
	require.NoError(t, err)
	_, err = session.List(ctx, day.AddDate(0, 0, -1), day)
	require.NoError(t, err)
	session.Close()
	// A one-second token is inside oauth2's expiry margin, so each call refreshes; every one of those is a real request to COROS.
	assert.Greater(t, refreshes(t, "refreshed"), refreshed)

	// The refresh token was used above, so trying it again fails.
	_, err = svc.Open(ctx, client, &spent)
	require.ErrorIs(t, err, coros.ErrAuthorization)
	assert.Greater(t, refreshes(t, "failed"), failed)
}
