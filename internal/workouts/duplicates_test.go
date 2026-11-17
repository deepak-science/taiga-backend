package workouts_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

var t0 = time.Date(2022, 5, 1, 17, 0, 0, 0, time.UTC)

// rec spans minutes from to to after t0.
func rec(source string, from, to int) workouts.Recording {
	return workouts.Recording{Start: t0.Add(time.Duration(from) * time.Minute), End: t0.Add(time.Duration(to) * time.Minute), Source: source}
}

func TestFindDuplicatesKeepsTheRicherSource(t *testing.T) {
	got := workouts.FindDuplicates([]workouts.Recording{
		rec(workouts.GoogleFitSource, -20, 30),
		rec(workouts.EndomondoSource, 0, 40),
		rec("COROS PACE 3", 2, 38),
	})
	// Google Fit starts 20 minutes early but shares 30 of its 50 with Endomondo, and more with COROS.
	assert.Equal(t, []int{2, 2, -1}, got)
}

func TestFindDuplicatesNeedsHalfTheShorter(t *testing.T) {
	got := workouts.FindDuplicates([]workouts.Recording{
		rec("COROS PACE 3", 0, 40),
		// A walk home that started in the last 10 minutes of the run.
		rec(workouts.GoogleFitSource, 30, 60),
		// A cooldown walk Google Fit found inside the run.
		rec(workouts.GoogleFitSource, 32, 38),
		// Touching isn't overlapping.
		rec(workouts.EndomondoSource, -20, 0),
	})
	assert.Equal(t, []int{-1, -1, 0, -1}, got)
}

func TestFindDuplicatesLeavesOneSourceAlone(t *testing.T) {
	got := workouts.FindDuplicates([]workouts.Recording{
		rec(workouts.EndomondoSource, 0, 20),
		rec(workouts.EndomondoSource, 17, 20),
		rec("COROS PACE 3", 0, 0),
	})
	assert.Equal(t, []int{-1, -1, -1}, got)
}

func TestFindDuplicatesNeverChains(t *testing.T) {
	// One long phone session over two separate watch runs: both runs stay, and the session copies the one it covers most.
	got := workouts.FindDuplicates([]workouts.Recording{
		rec("COROS PACE 3", 0, 30),
		rec(workouts.GoogleFitSource, 10, 90),
		rec("COROS PACE 3", 50, 100),
	})
	assert.Equal(t, []int{-1, 2, -1}, got)
}

func TestFindDuplicatesTiesGoToTheEarlier(t *testing.T) {
	got := workouts.FindDuplicates([]workouts.Recording{
		rec("Garmin fr965", 0, 30),
		rec("COROS PACE 3", 0, 30),
	})
	assert.Equal(t, []int{-1, 0}, got)
}
