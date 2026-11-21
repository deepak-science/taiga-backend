package places

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLookup(t *testing.T) {
	cases := []struct {
		what     string
		lat, lon float64
		name     string
		timezone string
	}{
		{"a city centre", 43.0747, -89.3842, "Madison", "America/Chicago"},
		// 5km from Middleton's centre and 5.7km from Madison's, but well inside Madison.
		{"a city's edge, nearer a suburb's centre", 43.060, -89.470, "Madison", "America/Chicago"},
		{"the suburb itself", 43.097, -89.504, "Middleton", "America/Chicago"},
		{"a suburb inside a metropolis", 42.045, -87.688, "Evanston", "America/Chicago"},
		{"another hemisphere", -33.8568, 151.2153, "Sydney", "Australia/Sydney"},
		{"a non-ASCII name", 47.3769, 8.5417, "Zürich", "Europe/Zurich"},
		{"a trail far from any town", 44.50, -110.60, "", "America/Denver"},
		{"open sea", 30.0, -45.0, "", ""},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			got := Lookup(c.lat, c.lon)
			assert.Equal(t, c.name, got.Name)
			assert.Equal(t, c.timezone, got.Timezone)
		})
	}
}

func TestLookupRejectsWhatIsNotAPoint(t *testing.T) {
	for _, p := range [][2]float64{{math.NaN(), 0}, {0, math.NaN()}, {91, 0}, {0, 181}} {
		assert.Equal(t, Place{}, Lookup(p[0], p[1]))
	}
}

func TestEveryZoneLoads(t *testing.T) {
	_, zones := load()
	assert.Greater(t, len(zones), 300)
	for _, zone := range zones {
		assert.NotNil(t, Place{Timezone: zone}.Location(), zone)
	}
}

func TestLocationWithoutAZone(t *testing.T) {
	assert.Nil(t, Place{}.Location())
}

func BenchmarkLookup(b *testing.B) {
	for b.Loop() {
		Lookup(43.0747, -89.3842)
	}
}
