package models_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
)

func TestSetAdminMatchesOneAccount(t *testing.T) {
	m := testsupport.NewModel(t)
	isAdmin := func(id int32) bool {
		s, err := m.AdminStatus(t.Context(), id)
		require.NoError(t, err)
		return s.IsAdmin
	}
	alice := createUser(t, m, "alice@example.com")

	u, err := m.SetAdmin(t.Context(), "ALICE@example.com", true)
	require.NoError(t, err, "case doesn't matter while one account matches")
	assert.Equal(t, alice.ID, u.ID)
	assert.True(t, isAdmin(alice.ID))

	_, err = m.SetAdmin(t.Context(), "nobody@example.com", true)
	assert.ErrorIs(t, err, models.ErrNoSuchAccount)

	// Registration kept the case it was given, so two accounts can differ only in it; then only the exact one is meant.
	shouty := createUser(t, m, "Alice@example.com")
	_, err = m.SetAdmin(t.Context(), "ALICE@example.com", false)
	assert.ErrorIs(t, err, models.ErrNoSuchAccount)
	u, err = m.SetAdmin(t.Context(), "Alice@example.com", true)
	require.NoError(t, err)
	assert.Equal(t, shouty.ID, u.ID)
	u, err = m.SetAdmin(t.Context(), "alice@example.com", false)
	require.NoError(t, err)
	assert.Equal(t, alice.ID, u.ID)
	assert.False(t, isAdmin(alice.ID))
	assert.True(t, isAdmin(shouty.ID))
}

func TestConfiguredAdmins(t *testing.T) {
	admins := models.NewAdmins([]string{" Carol@Example.com", ""})
	assert.True(t, admins.Is(&models.AdminStatus{Email: "carol@example.com", EmailVerified: true}))
	assert.False(t, admins.Is(&models.AdminStatus{Email: "carol@example.com"}), "an unverified email could be anyone's")
	assert.False(t, admins.Is(&models.AdminStatus{Email: "dave@example.com", EmailVerified: true}))
	assert.True(t, admins.Is(&models.AdminStatus{Email: "dave@example.com", IsAdmin: true}))
	assert.False(t, models.NewAdmins(nil).Is(&models.AdminStatus{EmailVerified: true}), "an empty email isn't listed")
}
