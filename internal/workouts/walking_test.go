package workouts

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leg is a spell at one speed and cadence; a NaN cadence leaves the column out.
type leg struct {
	seconds, mps, cadence float64
}

// seriesOf lays the legs end to end due north, a point a second.
func seriesOf(legs ...leg) *Series {
	s := &Series{}
	var t, d float64
	withCadence := false
	add := func(cadence float64) {
		s.TimeSeconds = append(s.TimeSeconds, t)
		s.DistanceMeters = append(s.DistanceMeters, d)
		s.Latitude = append(s.Latitude, 45+d/111_195)
		s.Longitude = append(s.Longitude, -93)
		s.Cadence = append(s.Cadence, cadence)
		withCadence = withCadence || !math.IsNaN(cadence)
	}
	add(legs[0].cadence)
	for _, l := range legs {
		for range int(l.seconds) {
			t++
			d += l.mps
			add(l.cadence)
		}
	}
	if !withCadence {
		s.Cadence = nil
	}
	return s
}

func TestDetectWalkingByCadence(t *testing.T) {
	walk, run := leg{60, 1.5, 108}, leg{300, 2.6, 144}
	s := seriesOf(walk, run, leg{40, 1.5, 108}, run, leg{90, 1.5, 108})

	w := DetectWalking(s)
	require.NotNil(t, w)
	assert.Equal(t, WalkingByCadence, w.Method)
	assert.InDelta(t, 60, w.WarmupSeconds, 2)
	assert.InDelta(t, 90, w.CooldownSeconds, 2)
	require.Len(t, w.Breaks, 1, "the warm-up and cool-down aren't breaks")
	assert.InDelta(t, 360, w.Breaks[0].StartSeconds, 2)
	assert.InDelta(t, 400, w.Breaks[0].EndSeconds, 2)
	assert.InDelta(t, 40, w.WalkSeconds, 2)
}

func TestDetectWalkingIgnoresShortAndStoppedSpells(t *testing.T) {
	run := leg{120, 2.6, 144}
	// Five seconds of walking is too short, and standing at a light isn't walking however long it lasts.
	s := seriesOf(run, leg{5, 1.5, 108}, run, leg{40, 0, 0}, run)

	w := DetectWalking(s)
	require.NotNil(t, w)
	assert.Empty(t, w.Breaks)
	assert.Zero(t, w.WarmupSeconds)
}

func TestDetectWalkingJoinsBreaksAFewSecondsApart(t *testing.T) {
	run, walk := leg{120, 2.6, 144}, leg{30, 1.5, 108}
	s := seriesOf(run, walk, leg{3, 2.6, 144}, walk, run)

	w := DetectWalking(s)
	require.NotNil(t, w)
	require.Len(t, w.Breaks, 1)
	assert.InDelta(t, 63, w.Breaks[0].EndSeconds-w.Breaks[0].StartSeconds, 3)
}

func TestDetectWalkingDoubledCadenceFallsBackToSpeed(t *testing.T) {
	run := leg{120, 2.6, 144}
	s := seriesOf(run, leg{60, 2.6, 216}, run, leg{40, 1.5, 216}, run)

	w := DetectWalking(s)
	require.NotNil(t, w)
	require.Len(t, w.Breaks, 1, "a doubled reading at running speed is running, at walking speed walking")
	assert.InDelta(t, 300, w.Breaks[0].StartSeconds, 6)
}

func TestDetectWalkingBySpeedNeedsTwoModes(t *testing.T) {
	none := math.NaN()
	steady := seriesOf(leg{600, 2.6, none})
	// A steady run would be cut in half by any split, so nothing is claimed.
	assert.Nil(t, DetectWalking(steady))

	// One hump: speeds drifting between 2.2 and 3.0 with no gap.
	var drift []leg
	for i := range 80 {
		drift = append(drift, leg{10, 2.2 + 0.01*float64(i), none})
	}
	assert.Nil(t, DetectWalking(seriesOf(drift...)))

	run := leg{300, 2.6, none}
	w := DetectWalking(seriesOf(run, leg{60, 1.4, none}, run, leg{60, 1.4, none}, run))
	require.NotNil(t, w)
	assert.Equal(t, WalkingBySpeed, w.Method)
	assert.Len(t, w.Breaks, 2)
}

func TestDetectWalkingNeedsSteadyRunning(t *testing.T) {
	assert.Nil(t, DetectWalking(seriesOf(leg{600, 1.5, 108})), "a run that was all walked has no run proper to put breaks in")
	assert.Nil(t, DetectWalking(&Series{TimeSeconds: Values{}, DistanceMeters: Values{}}))
}

func TestBuildSeriesOnlyLooksForWalkingInRuns(t *testing.T) {
	build := func(sport string) *Series {
		run := &Run{Summary: RunSummary{Sport: sport}}
		var d float64
		for i := range 400 {
			speed, cadence := 2.6, 144.0
			if i >= 150 && i < 200 {
				speed, cadence = 1.5, 108
			}
			d += speed
			run.Records = append(run.Records, &RecordData{
				Distance: &Distance{d}, DurationFromStart: time.Duration(i) * time.Second, SpeedMeterPerSec: speed, Cadence: cadence,
				AltitudeMeters: math.NaN(), HeartRate: math.NaN(), Latitude: math.NaN(), Longitude: math.NaN(),
			})
		}
		return BuildSeries(run, MaxSeriesPoints)
	}
	running := build(SportRunning)
	require.NotNil(t, running.Walking)
	assert.Len(t, running.Walking.Breaks, 1)
	assert.Equal(t, 144.0, running.Cadence[0])
	assert.Nil(t, build(SportWalking).Walking)
}

func TestValuesRoundTripNull(t *testing.T) {
	raw, err := Values{1.5, math.NaN(), 3}.MarshalJSON()
	require.NoError(t, err)
	var back Values
	require.NoError(t, back.UnmarshalJSON(raw))
	require.Len(t, back, 3)
	assert.Equal(t, 1.5, back[0])
	assert.True(t, math.IsNaN(back[1]))
}
