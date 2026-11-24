package workouts

import "math"

// Telling walking from running within a run. Tuned on two years of COROS runs (docs/roadmap-archive.md, M13).
const (
	// walkCadenceBelow is the dip between the walking mode (100–120 steps a minute) and the running one (130–150).
	walkCadenceBelow = 125
	// doubledCadence and over is a watch counting each step twice; such readings fall back to speed.
	doubledCadence = 190
	// walkSpeedBelow splits the two speed modes where cadence is missing from a record that should have it.
	walkSpeedBelow = 2.0
	// stoppedSpeedBelow is standing at a light, which is neither.
	stoppedSpeedBelow   = 0.5
	motionWindowSeconds = 10
	// A walking break lasts at least minWalkBreakSeconds; breaks closer than walkBreakGapSeconds are one.
	minWalkBreakSeconds = 10
	walkBreakGapSeconds = 5
	// steadyRunSeconds of unbroken running is the run proper: what's before the first is warm-up, after the last cool-down.
	steadyRunSeconds = 30
	// Without cadence a run's own speeds must show two modes: the split has to explain twoModeShare of their variance (one hump cut in half scores 0.64),
	// with class means at least modeGapMps apart and walkShare of the moving records in the lower one.
	twoModeShare = 0.7
	modeGapMps   = 0.7
	walkShare    = 0.03
)

// How Walking was told apart.
const (
	WalkingByCadence = "cadence"
	WalkingBySpeed   = "speed"
)

type WalkBreak struct {
	StartSeconds float64 `json:"start_s"`
	EndSeconds   float64 `json:"end_s"`
}

// Walking is where a run was walked.
type Walking struct {
	Method string `json:"method"`
	// Before the first steady running and after the last.
	WarmupSeconds   float64 `json:"warmup_s"`
	CooldownSeconds float64 `json:"cooldown_s"`
	// WalkSeconds is the walking between them, the sum of Breaks.
	WalkSeconds float64     `json:"walk_s"`
	Breaks      []WalkBreak `json:"breaks"`
}

type motion int8

const (
	motionUnknown motion = iota
	motionStopped
	motionWalking
	motionRunning
)

// windowSpeeds is the speed over motionWindowSeconds centred on each point, from distance; NaN where time or distance is missing.
func windowSpeeds(s *Series) []float64 {
	n := len(s.TimeSeconds)
	out := make([]float64, n)
	var valid []int
	for i := range n {
		out[i] = math.NaN()
		if i < len(s.DistanceMeters) && finite(s.TimeSeconds[i]) && finite(s.DistanceMeters[i]) {
			valid = append(valid, i)
		}
	}
	a, b := 0, 0
	for _, i := range valid {
		t := s.TimeSeconds[i]
		for s.TimeSeconds[valid[a]] < t-motionWindowSeconds/2 {
			a++
		}
		for b+1 < len(valid) && s.TimeSeconds[valid[b+1]] <= t+motionWindowSeconds/2 {
			b++
		}
		// A sparse series may have no second point inside the window; reach to the neighbours.
		lo, hi := a, b
		if lo == hi {
			lo, hi = max(lo-1, 0), min(hi+1, len(valid)-1)
		}
		if dt := s.TimeSeconds[valid[hi]] - s.TimeSeconds[valid[lo]]; dt > 0 {
			out[i] = (s.DistanceMeters[valid[hi]] - s.DistanceMeters[valid[lo]]) / dt
		}
	}
	return out
}

// speedSplit cuts the moving speeds in two where the classes are most distinct (Otsu), and reports whether they are two modes at all.
// It always finds a cut, so a run with no walking would have half its running called walking without the check.
func speedSplit(speeds []float64) (threshold float64, twoModes bool) {
	const lowest, highest, bin = stoppedSpeedBelow, 4.5, 0.05
	bins := int((highest - lowest) / bin)
	hist := make([]float64, bins)
	var total, sum, squares float64
	for _, v := range speeds {
		if v >= lowest && v < highest {
			hist[min(int((v-lowest)/bin), bins-1)]++
			total++
			sum += v
			squares += v * v
		}
	}
	if total < 50 {
		return 0, false
	}
	mean := sum / total
	variance := squares/total - mean*mean
	if variance <= 0 {
		return 0, false
	}
	var below, belowSum, best, gap, share float64
	for k := range bins - 1 {
		below += hist[k]
		belowSum += hist[k] * (lowest + (float64(k)+0.5)*bin)
		if below == 0 || below == total {
			continue
		}
		lo, hi := belowSum/below, (sum-belowSum)/(total-below)
		if between := below / total * (1 - below/total) * (hi - lo) * (hi - lo); between > best {
			best, threshold, gap, share = between, lowest+float64(k+1)*bin, hi-lo, below/total
		}
	}
	return threshold, best/variance >= twoModeShare && gap >= modeGapMps && share >= walkShare
}

// classify marks each point stopped, walking or running, by cadence where most records have it and else by the run's own two speed modes.
// It returns nil when neither can tell.
func classify(s *Series) ([]motion, string) {
	n := len(s.TimeSeconds)
	speeds := windowSpeeds(s)
	withCadence := 0
	for _, c := range s.Cadence {
		if finite(c) {
			withCadence++
		}
	}
	method, threshold := WalkingByCadence, float64(walkSpeedBelow)
	if n == 0 || withCadence*2 < n {
		split, ok := speedSplit(speeds)
		if !ok {
			return nil, ""
		}
		method, threshold = WalkingBySpeed, split
	}
	out := make([]motion, n)
	for i, v := range speeds {
		cadence := math.NaN()
		if method == WalkingByCadence {
			cadence = s.Cadence[i]
		}
		switch {
		case !finite(v):
			out[i] = motionUnknown
		case v < stoppedSpeedBelow:
			out[i] = motionStopped
		case finite(cadence) && cadence < doubledCadence:
			out[i] = motionRunning
			if cadence < walkCadenceBelow {
				out[i] = motionWalking
			}
		case v < threshold:
			out[i] = motionWalking
		default:
			out[i] = motionRunning
		}
	}
	return out, method
}

// spells are the maximal index ranges [a, b] of one kind of motion.
func spells(classes []motion, kind motion) [][2]int {
	var out [][2]int
	for i := 0; i < len(classes); {
		if classes[i] != kind {
			i++
			continue
		}
		j := i
		for j+1 < len(classes) && classes[j+1] == kind {
			j++
		}
		out = append(out, [2]int{i, j})
		i = j + 1
	}
	return out
}

// DetectWalking finds the run's warm-up, cool-down and the walking breaks between; nil when walking can't be told from running, or there's no steady running at all.
func DetectWalking(s *Series) *Walking {
	classes, method := classify(s)
	if classes == nil {
		return nil
	}
	t := s.TimeSeconds
	first, last := -1, -1
	for _, r := range spells(classes, motionRunning) {
		if t[r[1]]-t[r[0]] >= steadyRunSeconds {
			if first < 0 {
				first = r[0]
			}
			last = r[1]
		}
	}
	if first < 0 {
		return nil
	}
	w := &Walking{
		Method:          method,
		WarmupSeconds:   round(t[first]-t[0], 1),
		CooldownSeconds: round(t[len(t)-1]-t[last], 1),
		Breaks:          []WalkBreak{},
	}
	var merged [][2]int
	for _, r := range spells(classes[first:last+1], motionWalking) {
		r = [2]int{r[0] + first, r[1] + first}
		if n := len(merged); n > 0 && t[r[0]]-t[merged[n-1][1]] <= walkBreakGapSeconds {
			merged[n-1][1] = r[1]
			continue
		}
		merged = append(merged, r)
	}
	for _, r := range merged {
		if d := t[r[1]] - t[r[0]]; d >= minWalkBreakSeconds {
			w.Breaks = append(w.Breaks, WalkBreak{StartSeconds: t[r[0]], EndSeconds: t[r[1]]})
			w.WalkSeconds += d
		}
	}
	w.WalkSeconds = round(w.WalkSeconds, 1)
	return w
}
