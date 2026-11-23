package routes_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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

func TestImportTurnsAwayAFileOverItsLimit(t *testing.T) {
	srv, m, toker := newAPI(t)
	bob := newUserClient(t, srv, m, toker, "bob@example.com")
	big := bytes.Repeat([]byte("taiga"), (64<<20)/5+1)
	sum := sha256.Sum256(big)
	hash := hex.EncodeToString(sum[:])
	a := &backup.Archive{Manifest: backup.Manifest{Format: backup.Format, Kind: backup.KindUser, Version: backup.Version, CreatedAt: routeDay}}
	a.Files = []backup.File{{SHA256: hash, Timestamp: routeDay}}
	var buf bytes.Buffer
	require.NoError(t, backup.Write(t.Context(), &buf, a, func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(big)), nil
	}))

	status, body := bob.importArchive(buf.Bytes())
	assert.Equal(t, http.StatusRequestEntityTooLarge, status)
	assert.Contains(t, string(body), hash)
}

func TestImportTurnsAwayAFileThatDoesntMatchItsHash(t *testing.T) {
	srv, m, toker := newAPI(t)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	bob := newUserClient(t, srv, m, toker, "bob@example.com")
	fit := testsupport.EncodeFIT(t, routeDay, []testsupport.Split{{Meters: 2000, Seconds: 600}})
	alice.uploadWorkout(fit)
	status, archive := alice.get("/export")
	require.Equal(t, http.StatusOK, status, string(archive))

	// One byte of the file's payload changed, so its bytes no longer match the name it's under.
	at := bytes.Index(archive, fit)
	require.GreaterOrEqual(t, at, 0)
	archive[at+len(fit)/2] ^= 0xff
	status, body := bob.importArchive(archive)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Contains(t, string(body), "doesn't match its hash")
}

func TestAnImportHoldsUpOnlyItsOwnAccount(t *testing.T) {
	srv, m, toker := newAPI(t)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	bob := newUserClient(t, srv, m, toker, "bob@example.com")

	// Bob's first import is still uploading.
	pr, pw := io.Pipe()
	done := make(chan int)
	go func() {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/import", pr)
		if err != nil {
			done <- 0
			return
		}
		req.Header.Set("Authorization", "Bearer "+bob.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- 0
			return
		}
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()
	_, err := pw.Write([]byte("a"))
	require.NoError(t, err, "the server is reading it")

	status, _ := bob.importArchive([]byte("not a tar"))
	assert.Equal(t, http.StatusConflict, status, "bob's second waits for his first")
	status, _ = alice.importArchive([]byte("not a tar"))
	assert.Equal(t, http.StatusUnprocessableEntity, status, "alice's goes ahead")

	require.NoError(t, pw.Close())
	assert.Equal(t, http.StatusUnprocessableEntity, <-done)
	status, _ = bob.importArchive([]byte("not a tar"))
	assert.Equal(t, http.StatusUnprocessableEntity, status, "and once it's done, bob can again")
}
