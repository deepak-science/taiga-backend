package workouts

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func offset(hours float64) *int32 {
	seconds := int32(hours * 3600)
	return &seconds
}

func TestName(t *testing.T) {
	// A Saturday; 20:30 UTC is 14:30 in Madison (CST).
	saturday := time.Date(2026, 11, 21, 20, 30, 0, 0, time.UTC)
	cases := []struct {
		what    string
		summary RunSummary
		want    string
	}{
		{"a place leads", RunSummary{StartTime: saturday, Sport: "running", Place: "Madison", UTCOffsetSeconds: offset(-6)}, "Madison afternoon run"},
		{"the weekday without one", RunSummary{StartTime: saturday, Sport: "running", UTCOffsetSeconds: offset(-6)}, "Saturday afternoon run"},
		{"the day is the local one", RunSummary{StartTime: saturday, Sport: "walking", UTCOffsetSeconds: offset(11)}, "Sunday morning walk"},
		{"no offset, no part of day", RunSummary{StartTime: saturday, Sport: "running", SubSport: "treadmill"}, "Treadmill run"},
		{"treadmill", RunSummary{StartTime: saturday, Sport: "running", SubSport: "indoor_running", UTCOffsetSeconds: offset(-6)}, "Saturday afternoon treadmill run"},
		{"a treadmill session corrected to a walk", RunSummary{StartTime: saturday, Sport: "walking", SubSport: "treadmill", UTCOffsetSeconds: offset(-6)}, "Saturday afternoon walk"},
		{"trail", RunSummary{StartTime: saturday, Sport: "running", SubSport: "trail", Place: "Boulder", UTCOffsetSeconds: offset(-7)}, "Boulder lunchtime trail run"},
		{"a ride", RunSummary{StartTime: saturday, Sport: "cycling", Place: "Madison", UTCOffsetSeconds: offset(-6)}, "Madison afternoon ride"},
		{"a sport with no noun of its own", RunSummary{StartTime: saturday, Sport: "stair_climbing", UTCOffsetSeconds: offset(-6)}, "Saturday afternoon stair climbing"},
		{"no sport", RunSummary{StartTime: saturday}, "Workout"},
		{"generic", RunSummary{StartTime: saturday, Sport: "generic", UTCOffsetSeconds: offset(-6)}, "Saturday afternoon workout"},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			assert.Equal(t, c.want, c.summary.Name())
		})
	}
}

func TestPartOfDay(t *testing.T) {
	want := map[int]string{
		0: "late night", 3: "late night",
		4: "early morning", 6: "early morning",
		7: "morning", 10: "morning",
		11: "lunchtime", 13: "lunchtime",
		14: "afternoon", 16: "afternoon",
		17: "evening", 20: "evening",
		21: "night", 23: "night",
	}
	for hour, name := range want {
		assert.Equal(t, name, partOfDay(hour), "%d:00", hour)
	}
}

func TestStatedOffset(t *testing.T) {
	utc := time.Date(2024, 3, 23, 21, 17, 35, 0, time.UTC)
	assert.Equal(t, offset(-5), statedOffset(utc, utc.Add(-5*time.Hour)))
	assert.Equal(t, offset(5.75), statedOffset(utc, utc.Add(5*time.Hour+45*time.Minute)), "Nepal")
	assert.Equal(t, offset(0), statedOffset(utc, utc))
	assert.Nil(t, statedOffset(utc, time.Time{}), "no local time")
	assert.Nil(t, statedOffset(utc, utc.Add(20*time.Hour)), "not an offset any zone has")
}

func TestLocate(t *testing.T) {
	// 01:30 UTC on the 22nd is 19:30 on the 21st in Madison.
	start := time.Date(2026, 11, 22, 1, 30, 0, 0, time.UTC)
	nan := RecordData{Latitude: math.NaN(), Longitude: math.NaN()}

	run := &Run{Summary: RunSummary{StartTime: start, Sport: "running"}, Records: []*RecordData{&nan, {Latitude: 43.0747, Longitude: -89.3842}, {Latitude: 51.5, Longitude: -0.12}}}
	run.locate()
	assert.Equal(t, "Madison", run.Summary.Place)
	assert.Equal(t, "America/Chicago", run.Summary.Timezone)
	assert.Equal(t, offset(-6), run.Summary.UTCOffsetSeconds)
	assert.Equal(t, "Madison evening run", run.Summary.Name())

	summer := &Run{Summary: RunSummary{StartTime: start.AddDate(0, -5, 0)}, Records: []*RecordData{{Latitude: 43.0747, Longitude: -89.3842}}}
	summer.locate()
	assert.Equal(t, offset(-5), summer.Summary.UTCOffsetSeconds, "daylight time")

	indoors := &Run{Summary: RunSummary{StartTime: start, UTCOffsetSeconds: offset(-6)}, Records: []*RecordData{&nan}}
	indoors.locate()
	assert.Empty(t, indoors.Summary.Place)
	assert.Empty(t, indoors.Summary.Timezone)
	assert.Equal(t, offset(-6), indoors.Summary.UTCOffsetSeconds, "the file's own offset stays")
}
