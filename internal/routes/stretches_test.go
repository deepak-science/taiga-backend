package routes_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

func (c *apiClient) stretch(id int32, query string) models.StretchDetail {
	c.t.Helper()
	status, body := c.get(fmt.Sprintf("/stretches/%d%s", id, query))
	require.Equal(c.t, http.StatusOK, status, string(body))
	var detail models.StretchDetail
	require.NoError(c.t, json.Unmarshal(body, &detail))
	return detail
}

func TestStretches(t *testing.T) {
	srv, m, toker := newAPI(t)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	bob := newUserClient(t, srv, m, toker, "bob@example.com")
	_, first := alice.uploadWorkout(testsupport.EncodeFIT(t, routeDay, []testsupport.Split{{Meters: 2000, Seconds: 600}}))
	_, second := alice.uploadWorkout(testsupport.EncodeFIT(t, routeDay.AddDate(0, 0, 1), []testsupport.Split{{Meters: 2000, Seconds: 800}}))
	_, elsewhere := alice.uploadWorkout(testsupport.EncodeFITHeading(t, routeDay.AddDate(0, 0, 2), []testsupport.Split{{Meters: 2000, Seconds: 600}}, 90))

	status, body := alice.postJSON("/stretches", fmt.Sprintf(`{"name":"  The hill ","workout_id":%d,"start_m":500,"end_m":1500}`, first))
	require.Equal(t, http.StatusCreated, status, string(body))
	var created models.StretchDetail
	require.NoError(t, json.Unmarshal(body, &created))
	assert.Equal(t, "The hill", created.Name)
	assert.Equal(t, workouts.SportRunning, created.Sport)
	assert.Equal(t, 1000.0, created.Meters)
	assert.Len(t, created.Points, 101)
	require.NotNil(t, created.SourceWorkoutID)
	assert.Equal(t, first, *created.SourceWorkoutID)
	require.Len(t, created.Efforts, 2, "the runs already uploaded are timed when the stretch is made, the one heading east isn't on it")
	assert.Equal(t, second, created.Efforts[0].WorkoutID, "newest first")
	assert.InDelta(t, 400, created.Efforts[0].DurationSeconds, 0.5)
	assert.Equal(t, first, created.Efforts[1].WorkoutID)
	assert.InDelta(t, 300, created.Efforts[1].DurationSeconds, 0.5)
	assert.InDelta(t, 150, created.Efforts[1].StartOffsetSeconds, 0.5)
	assert.InDelta(t, 500, created.Efforts[1].StartMeters, 1)
	assert.InDelta(t, 1000, created.Efforts[1].DistanceMeters, 1)
	assert.Nil(t, created.Efforts[1].WalkedFraction, "a steady run without cadence can't say what was walked")
	require.NotNil(t, created.BestSeconds)
	assert.InDelta(t, 300, *created.BestSeconds, 0.5)
	assert.Equal(t, first, *created.BestWorkoutID)
	assert.Equal(t, 2, created.WorkoutCount)
	id := created.ID

	status, body = alice.get("/stretches")
	require.Equal(t, http.StatusOK, status, string(body))
	var list []models.StretchSummary
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list, 1)
	assert.Equal(t, 2, list[0].EffortCount)

	windowed := alice.stretch(id, "?from="+routeDay.AddDate(0, 0, 1).Format("2006-01-02"))
	require.Len(t, windowed.Efforts, 1)
	assert.InDelta(t, 400, *windowed.BestSeconds, 0.5, "the figures follow the window")

	status, body = alice.get(fmt.Sprintf("/workouts/%d/stretches", first))
	require.Equal(t, http.StatusOK, status, string(body))
	var onWorkout []models.WorkoutStretchEffort
	require.NoError(t, json.Unmarshal(body, &onWorkout))
	require.Len(t, onWorkout, 1)
	assert.Equal(t, id, onWorkout[0].StretchID)
	assert.Equal(t, "The hill", onWorkout[0].Name)
	assert.Equal(t, 1000.0, onWorkout[0].StretchMeters)
	status, body = alice.get(fmt.Sprintf("/workouts/%d/stretches", elsewhere))
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[]`, string(body))

	t.Run("a new upload is timed as it's processed", func(t *testing.T) {
		_, third := alice.uploadWorkout(testsupport.EncodeFIT(t, routeDay.AddDate(0, 0, 3), []testsupport.Split{{Meters: 2000, Seconds: 1000}}))
		detail := alice.stretch(id, "")
		require.Len(t, detail.Efforts, 3)
		assert.Equal(t, third, detail.Efforts[0].WorkoutID)

		status, body := alice.post(fmt.Sprintf("/workouts/%d/reprocess", third))
		require.Equal(t, http.StatusOK, status, string(body))
		assert.Len(t, alice.stretch(id, "").Efforts, 3, "reprocessing replaces a workout's efforts, it doesn't add to them")
	})

	t.Run("a workout that stops counting loses its efforts", func(t *testing.T) {
		status, body := alice.put(fmt.Sprintf("/workouts/%d/corrections", second), `{"excluded_reason":"ran with a friend"}`)
		require.Equal(t, http.StatusOK, status, string(body))
		assert.Len(t, alice.stretch(id, "").Efforts, 2)
		status, body = alice.put(fmt.Sprintf("/workouts/%d/corrections", second), `{}`)
		require.Equal(t, http.StatusOK, status, string(body))
		assert.Len(t, alice.stretch(id, "").Efforts, 3)
	})

	t.Run("rename", func(t *testing.T) {
		status, body := alice.put(fmt.Sprintf("/stretches/%d", id), `{"name":" Whitney Way hill "}`)
		require.Equal(t, http.StatusOK, status, string(body))
		assert.Contains(t, string(body), `"name":"Whitney Way hill"`)
		status, _ = alice.put(fmt.Sprintf("/stretches/%d", id), `{"name":"  "}`)
		assert.Equal(t, http.StatusBadRequest, status)
	})

	t.Run("what can't be a stretch", func(t *testing.T) {
		manual, err := m.AddWorkout(t.Context(), &models.Workout{}, &tokens.UserToken{ID: 1})
		require.NoError(t, err)
		for name, c := range map[string]struct {
			body   string
			status int
		}{
			"no name":            {fmt.Sprintf(`{"workout_id":%d,"start_m":500,"end_m":1500}`, first), http.StatusBadRequest},
			"no range":           {fmt.Sprintf(`{"name":"x","workout_id":%d}`, first), http.StatusBadRequest},
			"too short":          {fmt.Sprintf(`{"name":"x","workout_id":%d,"start_m":500,"end_m":520}`, first), http.StatusUnprocessableEntity},
			"past the end":       {fmt.Sprintf(`{"name":"x","workout_id":%d,"start_m":1500,"end_m":2500}`, first), http.StatusUnprocessableEntity},
			"an unknown workout": {`{"name":"x","workout_id":99999,"start_m":500,"end_m":1500}`, http.StatusUnprocessableEntity},
			"a hand-entered one": {fmt.Sprintf(`{"name":"x","workout_id":%d,"start_m":500,"end_m":1500}`, manual.ID), http.StatusUnprocessableEntity},
			"not valid JSON":     {`{`, http.StatusBadRequest},
		} {
			status, body := alice.postJSON("/stretches", c.body)
			assert.Equal(t, c.status, status, "%s: %s", name, body)
		}
	})

	t.Run("another user's", func(t *testing.T) {
		status, _ := bob.get(fmt.Sprintf("/stretches/%d", id))
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = bob.put(fmt.Sprintf("/stretches/%d", id), `{"name":"mine"}`)
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = bob.delete(fmt.Sprintf("/stretches/%d", id))
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = bob.postJSON("/stretches", fmt.Sprintf(`{"name":"x","workout_id":%d,"start_m":500,"end_m":1500}`, first))
		assert.Equal(t, http.StatusUnprocessableEntity, status)
		status, body := bob.get("/stretches")
		require.Equal(t, http.StatusOK, status)
		assert.JSONEq(t, `[]`, string(body))
	})

	t.Run("deleting the workout it was cut from keeps the stretch", func(t *testing.T) {
		status, body := alice.delete(fmt.Sprintf("/workouts/%d", first))
		require.Less(t, status, 300, string(body))
		detail := alice.stretch(id, "")
		assert.Nil(t, detail.SourceWorkoutID)
		assert.Len(t, detail.Efforts, 2)
	})

	status, _ = alice.delete(fmt.Sprintf("/stretches/%d", id))
	require.Equal(t, http.StatusNoContent, status)
	status, _ = alice.get(fmt.Sprintf("/stretches/%d", id))
	assert.Equal(t, http.StatusNotFound, status)
	status, body = alice.get(fmt.Sprintf("/workouts/%d/stretches", second))
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[]`, string(body), "its efforts go with it")
}

func TestWalkingBreaksAndWalkedShare(t *testing.T) {
	srv, m, toker := newAPI(t)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	// 400m run, 100m walked in 70 seconds, 500m run, with a watch's cadence.
	_, workoutID := alice.uploadWorkout(testsupport.EncodeFIT(t, routeDay, []testsupport.Split{
		{Meters: 400, Seconds: 160, StepsPerMinute: 150},
		{Meters: 100, Seconds: 70, StepsPerMinute: 110},
		{Meters: 500, Seconds: 200, StepsPerMinute: 150},
	}))

	status, body := alice.get(fmt.Sprintf("/workouts/%d/series", workoutID))
	require.Equal(t, http.StatusOK, status, string(body))
	var series struct {
		Cadence []float64         `json:"cadence"`
		Walking *workouts.Walking `json:"walking"`
	}
	require.NoError(t, json.Unmarshal(body, &series))
	require.Len(t, series.Cadence, 101)
	assert.Equal(t, 150.0, series.Cadence[0], "steps a minute, both feet")
	assert.Equal(t, 110.0, series.Cadence[45])
	require.NotNil(t, series.Walking)
	assert.Equal(t, workouts.WalkingByCadence, series.Walking.Method)
	require.Len(t, series.Walking.Breaks, 1)
	assert.InDelta(t, 160, series.Walking.Breaks[0].StartSeconds, 8)
	assert.InDelta(t, 230, series.Walking.Breaks[0].EndSeconds, 1)
	assert.Zero(t, series.Walking.WarmupSeconds)

	status, body = alice.postJSON("/stretches", fmt.Sprintf(`{"name":"Walked bit","workout_id":%d,"start_m":300,"end_m":800}`, workoutID))
	require.Equal(t, http.StatusCreated, status, string(body))
	var created models.StretchDetail
	require.NoError(t, json.Unmarshal(body, &created))
	require.Len(t, created.Efforts, 1)
	assert.InDelta(t, 230, created.Efforts[0].DurationSeconds, 0.5)
	require.NotNil(t, created.Efforts[0].WalkedFraction)
	assert.InDelta(t, 70.0/230, *created.Efforts[0].WalkedFraction, 0.01)

	t.Run("a run without cadence or two speed modes has no walking to report", func(t *testing.T) {
		_, steady := alice.uploadWorkout(testsupport.EncodeFIT(t, routeDay.AddDate(0, 0, 1), []testsupport.Split{{Meters: 1000, Seconds: 300}}))
		status, body := alice.get(fmt.Sprintf("/workouts/%d/series", steady))
		require.Equal(t, http.StatusOK, status)
		assert.NotContains(t, string(body), `"walking"`)
		assert.NotContains(t, string(body), `"cadence"`)
	})
}
