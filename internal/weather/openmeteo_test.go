package weather_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/weather"
)

var (
	start = time.Date(2026, 11, 1, 7, 30, 0, 0, time.UTC)
	end   = time.Date(2026, 11, 1, 8, 15, 0, 0, time.UTC)
)

func TestRoundKeepsATenthOfADegree(t *testing.T) {
	lat, lon := weather.Round(43.0731, -89.4012)
	assert.Equal(t, 43.1, lat)
	assert.Equal(t, -89.4, lon)
}

func TestSourcesAreRankedBestFirst(t *testing.T) {
	assert.Less(t, weather.Rank(weather.SourceERA5), weather.Rank(weather.SourceDefault))
	assert.Less(t, weather.Rank(weather.SourceDefault), weather.Rank("something else"))
}

func TestLookupInterpolatesStartAndEnd(t *testing.T) {
	fake := testsupport.NewFakeWeather(t)
	reading, err := weather.NewOpenMeteo(fake.URL()).Lookup(t.Context(), weather.SourceERA5, 43.1, -89.4, start, end)
	require.NoError(t, err)

	assert.InDelta(t, 7.5, reading.Start.TemperatureC, 1e-9, "halfway between the hours either side")
	assert.InDelta(t, 8.25, reading.End.TemperatureC, 1e-9)
	assert.InDelta(t, 2.5, reading.Start.DewPointC, 1e-9)
	assert.InDelta(t, 70, reading.Start.HumidityPct, 1e-9)
	assert.InDelta(t, 40, reading.End.CloudCoverPct, 1e-9)
	// Halfway from 10° to 350° is north, not south.
	assert.InDelta(t, 0, min(reading.Start.WindDirectionDeg, 360-reading.Start.WindDirectionDeg), 1e-6)
	assert.InDelta(t, 3, reading.Start.WindSpeedMPS, 0.1)
	// The hours ending 05:00 to 09:00: three before the start, and the ones the run is in.
	assert.InDelta(t, 2.5, reading.PrecipitationMM, 1e-9)
}

func TestLookupSendsOnlyThePlaceAndTheDays(t *testing.T) {
	fake := testsupport.NewFakeWeather(t)
	om := weather.NewOpenMeteo(fake.URL())
	_, err := om.Lookup(t.Context(), weather.SourceDefault, 43.1, -89.4, start, end)
	require.NoError(t, err)
	_, err = om.Lookup(t.Context(), weather.SourceERA5, 43.1, -89.4, start, end)
	require.NoError(t, err)

	requests := fake.Requests()
	require.Len(t, requests, 2)
	q := requests[0]
	assert.Equal(t, "43.1", q.Get("latitude"))
	assert.Equal(t, "-89.4", q.Get("longitude"))
	assert.Equal(t, "2026-11-01", q.Get("start_date"))
	assert.Equal(t, "2026-11-01", q.Get("end_date"))
	assert.Equal(t, "ms", q.Get("wind_speed_unit"))
	assert.Len(t, q, 6, "nothing else goes out: %v", q)
	assert.False(t, q.Has("models"), "the default blend is what the archive gives unasked")
	assert.Equal(t, "era5", requests[1].Get("models"))
}

func TestLookupAsksForTheDayBeforeARunJustAfterMidnight(t *testing.T) {
	fake := testsupport.NewFakeWeather(t)
	early := time.Date(2026, 11, 2, 0, 30, 0, 0, time.UTC)
	late := time.Date(2026, 11, 2, 23, 40, 0, 0, time.UTC)
	_, err := weather.NewOpenMeteo(fake.URL()).Lookup(t.Context(), weather.SourceERA5, 43.1, -89.4, early, late)
	require.NoError(t, err)
	q := fake.Requests()[0]
	assert.Equal(t, "2026-11-01", q.Get("start_date"))
	assert.Equal(t, "2026-11-03", q.Get("end_date"))
}

func TestLookupNotYet(t *testing.T) {
	fake := testsupport.NewFakeWeather(t)
	fake.SetEmpty(true)
	_, err := weather.NewOpenMeteo(fake.URL()).Lookup(t.Context(), weather.SourceERA5, 43.1, -89.4, start, end)
	assert.ErrorIs(t, err, weather.ErrNotYet)
}

func TestLookupFailures(t *testing.T) {
	fake := testsupport.NewFakeWeather(t)
	om := weather.NewOpenMeteo(fake.URL())
	_, err := om.Lookup(t.Context(), "nowhere", 43.1, -89.4, start, end)
	assert.ErrorContains(t, err, "no source")
	assert.Empty(t, fake.Requests(), "an unknown source asks nothing")

	fake.SetStatus(http.StatusTooManyRequests)
	_, err = om.Lookup(t.Context(), weather.SourceERA5, 43.1, -89.4, start, end)
	require.Error(t, err)
	assert.NotErrorIs(t, err, weather.ErrNotYet)
	assert.ErrorContains(t, err, "429")
}
