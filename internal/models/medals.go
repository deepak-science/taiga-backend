package models

import (
	"context"
	"log"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

// Medals, best first: an effort gets the highest it holds.
const (
	MedalGold   = "gold"   // the best ever at its segment and sport
	MedalSilver = "silver" // the best of its local calendar year
	MedalBronze = "bronze" // the best of its local calendar month
)

// SegmentMedal is a medal one of a workout's segments holds now.
type SegmentMedal struct {
	Segment string `json:"segment"`
	Medal   string `json:"medal"`
}

// Medals is the medals each of the user's workouts holds now, keyed by workout id, in its segments' order; a workout with none is absent.
// They're worked out on every call, so a faster run, a correction or a deletion moves them at once.
func (m *storeModel) Medals(ctx context.Context, loc *time.Location, userToken *tokens.UserToken) (map[int32][]*SegmentMedal, error) {
	querier, err := m.store.GetQuerier()
	if err != nil {
		log.Printf("could not get a querier: %v", err)
		return nil, err
	}
	rows, err := querier.ListSegmentEffortsForMedals(ctx, userToken.ID)
	if err != nil {
		log.Printf("Could not list segments for medals: %v", err)
		return nil, err
	}
	return awardMedals(rows, loc), nil
}

// awardMedals takes rows oldest first, so on a tie the earlier effort keeps the medal.
func awardMedals(rows []*db.ListSegmentEffortsForMedalsRow, loc *time.Location) map[int32][]*SegmentMedal {
	// A zero year is all time, a zero month the whole year.
	type period struct {
		sport, name string
		year        int
		month       time.Month
	}
	periods := func(r *db.ListSegmentEffortsForMedalsRow) [3]period {
		local := r.WorkoutStartTime.Time.In(loc)
		return [3]period{
			{r.Sport, r.Name, 0, 0},
			{r.Sport, r.Name, local.Year(), 0},
			{r.Sport, r.Name, local.Year(), local.Month()},
		}
	}
	best := map[period]*db.ListSegmentEffortsForMedalsRow{}
	for _, r := range rows {
		for _, p := range periods(r) {
			if b := best[p]; b == nil || outranks(r.Kind, r.DistanceMeters, r.DurationSeconds, b.DistanceMeters, b.DurationSeconds) {
				best[p] = r
			}
		}
	}
	medals := map[int32][]*SegmentMedal{}
	for _, r := range rows {
		for i, p := range periods(r) {
			if best[p] == r {
				medal := []string{MedalGold, MedalSilver, MedalBronze}[i]
				medals[r.WorkoutID] = append(medals[r.WorkoutID], &SegmentMedal{Segment: r.Name, Medal: medal})
				break
			}
		}
	}
	return medals
}
