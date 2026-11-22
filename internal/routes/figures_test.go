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
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

func (c *apiClient) workout(id int32) models.Workout {
	c.t.Helper()
	status, body := c.get(fmt.Sprintf("/workouts/%d", id))
	require.Equal(c.t, http.StatusOK, status, string(body))
	var w models.Workout
	require.NoError(c.t, json.Unmarshal(body, &w))
	return w
}

func (c *apiClient) laps(id int32) []workouts.Lap {
	c.t.Helper()
	status, body := c.get(fmt.Sprintf("/workouts/%d/laps", id))
	require.Equal(c.t, http.StatusOK, status, string(body))
	var laps []workouts.Lap
	require.NoError(c.t, json.Unmarshal(body, &laps))
	return laps
}

func TestAWorkoutKeepsWhatItsFileStates(t *testing.T) {
	srv, m, toker := newAPI(t)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	bob := newUserClient(t, srv, m, toker, "bob@example.com")
	start := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	_, id := alice.uploadWorkout(testsupport.EncodeFITFromWatch(t, start, []testsupport.Split{{Meters: 3000, Seconds: 900, StepsPerMinute: 170}}))

	w := alice.workout(id)
	require.NotNil(t, w.TimerSeconds)
	assert.InDelta(t, 870, *w.TimerSeconds, 1e-6)
	assert.InDelta(t, 900, *w.TimeSeconds, 1e-6, "start to end, as before")
	assert.Equal(t, 150.0, *w.AvgHeartRate)
	assert.Equal(t, 172.0, *w.MaxHeartRate)
	assert.Equal(t, 118.0, *w.MinHeartRate)
	assert.Equal(t, 12.0, *w.AscentMeters)
	assert.Equal(t, 8.0, *w.DescentMeters)
	assert.Equal(t, 300.0, *w.Calories)
	assert.Equal(t, 2000.0, *w.TotalSteps)
	assert.Equal(t, 170.0, *w.AvgCadence)
	assert.Equal(t, 250.0, *w.AvgPowerWatts)
	assert.InDelta(t, 2.9, *w.AvgEffortPaceMps, 1e-6)
	assert.InDelta(t, 1.1, *w.AvgStepLengthM, 1e-9)
	assert.InDelta(t, 95, *w.AvgVerticalOscillationMm, 1e-9)
	assert.InDelta(t, 280, *w.AvgGroundContactMs, 1e-9)
	assert.InDelta(t, 8.5, *w.AvgVerticalRatioPct, 1e-9)

	laps := alice.laps(id)
	require.Len(t, laps, 2)
	assert.Equal(t, "manual", laps[0].Trigger)
	assert.Equal(t, 300.0, laps[1].StartOffsetSeconds)
	assert.Equal(t, 168.0, *laps[0].AvgCadence)

	status, body := alice.get(fmt.Sprintf("/workouts/%d/series", id))
	require.Equal(t, http.StatusOK, status)
	var series map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &series))
	for _, column := range []string{"power_w", "effort_pace_mps", "step_length_m", "vertical_oscillation_mm", "ground_contact_ms", "vertical_ratio_pct"} {
		assert.Contains(t, series, column)
	}

	t.Run("steps go by the sport as corrected", func(t *testing.T) {
		status, body := alice.put(fmt.Sprintf("/workouts/%d/corrections", id), `{"sport":"cycling"}`)
		require.Equal(t, http.StatusOK, status, string(body))
		var ride models.Workout
		require.NoError(t, json.Unmarshal(body, &ride))
		assert.Nil(t, ride.TotalSteps)
		assert.Equal(t, 85.0, *ride.AvgCadence)
		assert.Equal(t, 84.0, *alice.laps(id)[0].AvgCadence)

		status, body = alice.put(fmt.Sprintf("/workouts/%d/corrections", id), `{}`)
		require.Equal(t, http.StatusOK, status, string(body))
		assert.Equal(t, 2000.0, *alice.workout(id).TotalSteps)
	})

	t.Run("the laps are the user's own", func(t *testing.T) {
		status, _ := bob.get(fmt.Sprintf("/workouts/%d/laps", id))
		assert.Equal(t, http.StatusNotFound, status)
	})
}

func TestAFileWithoutThemHasNoFiguresOrLaps(t *testing.T) {
	srv, m, toker := newAPI(t)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	start := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	_, id := alice.uploadWorkout(testsupport.EncodeFIT(t, start, []testsupport.Split{{Meters: 1000, Seconds: 300}}))

	w := alice.workout(id)
	assert.Nil(t, w.TimerSeconds)
	assert.Nil(t, w.AvgHeartRate)
	assert.Nil(t, w.TotalSteps)
	assert.Nil(t, w.AvgGroundContactMs)
	assert.Empty(t, alice.laps(id))

	status, body := alice.get(fmt.Sprintf("/workouts/%d/laps", id))
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[]`, string(body), "a list, so a client needn't tell none from not there")
}
