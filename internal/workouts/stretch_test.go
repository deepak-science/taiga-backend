package workouts

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pathSeries walks the path (metres east and north of a corner) at mps, a point a second.
func pathSeries(mps float64, path ...xy) *Series {
	const lat0, lon0 = 45.0, -93.0
	mPerDegLat := earthRadiusM * math.Pi / 180
	mPerDegLon := mPerDegLat * math.Cos(lat0*math.Pi/180)
	s := &Series{}
	var t, d float64
	add := func(p xy) {
		s.TimeSeconds = append(s.TimeSeconds, t)
		s.DistanceMeters = append(s.DistanceMeters, d)
		s.Latitude = append(s.Latitude, lat0+p.y/mPerDegLat)
		s.Longitude = append(s.Longitude, lon0+p.x/mPerDegLon)
	}
	add(path[0])
	for i := 1; i < len(path); i++ {
		a, b := path[i-1], path[i]
		length := math.Hypot(b.x-a.x, b.y-a.y)
		for covered := mps; covered <= length; covered += mps {
			t++
			d += mps
			add(xy{a.x + (b.x-a.x)*covered/length, a.y + (b.y-a.y)*covered/length})
		}
	}
	return s
}

func TestBuildStretch(t *testing.T) {
	s := pathSeries(2.5, xy{0, 0}, xy{0, 2000})

	st, err := BuildStretch(s, 500, 1000)
	require.NoError(t, err)
	assert.Equal(t, 500.0, st.Meters)
	require.Len(t, st.Points, 51, "a point every 10m, both ends included")
	assert.InDelta(t, 45+500/(earthRadiusM*math.Pi/180), st.Points[0][0], 1e-6)

	for name, r := range map[string][2]float64{"too short": {500, 550}, "backwards": {1000, 500}, "past the end": {1500, 2500}, "not a number": {math.NaN(), 800}} {
		_, err := BuildStretch(s, r[0], r[1])
		assert.ErrorIs(t, err, ErrStretchRange, name)
	}
	_, err = BuildStretch(&Series{TimeSeconds: Values{0, 1}, DistanceMeters: Values{0, 500}}, 0, 400)
	assert.ErrorIs(t, err, ErrStretchRange, "no GPS")
}

func TestPasses(t *testing.T) {
	source := pathSeries(2.5, xy{0, 0}, xy{0, 2000})
	st, err := BuildStretch(source, 500, 1000)
	require.NoError(t, err)

	passes := st.Passes(source)
	require.Len(t, passes, 1)
	assert.InDelta(t, 200, passes[0].StartSeconds, 0.5)
	assert.InDelta(t, 400, passes[0].EndSeconds, 0.5)
	assert.InDelta(t, 500, passes[0].StartMeters, 1)
	assert.True(t, math.IsNaN(passes[0].WalkedFraction), "nothing to tell walking by")

	// A slower run on the far side of the road, 20m over, is still a pass; its points don't land on the stretch's ends, so the time is interpolated.
	across := st.Passes(pathSeries(3, xy{20, 0}, xy{20, 2000}))
	require.Len(t, across, 1)
	assert.InDelta(t, 500.0/3, across[0].EndSeconds-across[0].StartSeconds, 0.5)

	assert.Empty(t, st.Passes(pathSeries(2.5, xy{50, 0}, xy{50, 2000})), "the next street over")
	assert.Empty(t, st.Passes(pathSeries(2.5, xy{0, 2000}, xy{0, 0})), "the other way is another stretch")
	assert.Empty(t, st.Passes(pathSeries(2.5, xy{0, 0}, xy{0, 800}, xy{300, 800})), "turning off partway")
	assert.Empty(t, st.Passes(pathSeries(2.5, xy{0, 700}, xy{0, 2000})), "joining partway")

	outAndBack := st.Passes(pathSeries(2.5, xy{0, 0}, xy{0, 1500}, xy{0, 0}))
	assert.Len(t, outAndBack, 1, "only the way out follows it")

	laps := st.Passes(pathSeries(2.5, xy{0, 0}, xy{0, 1200}, xy{200, 1200}, xy{200, 300}, xy{0, 300}, xy{0, 1200}))
	require.Len(t, laps, 2, "an effort is a pass, not a workout")
	assert.Greater(t, laps[1].StartSeconds, laps[0].EndSeconds)
}

func TestPassesSkipsADetourAroundPartOfTheStretch(t *testing.T) {
	source := pathSeries(2.5, xy{0, 0}, xy{0, 2000})
	st, err := BuildStretch(source, 500, 1500)
	require.NoError(t, err)
	detour := pathSeries(2.5, xy{0, 0}, xy{0, 800}, xy{100, 800}, xy{100, 1200}, xy{0, 1200}, xy{0, 2000})
	assert.Empty(t, st.Passes(detour))
}

func TestPassesWalkedFraction(t *testing.T) {
	// 400m run, 150m walked, 450m run: the stretch from 300m to 800m has the whole walk in it.
	s := seriesOf(leg{160, 2.5, 144}, leg{100, 1.5, 108}, leg{180, 2.5, 144})
	s.Walking = DetectWalking(s)
	require.NotNil(t, s.Walking)
	st, err := BuildStretch(s, 300, 800)
	require.NoError(t, err)

	passes := st.Passes(s)
	require.Len(t, passes, 1)
	// 100 of the pass's 240 seconds.
	assert.InDelta(t, 100.0/240, passes[0].WalkedFraction, 0.03)
}

func TestMightPass(t *testing.T) {
	st, err := BuildStretch(pathSeries(2.5, xy{0, 0}, xy{0, 2000}), 500, 1000)
	require.NoError(t, err)
	assert.True(t, st.MightPass(BuildTrack(runAlong([]xy{{0, 0}, {0, 2000}}, 1))))
	assert.True(t, st.MightPass(BuildTrack(runAlong([]xy{{0, 2000}, {0, 0}}, 1))), "direction is for Passes to settle")
	assert.False(t, st.MightPass(BuildTrack(runAlong([]xy{{0, 0}, {0, 800}, {600, 800}}, 1))), "never reaches the end")
	assert.False(t, st.MightPass(nil))
}
