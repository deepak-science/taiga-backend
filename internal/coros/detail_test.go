package coros

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recorded(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	require.Equal(t, byte('"'), raw[0], "the details arrive as a JSON string, like the list")
	return unquote(string(raw))
}

func TestParseARunsDetailsAsCOROSSendsThem(t *testing.T) {
	text := recorded(t, "activity-detail.txt")
	d, err := parseDetail(text)
	require.NoError(t, err)

	assert.Equal(t, text, d.Reply)
	assert.Equal(t, 51*60+40.0, *d.WorkoutSeconds)
	assert.Equal(t, 52*60+5.0, *d.TotalSeconds)
	assert.Equal(t, 7630.0, *d.DistanceMeters)
	assert.Equal(t, 407.0, *d.AvgPaceSecondsPerKm)
	assert.Equal(t, 398.0, *d.MovingAvgPaceSecondsPerKm)
	assert.Equal(t, 403.0, *d.AdjustedPaceSecondsPerKm)
	assert.Equal(t, 380.0, *d.BestKmSeconds)
	assert.Equal(t, 150.0, *d.AvgHeartRate)
	assert.Equal(t, 170.0, *d.AvgCadence)
	assert.Equal(t, 1.05, *d.AvgStrideLengthMeters)
	assert.Equal(t, 250.0, *d.AvgPowerWatts)
	assert.Equal(t, 54.0, *d.ElevationGainMeters)
	assert.Equal(t, 51.0, *d.ElevationLossMeters)
	assert.Equal(t, 300.0, *d.Calories)
	assert.Equal(t, 120.0, *d.TrainingLoad)
	assert.Equal(t, 3.6, *d.AerobicTE)
	assert.Equal(t, 0.9, *d.AnaerobicTE)
	assert.Equal(t, "Threshold", d.TrainingFocus)
	assert.Equal(t, "Best", d.Performance)
}

func TestASportWithFewerFiguresLeavesTheRestOut(t *testing.T) {
	d, err := parseDetail(recorded(t, "activity-detail-gym.txt"))
	require.NoError(t, err)

	assert.Equal(t, 3971.0, *d.WorkoutSeconds, "an hour and more is written with three parts")
	assert.Equal(t, 19.0, *d.TrainingLoad)
	assert.Equal(t, 0.0, *d.AnaerobicTE, "none, which isn't the same as not said")
	assert.Equal(t, "Recovery", d.TrainingFocus)
	assert.Nil(t, d.DistanceMeters)
	assert.Nil(t, d.AvgPaceSecondsPerKm)
	assert.Nil(t, d.ElevationGainMeters)
	assert.Empty(t, d.Performance)
}

func TestAnAccountInMilesIsStoredInMetres(t *testing.T) {
	d, err := parseDetail("Outdoor Run Activity Details\n====\n\nDistance: 3.00 mi\nAverage Pace: 8:00 /mi\nElevation Gain / Loss: 100 ft / 50 ft\n")
	require.NoError(t, err)
	assert.InDelta(t, 4828.03, *d.DistanceMeters, 0.01)
	assert.InDelta(t, 298.26, *d.AvgPaceSecondsPerKm, 0.01)
	assert.InDelta(t, 30.48, *d.ElevationGainMeters, 1e-9)
	assert.InDelta(t, 15.24, *d.ElevationLossMeters, 1e-9)
}

func TestDetailsThatDoNotAddUpAreNotGuessedAt(t *testing.T) {
	run := recorded(t, "activity-detail.txt")
	for name, text := range map[string]string{
		"something else entirely":  "Sorry, something went wrong.",
		"nothing known in it":      "Outdoor Run Activity Details\n====\n\nVibes: good\n",
		"a unit not seen before":   strings.Replace(run, "7.63 km", "7.63 leagues", 1),
		"a pace per something new": strings.Replace(run, "6:47 /km", "6:47 /500m", 1),
		"a time that isn't one":    strings.Replace(run, "Workout Time: 51:40", "Workout Time: about an hour", 1),
		"sixty seconds or more":    strings.Replace(run, "6:47 /km", "6:99 /km", 1),
		"sixty minutes in an hour": strings.Replace(run, "Workout Time: 51:40", "Workout Time: 1:99:00", 1),
		"hours past counting":      strings.Replace(run, "Workout Time: 51:40", "Workout Time: 99999999999999999999:00:00", 1),
		"a number that isn't one":  strings.Replace(run, "Training Load: 120", "Training Load: high", 1),
		"a gain without a loss":    strings.Replace(run, "54 m / 51 m", "54 m", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseDetail(text)
			assert.ErrorIs(t, err, ErrUnexpectedReply)
		})
	}
}

func TestWhatIsNotKnownIsPassedOver(t *testing.T) {
	run := recorded(t, "activity-detail.txt")
	d, err := parseDetail(run + "\nRunning Economy: 4.2 kJ/kg/km\nAverage Power: --")
	require.NoError(t, err)
	assert.Equal(t, 120.0, *d.TrainingLoad)
	assert.Nil(t, d.AvgPowerWatts, "the later line says COROS has none")
	assert.Contains(t, d.Reply, "Running Economy", "kept, for the day it is read")
}

func TestAnEmptyValueDoesNotTakeTheNextLine(t *testing.T) {
	d, err := parseDetail("Outdoor Run Activity Details\n====\n\nAverage Power:\nTraining Focus:\nPerformance: Best\nTraining Load:   \nAerobic TE: 3.6\n")
	require.NoError(t, err)
	assert.Empty(t, d.TrainingFocus)
	assert.Equal(t, "Best", d.Performance)
	assert.Nil(t, d.AvgPowerWatts)
	assert.Nil(t, d.TrainingLoad)
	assert.Equal(t, 3.6, *d.AerobicTE)
}
