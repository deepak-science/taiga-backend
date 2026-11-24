package models

import (
	"cmp"
	"context"
	"errors"
	"log"
	"slices"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

// How a periodic total buckets workouts: weeks start on Monday, all at local midnight.
const (
	EveryWeek  = "week"
	EveryMonth = "month"
	EveryYear  = "year"
)

// Periods are what PeriodicTotals takes for every.
var Periods = []string{EveryWeek, EveryMonth, EveryYear}

// MaxPeriods caps a periodic total, so a window of centuries can't ask for tens of thousands of empty weeks.
const MaxPeriods = 1000

var ErrTooManyPeriods = errors.New("the window spans too many periods")

// PeriodTotals sums each sport's counted workouts starting in one week, month or year.
type PeriodTotals struct {
	Start  time.Time      `json:"start"`
	Sports []*SportTotals `json:"sports"`
}

// periodStart is the local midnight starting the week, month or year t falls in.
func periodStart(t time.Time, every string, loc *time.Location) time.Time {
	t = t.In(loc)
	switch every {
	case EveryMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
	case EveryYear:
		return time.Date(t.Year(), time.January, 1, 0, 0, 0, 0, loc)
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	return day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
}

func nextPeriod(start time.Time, every string) time.Time {
	switch every {
	case EveryMonth:
		return start.AddDate(0, 1, 0)
	case EveryYear:
		return start.AddDate(1, 0, 0)
	}
	return start.AddDate(0, 0, 7)
}

// PeriodicTotals lists every week, month or year from the window's start to its end, empty ones included; an open end stops at the first or last workout.
// A period straddling an edge of the window only sums the workouts inside it.
func (m *storeModel) PeriodicTotals(ctx context.Context, every string, window Window, loc *time.Location, userToken *tokens.UserToken) ([]*PeriodTotals, error) {
	querier, err := m.store.GetQuerier()
	if err != nil {
		log.Printf("could not get a querier: %v", err)
		return nil, err
	}
	rows, err := listCounted(ctx, querier, window, userToken)
	if err != nil {
		log.Printf("Could not list counted workouts: %v", err)
		return nil, err
	}
	return bucketTotals(rows, every, window, loc)
}

func bucketTotals(rows []*db.ListCountedWorkoutsInWindowRow, every string, window Window, loc *time.Location) ([]*PeriodTotals, error) {
	first, last := window.From, window.To
	if !last.IsZero() {
		// To is exclusive, so a window ending on a period's first midnight doesn't include that period.
		last = last.Add(-time.Nanosecond)
	}
	if len(rows) > 0 {
		if first.IsZero() {
			first = rows[0].StartTime.Time
		}
		if last.IsZero() {
			last = rows[len(rows)-1].StartTime.Time
		}
	}
	if first.IsZero() || last.IsZero() {
		return []*PeriodTotals{}, nil
	}

	var periods []*PeriodTotals
	index := map[time.Time]int{}
	end := periodStart(last, every, loc)
	for start := periodStart(first, every, loc); !start.After(end); start = nextPeriod(start, every) {
		if len(periods) == MaxPeriods {
			return nil, ErrTooManyPeriods
		}
		index[start] = len(periods)
		periods = append(periods, &PeriodTotals{Start: start})
	}
	byPeriod := make([][]*db.ListCountedWorkoutsInWindowRow, len(periods))
	for _, r := range rows {
		i := index[periodStart(r.StartTime.Time, every, loc)]
		byPeriod[i] = append(byPeriod[i], r)
	}
	for i, p := range periods {
		p.Sports = sumBySport(byPeriod[i])
	}
	return periods, nil
}

// YearBests is the best effort at each standard segment in one local calendar year.
type YearBests struct {
	Year    int                       `json:"year"`
	Efforts map[string]*SegmentEffort `json:"efforts"`
}

// YearlyBests ranks each year's efforts of one sport apart, newest year first, leaving out years with none.
func (m *storeModel) YearlyBests(ctx context.Context, sport string, loc *time.Location, userToken *tokens.UserToken) ([]*YearBests, error) {
	querier, err := m.store.GetQuerier()
	if err != nil {
		log.Printf("could not get a querier: %v", err)
		return nil, err
	}
	rows, err := querier.ListSegmentEffortsBySport(ctx, &db.ListSegmentEffortsBySportParams{UserID: userToken.ID, Sport: sport})
	if err != nil {
		log.Printf("Could not list %s segments: %v", sport, err)
		return nil, err
	}
	byYear := map[int]*YearBests{}
	for _, r := range rows {
		effort := SegmentEffort(*r)
		year := effort.WorkoutStartTime.Time.In(loc).Year()
		y := byYear[year]
		if y == nil {
			y = &YearBests{Year: year, Efforts: map[string]*SegmentEffort{}}
			byYear[year] = y
		}
		if best := y.Efforts[effort.Name]; best == nil || better(&effort, best) {
			y.Efforts[effort.Name] = &effort
		}
	}
	years := make([]*YearBests, 0, len(byYear))
	for _, y := range byYear {
		years = append(years, y)
	}
	slices.SortFunc(years, func(a, b *YearBests) int { return cmp.Compare(b.Year, a.Year) })
	return years, nil
}

// better ranks as ListBestSegmentsByName does: faster over a distance, farther over a duration.
func better(a, b *SegmentEffort) bool {
	return outranks(a.Kind, a.DistanceMeters, a.DurationSeconds, b.DistanceMeters, b.DurationSeconds)
}

// outranks is whether effort a beats effort b at a segment of kind: faster over a distance, farther over a duration.
func outranks(kind string, aMeters, aSeconds, bMeters, bSeconds float64) bool {
	if kind == workouts.KindDuration {
		return aMeters > bMeters
	}
	return aSeconds < bSeconds
}
