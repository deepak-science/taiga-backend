package routes_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/backup"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
)

func (c *apiClient) importArchive(archive []byte) (int, []byte) {
	req, err := http.NewRequest(http.MethodPost, c.srv.URL+"/import", bytes.NewReader(archive))
	require.NoError(c.t, err)
	req.Header.Set("Content-Type", "application/x-tar")
	return c.do(req)
}

func TestImport(t *testing.T) {
	srv, m, toker := newAPI(t)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	bob := newUserClient(t, srv, m, toker, "bob@example.com")
	splits := []testsupport.Split{{Meters: 2000, Seconds: 600}}
	alice.uploadWorkout(testsupport.EncodeFIT(t, routeDay, splits))
	alice.uploadWorkout(testsupport.EncodeFIT(t, routeDay.AddDate(0, 0, 1), splits))
	status, body := alice.postJSON("/workouts", `{"distance_miles": 3, "time_seconds": 1800, "start_time": "2026-10-30T07:00:00Z"}`)
	require.Equal(t, http.StatusCreated, status, string(body))
	status, archive := alice.get("/export")
	require.Equal(t, http.StatusOK, status, string(archive))

	var report models.ImportReport
	status, body = bob.importArchive(archive)
	require.Equal(t, http.StatusOK, status, string(body))
	require.NoError(t, json.Unmarshal(body, &report))
	assert.Equal(t, models.ImportReport{Files: 2, FilesAdded: 2, Workouts: 3, Unreadable: map[string]string{}}, report)
	status, body = bob.get("/workouts")
	require.Equal(t, http.StatusOK, status, string(body))
	var got []models.Workout
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Len(t, got, 3)

	t.Run("again changes nothing", func(t *testing.T) {
		status, body := bob.importArchive(archive)
		require.Equal(t, http.StatusOK, status, string(body))
		var report models.ImportReport
		require.NoError(t, json.Unmarshal(body, &report))
		assert.Equal(t, models.ImportReport{Files: 2, Unreadable: map[string]string{}}, report)
	})

	t.Run("not a deployment's backup", func(t *testing.T) {
		var buf bytes.Buffer
		a := &backup.Archive{Manifest: backup.Manifest{Format: backup.Format, Kind: backup.KindDeployment, Version: backup.Version, CreatedAt: routeDay}}
		require.NoError(t, backup.Write(t.Context(), &buf, a, nil))
		status, body := bob.importArchive(buf.Bytes())
		assert.Equal(t, http.StatusUnprocessableEntity, status)
		assert.Contains(t, string(body), "deployment's backup")
	})

	t.Run("not an archive", func(t *testing.T) {
		status, body := bob.importArchive([]byte("not a tar"))
		assert.Equal(t, http.StatusUnprocessableEntity, status)
		assert.Contains(t, string(body), "not a valid taiga backup")
	})

	t.Run("cut short", func(t *testing.T) {
		status, _ := bob.importArchive(archive[:len(archive)/2])
		assert.Equal(t, http.StatusUnprocessableEntity, status)
	})

	t.Run("needs a token", func(t *testing.T) {
		anon := &apiClient{t: t, srv: srv}
		status, _ := anon.importArchive(archive)
		assert.Equal(t, http.StatusUnauthorized, status)
	})
}
