package workouts

import (
	"math"
	"slices"
)

// Route matching tolerances.
const (
	// RouteEndpointMeters is how far apart two runs' starts (and ends) may be.
	RouteEndpointMeters = 150
	// RouteLengthRatio is how much longer one run may be than the other, as a fraction of the longer.
	RouteLengthRatio = 0.10
	// RouteShapeMeters bounds the discrete Fréchet distance between two tracks.
	RouteShapeMeters = 75
)

const (
	// TrackSpacingMeters is the resampling step; discrete Fréchet overstates the true distance by up to half of it.
	// Stored tracks use it, so changing it needs an extractorVersion bump.
	TrackSpacingMeters = 20
	// MaxTrackPoints caps a track, widening the spacing past 40km.
	MaxTrackPoints = 2000
	// minTrackMeters is the shortest GPS track worth matching; anything shorter is a glitch or a watch started by accident.
	minTrackMeters = 200
	// medoidPoints is the coarser resampling used to choose a route's representative run.
	medoidPoints  = 100
	earthRadiusM  = 6_371_000
	trackDecimals = 6
)

// Track is a run's GPS path resampled evenly by distance, the input to route matching.
type Track struct {
	Meters float64 `json:"meters"`
	// [lat, lon] pairs, first at the start and last at the end.
	Points [][2]float64 `json:"points"`
}

// BuildTrack resamples the run's positions every TrackSpacingMeters along its recorded distance.
// It returns nil for a run without enough GPS to match, such as a treadmill run.
func BuildTrack(run *Run) *Track {
	type fix struct{ d, lat, lon float64 }
	var fixes []fix
	for _, r := range run.Records {
		if r.Distance == nil || !finite(r.Distance.Meters) || !finite(r.Latitude) || !finite(r.Longitude) {
			continue
		}
		// Distance should never go backwards; a fix that does would fold the interpolation.
		if len(fixes) > 0 && r.Distance.Meters < fixes[len(fixes)-1].d {
			continue
		}
		fixes = append(fixes, fix{r.Distance.Meters, r.Latitude, r.Longitude})
	}
	if len(fixes) < 2 {
		return nil
	}
	first, last := fixes[0].d, fixes[len(fixes)-1].d
	total := last - first
	if total < minTrackMeters {
		return nil
	}

	n := min(int(math.Ceil(total/TrackSpacingMeters))+1, MaxTrackPoints)
	points := make([][2]float64, n)
	j := 0
	for i := range n {
		target := first + total*float64(i)/float64(n-1)
		for j+1 < len(fixes)-1 && fixes[j+1].d < target {
			j++
		}
		a, b := fixes[j], fixes[j+1]
		f := 0.0
		if b.d > a.d {
			f = math.Min(1, math.Max(0, (target-a.d)/(b.d-a.d)))
		}
		points[i] = [2]float64{
			round(a.lat+(b.lat-a.lat)*f, trackDecimals),
			round(a.lon+(b.lon-a.lon)*f, trackDecimals),
		}
	}
	return &Track{Meters: total, Points: points}
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// projection maps degrees to local meters around a reference latitude; equirectangular is accurate to well under a metre across a run.
type projection struct{ cosLat float64 }

func newProjection(lat float64) projection { return projection{math.Cos(lat * math.Pi / 180)} }

func (p projection) apply(points [][2]float64) [][2]float64 {
	out := make([][2]float64, len(points))
	for i, pt := range points {
		out[i] = [2]float64{
			pt[1] * math.Pi / 180 * earthRadiusM * p.cosLat,
			pt[0] * math.Pi / 180 * earthRadiusM,
		}
	}
	return out
}

func dist(a, b [2]float64) float64 { return math.Hypot(a[0]-b[0], a[1]-b[1]) }

// SameRoute reports whether two tracks are the same route in the same direction, with the Fréchet distance between them when they are.
func SameRoute(a, b *Track) (float64, bool) {
	if a == nil || b == nil || len(a.Points) < 2 || len(b.Points) < 2 {
		return 0, false
	}
	if math.Abs(a.Meters-b.Meters) > RouteLengthRatio*math.Max(a.Meters, b.Meters) {
		return 0, false
	}
	proj := newProjection(a.Points[0][0])
	pa, pb := proj.apply(a.Points), proj.apply(b.Points)
	if dist(pa[0], pb[0]) > RouteEndpointMeters || dist(pa[len(pa)-1], pb[len(pb)-1]) > RouteEndpointMeters {
		return 0, false
	}
	// The endpoint check already lets the starts and ends sit RouteEndpointMeters apart, so Fréchet may skip that far into either track at each end.
	// Otherwise a loop started a little further round, or a watch started a few steps from the door, counts the offset as shape.
	d := frechet(pa, pb, endpointWindow(a), endpointWindow(b), RouteShapeMeters)
	return d, d <= RouteShapeMeters
}

// endpointWindow is how many points from either end of the track lie within RouteEndpointMeters, kept short enough that the start and end windows never overlap.
func endpointWindow(t *Track) int {
	spacing := t.Meters / float64(len(t.Points)-1)
	return min(int(RouteEndpointMeters/spacing), (len(t.Points)-2)/2)
}

// frechet is the discrete Fréchet distance between two point sequences, or +Inf once it must exceed limit.
// The coupling may start anywhere in the first ka+1 points of a or kb+1 points of b, and end anywhere in the last ones, so up to that many points at either end go unmatched.
// Following the points in order is what tells a loop run clockwise from the same loop run anticlockwise.
func frechet(a, b [][2]float64, ka, kb int, limit float64) float64 {
	prev := make([]float64, len(b))
	cur := make([]float64, len(b))
	best := math.Inf(1)
	for i := range a {
		rowMin := math.Inf(1)
		for j := range b {
			d := dist(a[i], b[j])
			var reach float64
			switch {
			case (i == 0 && j <= kb) || (j == 0 && i <= ka):
				reach = d
			case i == 0:
				reach = math.Max(cur[j-1], d)
			case j == 0:
				reach = math.Max(prev[0], d)
			default:
				reach = math.Max(math.Min(prev[j], math.Min(prev[j-1], cur[j-1])), d)
			}
			cur[j] = reach
			rowMin = math.Min(rowMin, reach)
			if (j == len(b)-1 && i >= len(a)-1-ka) || (i == len(a)-1 && j >= len(b)-1-kb) {
				best = math.Min(best, reach)
			}
		}
		// Past the last place a coupling can start, every unfinished coupling passes through this row, so none can come in under the limit.
		if i >= ka && rowMin > limit && best > limit {
			return math.Inf(1)
		}
		prev, cur = cur, prev
	}
	return best
}

// RouteGroup is a set of runs on the same route, as indices into the tracks passed to GroupRoutes.
type RouteGroup struct {
	Members []int
	// Representative is the member closest to all the others, whose track stands for the route.
	Representative int
}

// GroupRoutes clusters tracks, given oldest first, into routes of at least two runs; a run that matches nothing is left out.
func GroupRoutes(tracks []*Track) []RouteGroup {
	var out []RouteGroup
	for _, g := range ClusterRoutes(tracks) {
		if len(g.Members) >= 2 {
			out = append(out, g)
		}
	}
	return out
}

// ClusterRoutes is GroupRoutes with the runs that match nothing kept, each a group of its own.
// Each run first joins the group whose first run it matches most closely.
// That first run may be an off day, splitting one route in two, so groups whose representatives match are then merged until none do.
// The result depends only on the tracks and their order, so rebuilding from scratch gives the same routes.
func ClusterRoutes(tracks []*Track) []RouteGroup {
	var groups []RouteGroup
	for i, t := range tracks {
		if t == nil {
			continue
		}
		best, bestD := -1, math.Inf(1)
		for g := range groups {
			if d, ok := SameRoute(tracks[groups[g].Members[0]], t); ok && d < bestD {
				best, bestD = g, d
			}
		}
		if best < 0 {
			groups = append(groups, RouteGroup{Members: []int{i}})
			continue
		}
		groups[best].Members = append(groups[best].Members, i)
	}
	for g := range groups {
		groups[g].Representative = Medoid(tracks, groups[g].Members)
	}

	for merged := true; merged; {
		merged = false
		for a := 0; a < len(groups) && !merged; a++ {
			for b := a + 1; b < len(groups); b++ {
				if _, ok := SameRoute(tracks[groups[a].Representative], tracks[groups[b].Representative]); !ok {
					continue
				}
				members := append(groups[a].Members, groups[b].Members...)
				slices.Sort(members)
				groups[a] = RouteGroup{Members: members, Representative: Medoid(tracks, members)}
				groups = slices.Delete(groups, b, b+1)
				merged = true
				break
			}
		}
	}

	return groups
}

// Medoid picks the member with the least total distance to the others, comparing coarse resamplings point by point.
// Members already match within tolerance, so the cheap comparison is enough to rank them and keeps a large route from costing a Fréchet per pair.
func Medoid(tracks []*Track, members []int) int {
	proj := newProjection(tracks[members[0]].Points[0][0])
	coarse := make([][][2]float64, len(members))
	for k, m := range members {
		coarse[k] = proj.apply(resample(tracks[m].Points, medoidPoints))
	}
	best, bestSum := members[0], math.Inf(1)
	for k := range members {
		var sum float64
		for l := range members {
			for p := range medoidPoints {
				sum += dist(coarse[k][p], coarse[l][p])
			}
		}
		if sum < bestSum {
			best, bestSum = members[k], sum
		}
	}
	return best
}

// resample picks n points spread evenly by index; tracks are already evenly spaced, so that's evenly by distance.
func resample(points [][2]float64, n int) [][2]float64 {
	out := make([][2]float64, n)
	for i := range n {
		out[i] = points[int(math.Round(float64(i)*float64(len(points)-1)/float64(n-1)))]
	}
	return out
}

// Placing a jittery run on a route: its drift lengthens it and pushes it tens of metres off, so the checks are looser than SameRoute's, and only ever applied to runs already known to be jittery.
const (
	// JitterRouteShapeMeters bounds the Fréchet distance between a jittery run and the route's representative.
	JitterRouteShapeMeters = 150
	// JitterRouteShortest and JitterRouteLongest bound the route's length as a fraction of the jittery track's, since drift only ever adds length.
	JitterRouteShortest = 0.70
	JitterRouteLongest  = 0.95
)

// JitterOnRoute reports whether a jittery track follows the route a representative track stands for, with the Fréchet distance between them when it does.
func JitterOnRoute(jittery, representative *Track) (float64, bool) {
	if jittery == nil || representative == nil || len(jittery.Points) < 2 || len(representative.Points) < 2 {
		return 0, false
	}
	if ratio := representative.Meters / jittery.Meters; ratio < JitterRouteShortest || ratio > JitterRouteLongest {
		return 0, false
	}
	proj := newProjection(representative.Points[0][0])
	pj, pr := proj.apply(jittery.Points), proj.apply(representative.Points)
	if dist(pj[0], pr[0]) > RouteEndpointMeters || dist(pj[len(pj)-1], pr[len(pr)-1]) > RouteEndpointMeters {
		return 0, false
	}
	d := frechet(pj, pr, endpointWindow(jittery), endpointWindow(representative), JitterRouteShapeMeters)
	return d, d <= JitterRouteShapeMeters
}
