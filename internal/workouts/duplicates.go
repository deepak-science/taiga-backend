package workouts

import (
	"cmp"
	"slices"
	"time"
)

// Recording is what duplicate detection needs of a workout from a file.
type Recording struct {
	Start, End time.Time
	Source     string
}

// DuplicateOverlap is how much of the shorter recording two from different sources must share to be one workout.
// On a Google Fit export against Endomondo and COROS, copies share 80–100% and the step-detected walks either side of a run mostly under 40%.
const DuplicateOverlap = 0.5

// GoogleFitSource is the author Google Fit's Takeout writes into every TCX file.
const GoogleFitSource = "Google Fit"

// sourceRank orders sources by how much they record, richest first: a watch's FIT file, then Endomondo, then Google Fit's phone tracks.
func sourceRank(source string) int {
	switch source {
	case GoogleFitSource:
		return 2
	case EndomondoSource:
		return 1
	default:
		return 0
	}
}

// overlap is the share of the shorter recording's span that the two have in common; 0 when either has no span.
func overlap(a, b Recording) float64 {
	shorter := min(a.End.Sub(a.Start), b.End.Sub(b.Start))
	start, end := a.Start, a.End
	if b.Start.After(start) {
		start = b.Start
	}
	if b.End.Before(end) {
		end = b.End
	}
	common := end.Sub(start)
	if shorter <= 0 || common <= 0 {
		return 0
	}
	return float64(common) / float64(shorter)
}

// FindDuplicates returns, for each recording, the index of the one it's a copy of, or -1 if it's kept.
// Recordings are kept richest source first, ties going to the earlier index; one overlapping a kept recording from another source by DuplicateOverlap is a copy of whichever it overlaps most.
// A copy always points at a kept recording, so a long phone session over two watch runs doesn't merge the runs.
// Recordings from the same source are never copies of each other: one recorder can't record a workout twice.
func FindDuplicates(recs []Recording) []int {
	order := make([]int, len(recs))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return cmp.Compare(sourceRank(recs[a].Source), sourceRank(recs[b].Source))
	})
	of := make([]int, len(recs))
	var kept []int
	for _, i := range order {
		of[i] = -1
		best := 0.0
		for _, k := range kept {
			if recs[k].Source == recs[i].Source {
				continue
			}
			if o := overlap(recs[i], recs[k]); o >= DuplicateOverlap && o > best {
				of[i], best = k, o
			}
		}
		if of[i] < 0 {
			kept = append(kept, i)
		}
	}
	return of
}
