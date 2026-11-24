package workouts

import (
	"errors"
	"fmt"
	"math"
)

// Stretch matching. Tuned on ten years of runs over the same roads (docs/roadmap-archive.md, M13).
const (
	// StretchSpacingMeters is the step between a stretch's points.
	// Stored stretches use it, so changing it means rebuilding them.
	StretchSpacingMeters = 10
	// StretchToleranceMeters is how far a pass may sit from the stretch: GPS drift plus the other side of the road.
	StretchToleranceMeters = 30
	// A pass may cover this much less or more ground than the stretch.
	stretchShortest, stretchLongest = 0.8, 1.25
	// stretchLookahead is how many points of the stretch one record may advance past, so a pass can't jump to a later part of a stretch that doubles back.
	stretchLookahead = 8
	MinStretchMeters = 100
	MaxStretchMeters = 20_000
)

// ErrStretchRange means the stretch asked for can't be cut from the workout.
var ErrStretchRange = errors.New("stretch range")

// Stretch is a named piece of ground in one direction: points every StretchSpacingMeters, first at its start.
type Stretch struct {
	Meters float64 `json:"meters"`
	// [lat, lon] pairs.
	Points [][2]float64 `json:"points"`
}

// Pass is one time a workout covers a stretch end to end.
type Pass struct {
	StartSeconds float64
	EndSeconds   float64
	StartMeters  float64
	EndMeters    float64
	// WalkedFraction is the share of the pass walked; NaN when the workout can't tell walking from running.
	WalkedFraction float64
}

// fix is a series point with a time, a distance and a position.
type fix struct {
	t, d     float64
	lat, lon float64
	index    int
}

func fixes(s *Series) []fix {
	if s == nil || s.Latitude == nil || s.Longitude == nil {
		return nil
	}
	var out []fix
	for i := range s.TimeSeconds {
		t, d, lat, lon := s.TimeSeconds[i], s.DistanceMeters[i], s.Latitude[i], s.Longitude[i]
		if !finite(t) || !finite(d) || !finite(lat) || !finite(lon) {
			continue
		}
		// Distance should never go backwards; a fix that does would fold the interpolation.
		if len(out) > 0 && d < out[len(out)-1].d {
			continue
		}
		out = append(out, fix{t, d, lat, lon, i})
	}
	return out
}

// BuildStretch cuts the ground a workout covered between two distances along it, as its series counts them.
func BuildStretch(s *Series, startMeters, endMeters float64) (*Stretch, error) {
	length := endMeters - startMeters
	if !finite(startMeters) || !finite(endMeters) || length < MinStretchMeters || length > MaxStretchMeters {
		return nil, fmt.Errorf("%w: a stretch is between %dm and %dkm long", ErrStretchRange, MinStretchMeters, MaxStretchMeters/1000)
	}
	fx := fixes(s)
	if len(fx) < 2 {
		return nil, fmt.Errorf("%w: the workout has no GPS track", ErrStretchRange)
	}
	if startMeters < fx[0].d-StretchSpacingMeters || endMeters > fx[len(fx)-1].d+StretchSpacingMeters {
		return nil, fmt.Errorf("%w: the workout's track runs from %.0fm to %.0fm", ErrStretchRange, fx[0].d, fx[len(fx)-1].d)
	}
	n := int(math.Ceil(length/StretchSpacingMeters)) + 1
	points := make([][2]float64, n)
	j := 0
	for i := range n {
		target := startMeters + length*float64(i)/float64(n-1)
		for j+1 < len(fx)-1 && fx[j+1].d < target {
			j++
		}
		a, b := fx[j], fx[j+1]
		f := 0.0
		if b.d > a.d {
			f = math.Min(1, math.Max(0, (target-a.d)/(b.d-a.d)))
		}
		points[i] = [2]float64{round(a.lat+(b.lat-a.lat)*f, trackDecimals), round(a.lon+(b.lon-a.lon)*f, trackDecimals)}
	}
	return &Stretch{Meters: round(length, 1), Points: points}, nil
}

// MightPass is a cheap test on a workout's stored track before its series is read: the track comes near both ends of the stretch.
func (st *Stretch) MightPass(track *Track) bool {
	if track == nil || len(track.Points) < 2 || len(st.Points) < 2 {
		return false
	}
	proj := newProjection(st.Points[0][0])
	ends := proj.apply([][2]float64{st.Points[0], st.Points[len(st.Points)-1]})
	// A track point can sit half its spacing from where the run actually passed.
	reach := StretchToleranceMeters + track.Meters/float64(len(track.Points)-1)
	var nearStart, nearEnd bool
	for _, p := range proj.apply(track.Points) {
		nearStart = nearStart || dist(p, ends[0]) <= reach
		nearEnd = nearEnd || dist(p, ends[1]) <= reach
	}
	return nearStart && nearEnd
}

// closest is where the segment from a to b comes nearest p, as a fraction along it.
func closest(a, b, p [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	if l := dx*dx + dy*dy; l > 0 {
		return math.Min(1, math.Max(0, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/l))
	}
	return 0
}

// crossing refines a record near one end of the stretch to the moment the run was closest to it, between that record and a neighbour.
func crossing(fx []fix, xy [][2]float64, i int, end [2]float64) (seconds, meters float64) {
	seconds, meters = fx[i].t, fx[i].d
	best := dist(xy[i], end)
	for _, j := range []int{i - 1, i + 1} {
		if j < 0 || j >= len(fx) {
			continue
		}
		f := closest(xy[i], xy[j], end)
		at := [2]float64{xy[i][0] + (xy[j][0]-xy[i][0])*f, xy[i][1] + (xy[j][1]-xy[i][1])*f}
		if d := dist(at, end); d < best {
			best, seconds, meters = d, fx[i].t+(fx[j].t-fx[i].t)*f, fx[i].d+(fx[j].d-fx[i].d)*f
		}
	}
	return seconds, meters
}

// Passes finds every time the workout covers the stretch start to end: it comes within StretchToleranceMeters of the first point, moves forward along the stretch never further than that from it, and reaches the last.
// Following the points in order is what tells up the hill from down it.
func (st *Stretch) Passes(s *Series) []Pass {
	fx := fixes(s)
	n := len(st.Points)
	if len(fx) < 2 || n < 2 {
		return nil
	}
	proj := newProjection(st.Points[0][0])
	line := proj.apply(st.Points)
	positions := make([][2]float64, len(fx))
	for i, f := range fx {
		positions[i] = [2]float64{f.lat, f.lon}
	}
	xy := proj.apply(positions)
	var classes []motion
	if s.Walking != nil {
		classes, _ = classify(s)
	}

	var out []Pass
	for i := 0; i < len(fx); i++ {
		if dist(xy[i], line[0]) > StretchToleranceMeters {
			continue
		}
		// The entry is the closest approach to the first point while the run stays near it.
		entry := i
		for i+1 < len(fx) && dist(xy[i+1], line[0]) <= StretchToleranceMeters {
			i++
			if dist(xy[i], line[0]) < dist(xy[entry], line[0]) {
				entry = i
			}
		}
		exit := -1
		progress := 0
		for j := entry; j < len(fx); j++ {
			nearest, at := math.Inf(1), progress
			for k := progress; k < min(n, progress+stretchLookahead+1); k++ {
				if d := dist(xy[j], line[k]); d < nearest {
					nearest, at = d, k
				}
			}
			if nearest > StretchToleranceMeters {
				break
			}
			progress = at
			if progress == n-1 {
				// The exit is the closest approach to the last point.
				exit = j
				for exit+1 < len(fx) && dist(xy[exit+1], line[n-1]) < dist(xy[exit], line[n-1]) {
					exit++
				}
				break
			}
		}
		if exit <= entry {
			continue
		}
		p := Pass{WalkedFraction: math.NaN()}
		p.StartSeconds, p.StartMeters = crossing(fx, xy, entry, line[0])
		p.EndSeconds, p.EndMeters = crossing(fx, xy, exit, line[n-1])
		covered := p.EndMeters - p.StartMeters
		if p.EndSeconds <= p.StartSeconds || covered < stretchShortest*st.Meters || covered > stretchLongest*st.Meters {
			continue
		}
		if classes != nil {
			// By time, not by count: records needn't be evenly spaced, and each one speaks for the interval before it.
			var walked, known float64
			for k := fx[entry].index + 1; k <= fx[exit].index; k++ {
				dt := s.TimeSeconds[k] - s.TimeSeconds[k-1]
				if classes[k] == motionUnknown || !finite(dt) {
					continue
				}
				known += dt
				if classes[k] == motionWalking {
					walked += dt
				}
			}
			if known > 0 {
				p.WalkedFraction = walked / known
			}
		}
		out = append(out, p)
		// The next pass starts after this one ends.
		i = max(i, exit)
	}
	return out
}
