// Package places names where a point is, and its timezone, from a list of towns built into the binary, so no lookup leaves the server.
package places

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	// The image has no zoneinfo of its own, and the zones named here have to load.
	_ "time/tzdata"
)

// cities.tsv.gz is GeoNames' cities5000 (https://www.geonames.org, CC BY 4.0) cut down by ./gen to name, latitude, longitude, population and timezone.
//
//go:generate go run ./gen cities5000.zip cities.tsv.gz
//go:embed cities.tsv.gz
var citiesGz []byte

// Place is the town a point is in or beside. Name is empty when it's out of reach of every town, and Timezone (an IANA name) when the nearest one is too far to trust.
type Place struct {
	Name     string
	Timezone string
}

// Location loads the place's zone; nil when it has none.
func (p Place) Location() *time.Location {
	if p.Timezone == "" {
		return nil
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return nil
	}
	return loc
}

type city struct {
	name       string
	lat, lon   float64
	reachKm    float64
	timezoneID int
}

const (
	// A town is a point and a population, with no outline, so its extent is guessed as a disc at this density.
	// It's low for a city centre and high for a sprawling suburb; the slack below covers both.
	peoplePerSqKm = 1500
	// How far past that disc a town still names a point: the edge of town, and the trail just outside it.
	slackKm = 3
	// A point further than this multiple of a town's reach belongs to none.
	maxReach = 1.5
	// Without a town in reach the nearest one still gives the zone, up to here; beyond it (open sea) a guess would be worse than none.
	maxZoneKm = 300

	earthRadiusKm = 6371.0
)

var load = sync.OnceValues(func() ([]city, []string) {
	zr, err := gzip.NewReader(bytes.NewReader(citiesGz))
	if err != nil {
		panic(fmt.Sprintf("places: embedded list: %v", err))
	}
	var (
		cities  []city
		zones   []string
		zoneIDs = map[string]int{}
	)
	lines := bufio.NewScanner(zr)
	for lines.Scan() {
		f := strings.Split(lines.Text(), "\t")
		if len(f) != 5 {
			panic(fmt.Sprintf("places: embedded list: bad row %q", lines.Text()))
		}
		lat, errLat := strconv.ParseFloat(f[1], 64)
		lon, errLon := strconv.ParseFloat(f[2], 64)
		population, errPop := strconv.ParseFloat(f[3], 64)
		if errLat != nil || errLon != nil || errPop != nil {
			panic(fmt.Sprintf("places: embedded list: bad row %q", lines.Text()))
		}
		id, ok := zoneIDs[f[4]]
		if !ok {
			id = len(zones)
			zoneIDs[f[4]] = id
			zones = append(zones, f[4])
		}
		cities = append(cities, city{
			name:       f[0],
			lat:        lat,
			lon:        lon,
			reachKm:    math.Sqrt(population/(peoplePerSqKm*math.Pi)) + slackKm,
			timezoneID: id,
		})
	}
	if err := lines.Err(); err != nil {
		panic(fmt.Sprintf("places: embedded list: %v", err))
	}
	return cities, zones
})

// Lookup finds the town a point is in or beside.
// With only centres and populations to go on, each town's reach grows with its size and the point goes to the town it's deepest inside, so the west side of a city is the city's and not the suburb whose centre happens to be nearer.
func Lookup(lat, lon float64) Place {
	if math.IsNaN(lat) || math.IsNaN(lon) || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return Place{}
	}
	cities, zones := load()
	var (
		best, nearest        *city
		bestScore, nearestKm = math.Inf(1), math.Inf(1)
	)
	for i := range cities {
		c := &cities[i]
		// A degree of latitude is 111km, so this skips nearly every town without the trigonometry.
		if math.Abs(c.lat-lat) > maxZoneKm/111.0 {
			continue
		}
		km := distanceKm(lat, lon, c.lat, c.lon)
		if km < nearestKm {
			nearest, nearestKm = c, km
		}
		if score := km / c.reachKm; score < bestScore {
			best, bestScore = c, score
		}
	}
	switch {
	case best != nil && bestScore <= maxReach:
		return Place{Name: best.name, Timezone: zones[best.timezoneID]}
	case nearest != nil && nearestKm <= maxZoneKm:
		return Place{Timezone: zones[nearest.timezoneID]}
	}
	return Place{}
}

func distanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusKm * math.Asin(math.Sqrt(a))
}
