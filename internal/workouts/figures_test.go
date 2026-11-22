package workouts_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

func watchRun(t *testing.T) *workouts.Run {
	t.Helper()
	start := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	file := testsupport.EncodeFITFromWatch(t, start, []testsupport.Split{{Meters: 3000, Seconds: 900, StepsPerMinute: 170}})
	run, err := workouts.ReadActivityFile(bytes.NewReader(file))
	require.NoError(t, err)
	return run
}

func value(t *testing.T, v *float64) float64 {
	t.Helper()
	require.NotNil(t, v)
	return *v
}

func TestTheFilesOwnFiguresAreRead(t *testing.T) {
	f := watchRun(t).Summary.Figures.Counted(workouts.SportRunning)

	assert.InDelta(t, 870, value(t, f.TimerSeconds), 1e-6, "the timer stopped for 30 of the 900 seconds")
	assert.Equal(t, 150.0, value(t, f.AvgHeartRate))
	assert.Equal(t, 172.0, value(t, f.MaxHeartRate))
	assert.Equal(t, 118.0, value(t, f.MinHeartRate))
	assert.Equal(t, 12.0, value(t, f.AscentMeters))
	assert.Equal(t, 8.0, value(t, f.DescentMeters))
	assert.Equal(t, 300.0, value(t, f.Calories))
	assert.Equal(t, 250.0, value(t, f.AvgPowerWatts))
	assert.InDelta(t, 2.9, value(t, f.AvgEffortPaceMPS), 1e-6)
	assert.InDelta(t, 1.1, value(t, f.AvgStepLengthMeters), 1e-9, "the file has millimetres")
	assert.InDelta(t, 95, value(t, f.AvgVerticalOscillationMM), 1e-9)
	assert.InDelta(t, 280, value(t, f.AvgGroundContactMS), 1e-9)
	assert.InDelta(t, 8.5, value(t, f.AvgVerticalRatioPct), 1e-9)
	// The file counts one foot.
	assert.Equal(t, 2000.0, value(t, f.TotalSteps))
	assert.Equal(t, 170.0, value(t, f.AvgCadence))
}

func TestStridesAreOnlyStepsOnFoot(t *testing.T) {
	f := watchRun(t).Summary.Figures.Counted("cycling")
	assert.Nil(t, f.TotalSteps)
	assert.Equal(t, 85.0, value(t, f.AvgCadence), "revolutions, as the file has them")
}

func TestAFileThatStatesNothingHasNoFigures(t *testing.T) {
	start := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	file := testsupport.EncodeFIT(t, start, []testsupport.Split{{Meters: 1000, Seconds: 300}})
	run, err := workouts.ReadActivityFile(bytes.NewReader(file))
	require.NoError(t, err)

	encoded, err := json.Marshal(run.Summary.Figures.Counted(workouts.SportRunning))
	require.NoError(t, err)
	var figures map[string]*float64
	require.NoError(t, json.Unmarshal(encoded, &figures))
	assert.Len(t, figures, 15)
	for name, v := range figures {
		assert.Nil(t, v, name)
	}
	assert.Empty(t, run.Laps)

	series := workouts.BuildSeries(run, workouts.MaxSeriesPoints)
	assert.Nil(t, series.PowerWatts)
	assert.Nil(t, series.EffortPaceMPS)
	assert.Nil(t, series.GroundContactMS)
}

func TestLapsAreKeptAsRecorded(t *testing.T) {
	run := watchRun(t)
	laps := workouts.BuildLaps(run)
	require.Len(t, laps, 2)

	first, second := laps[0], laps[1]
	assert.Zero(t, first.StartOffsetSeconds)
	assert.Equal(t, 300.0, value(t, first.ElapsedSeconds))
	assert.InDelta(t, 1000, value(t, first.DistanceMeters), 0.01)
	assert.Equal(t, "manual", first.Trigger, "the lap button")
	assert.Equal(t, 140.0, value(t, first.AvgHeartRate))
	assert.Equal(t, 168.0, value(t, first.AvgCadence), "steps, like the workout's")
	assert.InDelta(t, 2.8, value(t, first.AvgEffortPaceMPS), 1e-6)
	assert.InDelta(t, 275, value(t, first.AvgGroundContactMS), 1e-9)

	assert.Equal(t, 300.0, second.StartOffsetSeconds)
	assert.Equal(t, 600.0, value(t, second.ElapsedSeconds))
	assert.Equal(t, "session_end", second.Trigger)
	// What a lap doesn't state is left out, not zero.
	assert.Nil(t, second.AvgPowerWatts)
	assert.Nil(t, second.TotalSteps)
	assert.Nil(t, second.TimerSeconds)

	encoded, err := json.Marshal(laps)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"avg_power_watts":null`)
	assert.Contains(t, string(encoded), `"avg_ground_contact_ms":283`)
}

func TestTheSeriesCarriesTheStride(t *testing.T) {
	run := watchRun(t)
	series := workouts.BuildSeries(run, workouts.MaxSeriesPoints)
	n := len(series.TimeSeconds)
	require.Greater(t, n, 2)

	for name, column := range map[string]workouts.Values{
		"power": series.PowerWatts, "effort pace": series.EffortPaceMPS, "step length": series.StepLengthMeters,
		"vertical oscillation": series.VerticalOscillationMM, "ground contact": series.GroundContactMS, "vertical ratio": series.VerticalRatioPct,
	} {
		require.Len(t, column, n, name)
	}
	assert.Equal(t, 250.0, series.PowerWatts[1])
	assert.Equal(t, 3.0, series.EffortPaceMPS[1])
	assert.Equal(t, 1.1, series.StepLengthMeters[1])
	assert.Equal(t, 95.0, series.VerticalOscillationMM[1])
	assert.Equal(t, 280.0, series.GroundContactMS[1])
	assert.Equal(t, 8.5, series.VerticalRatioPct[1])

	// A zero is the watch saying it didn't measure that stride.
	encoded, err := json.Marshal(series)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"ground_contact_ms":[null,280,`)
	assert.Contains(t, string(encoded), `"step_length_m":[1.1,1.1,`)
}
