package routes_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/models"
)

// corosFigures is GET /workouts/{id}/coros, decoded loosely so a field that shouldn't be served shows up.
func (c *apiClient) corosFigures(id int32) (int, map[string]any) {
	c.t.Helper()
	status, body := c.get(fmt.Sprintf("/workouts/%d/coros", id))
	if status != http.StatusOK {
		return status, nil
	}
	var out map[string]any
	require.NoError(c.t, json.Unmarshal(body, &out), string(body))
	return status, out
}

func TestASyncKeepsCOROSsOwnFigures(t *testing.T) {
	a := newSourcesAPI(t)
	alice, bob := a.user("alice@example.com"), a.user("bob@example.com")
	a.fake.Add(a.run(1, 2*time.Hour), a.junk(2, 3*time.Hour))
	a.connect(alice)
	a.sync()

	imported := a.workouts(alice)
	require.Len(t, imported, 1)
	id := imported[0].ID
	assert.Equal(t, 1, a.fake.Details(), "asked for the run; the file that made no workout has nowhere to keep them")
	assert.Empty(t, a.sources(alice).Connections[0].LastError)

	status, figures := alice.corosFigures(id)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, a.run(1, 0).ID, figures["external_id"])
	assert.Equal(t, 120.0, figures["training_load"])
	assert.Equal(t, 3.6, figures["aerobic_te"])
	assert.Equal(t, 0.9, figures["anaerobic_te"])
	assert.Equal(t, "Threshold", figures["training_focus"])
	assert.Equal(t, "Best", figures["performance"])
	assert.Equal(t, 403.0, figures["adjusted_pace_s_per_km"])
	assert.Equal(t, 398.0, figures["moving_avg_pace_s_per_km"])
	assert.Equal(t, 380.0, figures["best_km_seconds"])
	assert.Equal(t, 7630.0, figures["distance_meters"])
	assert.NotContains(t, figures, "reply")
	assert.NotContains(t, figures, "user_id")

	t.Run("they are for tracking, and the workout's own figures stay taiga's", func(t *testing.T) {
		w := alice.workout(id)
		assert.InDelta(t, 300, *w.TimeSeconds, 1e-6, "the file's five minutes, not COROS's 51:40")
		assert.Nil(t, w.Calories, "the file states none, and COROS's aren't copied in")
	})

	t.Run("asked once", func(t *testing.T) {
		a.syncAgain(alice)
		assert.Equal(t, 1, a.fake.Details())
	})

	t.Run("reprocessing leaves them alone", func(t *testing.T) {
		status, body := alice.post(fmt.Sprintf("/workouts/%d/reprocess", id))
		require.Equal(t, http.StatusOK, status, string(body))
		status, after := alice.corosFigures(id)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, figures, after)
	})

	t.Run("they are the user's own", func(t *testing.T) {
		status, _ := bob.corosFigures(id)
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("a deleted workout takes them with it, and isn't asked about again", func(t *testing.T) {
		status, _ := alice.delete(fmt.Sprintf("/workouts/%d", id))
		require.Less(t, status, 300)
		a.syncAgain(alice)
		assert.Equal(t, 1, a.fake.Details())
		assert.Empty(t, a.workouts(alice))
	})
}

func TestARunUploadedByHandGetsCOROSsFiguresToo(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	activity := a.run(1, 2*time.Hour)
	_, id := alice.uploadWorkout(activity.File)

	status, _ := alice.corosFigures(id)
	assert.Equal(t, http.StatusNotFound, status, "nothing from COROS yet")

	a.fake.Add(activity)
	a.connect(alice)
	a.sync()
	require.Len(t, a.workouts(alice), 1, "the same file, so the same workout")
	status, figures := alice.corosFigures(id)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, 120.0, figures["training_load"])
}

func TestFiguresThatCannotBeReadDoNotStopTheImport(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	a.fake.Add(a.run(1, 2*time.Hour), a.run(2, 26*time.Hour))
	a.fake.DetailReply = "Activity Details are having a day off."
	a.connect(alice)
	a.sync()

	imported := a.workouts(alice)
	require.Len(t, imported, 2, "the activities themselves came in")
	conn := a.sources(alice).Connections[0]
	require.NotNil(t, conn.LastSyncedAt)
	assert.False(t, conn.NeedsReconnect)
	assert.Contains(t, conn.LastError, "COROS's own figures for 2 of them couldn't be read")
	assert.Equal(t, 2, a.fake.Details(), "one unreadable reply doesn't speak for the next activity")
	status, _ := alice.corosFigures(imported[0].ID)
	assert.Equal(t, http.StatusNotFound, status)

	// The job finished, so nothing is dead and the hourly sweep carries on.
	status, body := alice.get("/jobs?state=dead")
	require.Equal(t, http.StatusOK, status)
	var dead []models.Job
	require.NoError(t, json.Unmarshal(body, &dead))
	assert.Empty(t, dead)

	// COROS back to its usual wording: the next sync asks again, for no files.
	a.fake.DetailReply = ""
	files := a.fileRequests()
	a.now = a.now.Add(time.Hour)
	a.syncAgain(alice)
	assert.Equal(t, files, a.fileRequests())
	assert.Empty(t, a.sources(alice).Connections[0].LastError)
	for _, w := range imported {
		status, figures := alice.corosFigures(w.ID)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, 120.0, figures["training_load"])
	}
}
