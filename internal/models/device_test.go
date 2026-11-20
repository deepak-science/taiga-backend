package models_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

var deviceDay = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

func TestDeviceSignIn(t *testing.T) {
	ctx := context.Background()
	m := testsupport.NewModel(t)
	alice := createUser(t, m, "alice@example.com")
	bob := createUser(t, m, "bob@example.com")

	start, err := m.StartDeviceAuthorization(ctx, "  laptop  ", deviceDay)
	require.NoError(t, err)
	assert.Regexp(t, `^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$`, start.UserCode)
	assert.Equal(t, deviceDay.Add(models.DeviceCodeTTL), start.ExpiresAt)

	_, err = m.ExchangeDeviceCode(ctx, start.DeviceCode, deviceDay.Add(time.Second))
	assert.ErrorIs(t, err, models.ErrAuthorizationPending)
	_, err = m.ExchangeDeviceCode(ctx, start.DeviceCode, deviceDay.Add(2*time.Second))
	assert.ErrorIs(t, err, models.ErrSlowDown, "a second after the last poll, with five asked for")
	_, err = m.ExchangeDeviceCode(ctx, start.DeviceCode, deviceDay.Add(5200*time.Millisecond))
	assert.ErrorIs(t, err, models.ErrAuthorizationPending, "a poll a little early of the interval is let through")
	_, err = m.ExchangeDeviceCode(ctx, "not-a-code", deviceDay)
	assert.ErrorIs(t, err, models.ErrInvalidGrant)

	// The approval page looks the code up however it was typed.
	typed := " " + strings.ToLower(strings.ReplaceAll(start.UserCode, "-", " "))
	waiting, err := m.DeviceRequest(ctx, typed, deviceDay.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "laptop", waiting.ClientName)
	assert.Equal(t, start.UserCode, waiting.UserCode)

	require.NoError(t, m.ApproveDevice(ctx, typed, deviceDay.Add(time.Minute), alice))
	_, err = m.DeviceRequest(ctx, start.UserCode, deviceDay.Add(time.Minute))
	assert.True(t, models.IsNotFound(err), "decided, so no longer waiting")
	err = m.ApproveDevice(ctx, start.UserCode, deviceDay.Add(time.Minute), bob)
	assert.True(t, models.IsNotFound(err), "a code is approved once, by one user")

	grant, err := m.ExchangeDeviceCode(ctx, start.DeviceCode, deviceDay.Add(2*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, alice.ID, grant.User.ID)
	assert.Equal(t, "alice@example.com", grant.User.Email)
	assert.NotEmpty(t, grant.RefreshToken)
	_, err = m.ExchangeDeviceCode(ctx, start.DeviceCode, deviceDay.Add(3*time.Minute))
	assert.ErrorIs(t, err, models.ErrInvalidGrant, "a device code is good for one grant")

	sessions, err := m.CLISessions(ctx, alice)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "laptop", sessions[0].Name)
	none, err := m.CLISessions(ctx, bob)
	require.NoError(t, err)
	assert.Empty(t, none)

	t.Run("refresh keeps the session alive", func(t *testing.T) {
		later := deviceDay.Add(80 * 24 * time.Hour)
		user, err := m.RefreshCLISession(ctx, grant.RefreshToken, later)
		require.NoError(t, err)
		assert.Equal(t, alice.ID, user.ID)
		sessions, err := m.CLISessions(ctx, alice)
		require.NoError(t, err)
		assert.True(t, sessions[0].LastUsedAt.Time.Equal(later))

		// Past the first 90 days, but within 90 of that use.
		_, err = m.RefreshCLISession(ctx, grant.RefreshToken, later.Add(80*24*time.Hour))
		require.NoError(t, err)
		_, err = m.RefreshCLISession(ctx, "not-a-token", later)
		assert.ErrorIs(t, err, models.ErrInvalidGrant)
	})

	t.Run("revoking", func(t *testing.T) {
		err := m.RevokeCLISession(ctx, sessions[0].ID, bob)
		assert.True(t, models.IsNotFound(err), "not bob's to revoke")
		require.NoError(t, m.RevokeCLISession(ctx, sessions[0].ID, alice))
		_, err = m.RefreshCLISession(ctx, grant.RefreshToken, deviceDay.Add(time.Hour))
		assert.ErrorIs(t, err, models.ErrInvalidGrant)
		err = m.RevokeCLISession(ctx, sessions[0].ID, alice)
		assert.True(t, models.IsNotFound(err))
	})
}

// signInCLI runs the whole flow for user and returns the refresh token.
func signInCLI(t *testing.T, m models.Model, user *tokens.UserToken, now time.Time) string {
	t.Helper()
	ctx := context.Background()
	start, err := m.StartDeviceAuthorization(ctx, "box", now)
	require.NoError(t, err)
	require.NoError(t, m.ApproveDevice(ctx, start.UserCode, now, user))
	grant, err := m.ExchangeDeviceCode(ctx, start.DeviceCode, now)
	require.NoError(t, err)
	return grant.RefreshToken
}

func TestDeviceSignInRunsOutOrIsDenied(t *testing.T) {
	ctx := context.Background()
	m := testsupport.NewModel(t)
	alice := createUser(t, m, "alice@example.com")

	late, err := m.StartDeviceAuthorization(ctx, "", deviceDay)
	require.NoError(t, err)
	after := deviceDay.Add(models.DeviceCodeTTL)
	err = m.ApproveDevice(ctx, late.UserCode, after, alice)
	assert.True(t, models.IsNotFound(err), "too late to approve")
	_, err = m.ExchangeDeviceCode(ctx, late.DeviceCode, after)
	assert.ErrorIs(t, err, models.ErrExpiredToken)
	_, err = m.ExchangeDeviceCode(ctx, late.DeviceCode, after.Add(time.Minute))
	assert.ErrorIs(t, err, models.ErrInvalidGrant, "and gone once it's said so")

	turned, err := m.StartDeviceAuthorization(ctx, "", deviceDay)
	require.NoError(t, err)
	require.NoError(t, m.DenyDevice(ctx, turned.UserCode, deviceDay))
	err = m.ApproveDevice(ctx, turned.UserCode, deviceDay, alice)
	assert.True(t, models.IsNotFound(err), "denied is decided")
	_, err = m.ExchangeDeviceCode(ctx, turned.DeviceCode, deviceDay)
	assert.ErrorIs(t, err, models.ErrAccessDenied)

	waiting, err := m.StartDeviceAuthorization(ctx, "", deviceDay)
	require.NoError(t, err)
	req, err := m.DeviceRequest(ctx, waiting.UserCode, deviceDay)
	require.NoError(t, err)
	assert.Equal(t, "taco", req.ClientName, "a CLI that doesn't say what it is")
}

func TestDeviceSignInsWaitingAreCapped(t *testing.T) {
	ctx := context.Background()
	m := testsupport.NewModel(t)
	for range 100 {
		_, err := m.StartDeviceAuthorization(ctx, "flood", deviceDay)
		require.NoError(t, err)
	}
	_, err := m.StartDeviceAuthorization(ctx, "one more", deviceDay.Add(time.Minute))
	assert.ErrorIs(t, err, models.ErrTooManyDevices)
	_, err = m.StartDeviceAuthorization(ctx, "later", deviceDay.Add(models.DeviceCodeTTL))
	assert.NoError(t, err, "the ones that ran out make room")
}

func TestCLISessionLapsesUnused(t *testing.T) {
	ctx := context.Background()
	m := testsupport.NewModel(t)
	alice := createUser(t, m, "alice@example.com")
	idle := signInCLI(t, m, alice, deviceDay)
	used := signInCLI(t, m, alice, deviceDay)

	_, err := m.RefreshCLISession(ctx, used, deviceDay.Add(60*24*time.Hour))
	require.NoError(t, err)
	lapsed := deviceDay.Add(models.CLISessionTTL)
	_, err = m.RefreshCLISession(ctx, idle, lapsed)
	assert.ErrorIs(t, err, models.ErrInvalidGrant, "refused even before it's pruned")

	require.NoError(t, m.PruneCLIAuth(ctx, lapsed))
	sessions, err := m.CLISessions(ctx, alice)
	require.NoError(t, err)
	require.Len(t, sessions, 1, "only the one in use is left")
	_, err = m.RefreshCLISession(ctx, used, lapsed)
	assert.NoError(t, err)

	require.NoError(t, m.RevokeCLISessionByToken(ctx, used))
	_, err = m.RefreshCLISession(ctx, used, lapsed)
	assert.ErrorIs(t, err, models.ErrInvalidGrant)
	assert.NoError(t, m.RevokeCLISessionByToken(ctx, "never-issued"), "already signed out")
}
