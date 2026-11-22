package routes_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/muktihari/fit/profile/typedef"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/testsupport"
)

// old is a run from before the connection: the file as the user would have uploaded it, and COROS's activity for it, starting at the same second.
func (a *sourcesAPI) old(n int, ago time.Duration) (file []byte, activity testsupport.FakeActivity) {
	start := a.now.Add(-ago).Truncate(time.Second)
	file = testsupport.EncodeFITDevice(a.t, start, []testsupport.Split{{Meters: 1000, Seconds: 300}}, typedef.ManufacturerCoros, 0, "COROS PACE 3")
	return file, testsupport.FakeActivity{ID: fmt.Sprintf("47000000000000%04d", n), SportType: 100, Start: start, End: start.Add(5 * time.Minute), File: file}
}

func (a *sourcesAPI) lists() int {
	_, lists, _, _, _ := a.fake.Counts()
	return lists
}

func TestRunsFromBeforeTheConnectionGetCOROSFigures(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	const day = 24 * time.Hour
	lastYear, lastYearActivity := a.old(1, 400*day)
	twoYears, twoYearsActivity := a.old(2, 800*day)
	// One COROS no longer has, and one another watch recorded at the very second of a COROS activity.
	gone, _ := a.old(3, 600*day)
	_, elsewhere := a.old(4, 500*day)
	garmin := testsupport.EncodeFITDevice(t, elsewhere.Start, []testsupport.Split{{Meters: 1000, Seconds: 300}}, typedef.ManufacturerGarmin, 0, "")
	a.fake.Add(lastYearActivity, twoYearsActivity, elsewhere)

	_, lastYearID := alice.uploadWorkout(lastYear)
	_, twoYearsID := alice.uploadWorkout(twoYears)
	_, goneID := alice.uploadWorkout(gone)
	_, garminID := alice.uploadWorkout(garmin)

	a.connect(alice)
	a.sync()

	for _, id := range []int32{lastYearID, twoYearsID} {
		status, figures := alice.corosFigures(id)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, 120.0, figures["training_load"])
	}
	for name, id := range map[string]int32{"the one COROS doesn't list": goneID, "another watch's": garminID} {
		status, _ := alice.corosFigures(id)
		assert.Equal(t, http.StatusNotFound, status, name)
	}
	assert.Zero(t, a.fileRequests(), "the files were already here")
	assert.Len(t, a.workouts(alice), 4)
	conn := a.sources(alice).Connections[0]
	assert.Empty(t, conn.LastError)
	assert.Equal(t, int64(2), conn.Activities)

	// Nothing is left to look for, so the next sync lists the new and no history.
	lists := a.lists()
	a.syncAgain(alice)
	assert.Equal(t, lists+1, a.lists())
}

func TestARunUploadedAfterTheWalkBackIsStillFound(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	const day = 24 * time.Hour
	first, firstActivity := a.old(1, 300*day)
	// Between two that were already looked for.
	later, laterActivity := a.old(2, 200*day)
	a.fake.Add(firstActivity, laterActivity)
	alice.uploadWorkout(first)
	recent, _ := a.old(3, 100*day)
	alice.uploadWorkout(recent)
	a.connect(alice)
	a.sync()

	_, laterID := alice.uploadWorkout(later)
	status, _ := alice.corosFigures(laterID)
	require.Equal(t, http.StatusNotFound, status)
	a.syncAgain(alice)
	status, _ = alice.corosFigures(laterID)
	assert.Equal(t, http.StatusOK, status)
}

func TestTheWalkBackGoesAFewMonthsASync(t *testing.T) {
	a := newSourcesAPI(t)
	alice := a.user("alice@example.com")
	// Each in a month of its own, and more of them than one sync lists.
	const runs = 15
	ids := make([]int32, runs)
	for n := range runs {
		file, activity := a.old(n, time.Duration(n+1)*40*24*time.Hour)
		a.fake.Add(activity)
		_, ids[n] = alice.uploadWorkout(file)
	}
	have := func() (n int, oldest bool) {
		for i, id := range ids {
			if status, _ := alice.corosFigures(id); status == http.StatusOK {
				n++
				oldest = oldest || i == runs-1
			}
		}
		return n, oldest
	}

	a.connect(alice)
	a.sync()
	n, oldest := have()
	assert.Equal(t, 12, n)
	assert.False(t, oldest, "newest first")

	a.syncAgain(alice)
	n, _ = have()
	assert.Equal(t, runs, n)
}
