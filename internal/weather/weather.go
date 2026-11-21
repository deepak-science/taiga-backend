// Package weather looks up what the weather was where and when a workout was run (ROADMAP M15).
package weather

import (
	"context"
	"errors"
	"math"
	"time"
)

// Conditions at one moment, metric.
type Conditions struct {
	TemperatureC float64 `json:"temperature_c"`
	DewPointC    float64 `json:"dew_point_c"`
	HumidityPct  float64 `json:"humidity_pct"`
	WindSpeedMPS float64 `json:"wind_speed_mps"`
	// WindDirectionDeg is where the wind comes from, clockwise from north.
	WindDirectionDeg float64 `json:"wind_direction_deg"`
	CloudCoverPct    float64 `json:"cloud_cover_pct"`
}

// Reading is the weather over one workout.
type Reading struct {
	Start Conditions
	End   Conditions
	// PrecipitationMM falls from PrecipitationLead before the start to the end: models miss rain by the hour, so it only says whether it was a wet outing.
	PrecipitationMM float64
}

// PrecipitationLead is how long before the start rain still counts.
const PrecipitationLead = 3 * time.Hour

// Source is one answer a provider can give; a workout keeps a row for each it has.
type Source struct {
	Name string
	// MinAge is how long after a workout ends the source has it.
	MinAge time.Duration
}

const (
	SourceERA5    = "openmeteo-era5"
	SourceDefault = "openmeteo-default"
)

// Sources, best first: a workout's weather is its reading from the first of these it has.
// ERA5 was closer to stations on temperature at home and away (ROADMAP M15), but runs about five days behind.
var Sources = []Source{
	{Name: SourceERA5, MinAge: 7 * 24 * time.Hour},
	{Name: SourceDefault, MinAge: 24 * time.Hour},
}

// Rank is a source's place in Sources, lower is better; an unknown source ranks last.
func Rank(source string) int {
	for i, s := range Sources {
		if s.Name == source {
			return i
		}
	}
	return len(Sources)
}

// ErrNotYet means the source has no values for those hours yet; asking again later may find them.
var ErrNotYet = errors.New("the weather source has nothing for those hours yet")

// Provider answers for a place and a span of time.
type Provider interface {
	// Lookup takes coordinates as Round gives them.
	Lookup(ctx context.Context, source string, lat, lon float64, start, end time.Time) (*Reading, error)
}

// Round coarsens coordinates to 0.1°, about 10km: finer than the models' grids, and all a provider is told about where the user ran.
func Round(lat, lon float64) (float64, float64) {
	return math.Round(lat*10) / 10, math.Round(lon*10) / 10
}
