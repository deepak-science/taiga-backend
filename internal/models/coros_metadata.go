package models

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/coros"
	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

// sourceDetailsPerSync caps how many activities one sync asks COROS's figures for; whether COROS limits those calls isn't known, and the rest wait an hour.
const sourceDetailsPerSync = 20

// ErrNoCOROSMetadata means COROS has given no figures for the workout: it didn't come from COROS, or they're still to be fetched.
var ErrNoCOROSMetadata = errors.New("workout has no figures from COROS")

// COROSMetadata is COROS's own figures for a workout, kept for tracking: none of it feeds best efforts, splits or totals.
// A figure is null where COROS gave none, which goes by the sport.
type COROSMetadata struct {
	// ExternalID is COROS's id for the activity.
	ExternalID     string    `json:"external_id"`
	FetchedAt      time.Time `json:"fetched_at"`
	WorkoutSeconds *float64  `json:"workout_seconds"`
	TotalSeconds   *float64  `json:"total_seconds"`
	DistanceMeters *float64  `json:"distance_meters"`
	// Paces are seconds a kilometre; adjusted is the pace on the flat COROS reckons the effort was worth.
	AvgPaceSPerKm       *float64 `json:"avg_pace_s_per_km"`
	MovingAvgPaceSPerKm *float64 `json:"moving_avg_pace_s_per_km"`
	AdjustedPaceSPerKm  *float64 `json:"adjusted_pace_s_per_km"`
	BestKmSeconds       *float64 `json:"best_km_seconds"`
	AvgHeartRate        *float64 `json:"avg_heart_rate"`
	AvgCadence          *float64 `json:"avg_cadence"`
	AvgStrideLengthM    *float64 `json:"avg_stride_length_m"`
	AvgPowerWatts       *float64 `json:"avg_power_watts"`
	ElevationGainM      *float64 `json:"elevation_gain_m"`
	ElevationLossM      *float64 `json:"elevation_loss_m"`
	Calories            *float64 `json:"calories"`
	TrainingLoad        *float64 `json:"training_load"`
	AerobicTE           *float64 `json:"aerobic_te"`
	AnaerobicTE         *float64 `json:"anaerobic_te"`
	// TrainingFocus and Performance are COROS's words, e.g. "Threshold" and "Best"; empty when it gave none.
	TrainingFocus string `json:"training_focus"`
	Performance   string `json:"performance"`
}

// WorkoutCOROSMetadata returns COROS's figures for the workout; ErrNoCOROSMetadata when it has none.
func (m *storeModel) WorkoutCOROSMetadata(ctx context.Context, workoutID int32, userToken *tokens.UserToken) (*COROSMetadata, error) {
	if _, err := m.Workout(ctx, workoutID, userToken); err != nil {
		return nil, err
	}
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	row, err := q.GetCOROSMetadata(ctx, &db.GetCOROSMetadataParams{WorkoutID: workoutID, UserID: userToken.ID})
	if IsNotFound(err) {
		return nil, ErrNoCOROSMetadata
	}
	if err != nil {
		return nil, err
	}
	return &COROSMetadata{
		ExternalID:          row.ExternalID,
		FetchedAt:           row.FetchedAt.Time,
		WorkoutSeconds:      row.WorkoutSeconds,
		TotalSeconds:        row.TotalSeconds,
		DistanceMeters:      row.DistanceMeters,
		AvgPaceSPerKm:       row.AvgPaceSPerKm,
		MovingAvgPaceSPerKm: row.MovingAvgPaceSPerKm,
		AdjustedPaceSPerKm:  row.AdjustedPaceSPerKm,
		BestKmSeconds:       row.BestKmSeconds,
		AvgHeartRate:        row.AvgHeartRate,
		AvgCadence:          row.AvgCadence,
		AvgStrideLengthM:    row.AvgStrideLengthM,
		AvgPowerWatts:       row.AvgPowerWatts,
		ElevationGainM:      row.ElevationGainM,
		ElevationLossM:      row.ElevationLossM,
		Calories:            row.Calories,
		TrainingLoad:        row.TrainingLoad,
		AerobicTE:           row.AerobicTe,
		AnaerobicTE:         row.AnaerobicTe,
		TrainingFocus:       row.TrainingFocus,
		Performance:         row.Performance,
	}, nil
}

// outOfTime is what the connection says when the sync's time ran out before the figures were all kept.
func outOfTime(ctx context.Context) string {
	return fmt.Sprintf("the activities came in, but COROS's own figures for them couldn't be fetched: %v", ctx.Err())
}

// fetchSourceDetails asks the source for its own figures on the workouts that came from it and have none, newest first.
// It goes by what's stored and not by what the sync just listed, so one that failed is asked again next time.
// It returns how many it stored and, as words for the connection, why any couldn't be; only a failure that ends the sync (the grant gone) is an error.
func (m *storeModel) fetchSourceDetails(ctx context.Context, conn *SourceConnection, src ActivitySource, now time.Time, userToken *tokens.UserToken) (int, string, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return 0, "", err
	}
	due, err := q.ListSourceActivitiesWithoutMetadata(ctx, &db.ListSourceActivitiesWithoutMetadataParams{UserID: userToken.ID, Kind: conn.Kind, AtMost: sourceDetailsPerSync})
	if err != nil {
		return 0, "", err
	}
	stored, unread, problem := 0, 0, ""
	for _, row := range due {
		detail, err := src.Detail(ctx, coros.Activity{ID: row.ExternalID, SportType: int(*row.SportType)})
		switch {
		case errors.Is(err, coros.ErrAuthorization):
			return stored, "", err
		// One reply in words taiga can't read doesn't speak for the next activity, which may be another sport; nor does COROS having nothing for this one.
		case errors.Is(err, coros.ErrUnexpectedReply), errors.Is(err, coros.ErrNoDetail):
			log.Printf("sources: %s's figures for activity %s of user %d can't be read: %v", conn.Kind, row.ExternalID, userToken.ID, err)
			unread++
			if problem == "" {
				problem = err.Error()
			}
			// So the ones not yet tried come before it next time.
			if err := q.MarkSourceDetailTried(ctx, &db.MarkSourceDetailTriedParams{UserID: userToken.ID, Kind: conn.Kind, ExternalID: row.ExternalID, DetailTriedAt: timestamptz(now)}); err != nil {
				if ctx.Err() != nil {
					return stored, outOfTime(ctx), nil
				}
				return stored, "", err
			}
			continue
		// Anything else is COROS being unreachable or slow, and the rest would go the same way.
		case err != nil:
			log.Printf("sources: asking %s for its figures on activity %s of user %d: %v", conn.Kind, row.ExternalID, userToken.ID, err)
			return stored, fmt.Sprintf("the activities came in, but COROS's own figures for them couldn't be fetched: %v", err), nil
		}
		params := &db.UpsertCOROSMetadataParams{
			WorkoutID:           row.WorkoutID,
			UserID:              userToken.ID,
			ExternalID:          row.ExternalID,
			FetchedAt:           timestamptz(now),
			Reply:               detail.Reply,
			WorkoutSeconds:      detail.WorkoutSeconds,
			TotalSeconds:        detail.TotalSeconds,
			DistanceMeters:      detail.DistanceMeters,
			AvgPaceSPerKm:       detail.AvgPaceSecondsPerKm,
			MovingAvgPaceSPerKm: detail.MovingAvgPaceSecondsPerKm,
			AdjustedPaceSPerKm:  detail.AdjustedPaceSecondsPerKm,
			BestKmSeconds:       detail.BestKmSeconds,
			AvgHeartRate:        detail.AvgHeartRate,
			AvgCadence:          detail.AvgCadence,
			AvgStrideLengthM:    detail.AvgStrideLengthMeters,
			AvgPowerWatts:       detail.AvgPowerWatts,
			ElevationGainM:      detail.ElevationGainMeters,
			ElevationLossM:      detail.ElevationLossMeters,
			Calories:            detail.Calories,
			TrainingLoad:        detail.TrainingLoad,
			AerobicTe:           detail.AerobicTE,
			AnaerobicTe:         detail.AnaerobicTE,
			TrainingFocus:       detail.TrainingFocus,
			Performance:         detail.Performance,
		}
		// The call ran outside any transaction, and the workout may have been deleted meanwhile.
		kept := false
		err = m.store.InTx(ctx, func(q db.Querier) error {
			if _, err := q.GetWorkout(ctx, &db.GetWorkoutParams{ID: row.WorkoutID, UserID: userToken.ID}); err != nil {
				if IsNotFound(err) {
					return nil
				}
				return err
			}
			kept = true
			return q.UpsertCOROSMetadata(ctx, params)
		})
		// The sync's deadline can pass in a write as well as in a call, and the activities are in either way.
		if err != nil && ctx.Err() != nil {
			return stored, outOfTime(ctx), nil
		}
		if err != nil {
			// A delete that lands between the check and the insert fails the insert on its foreign key, which is the workout being gone and no fault of the sync.
			if _, gone := q.GetWorkout(ctx, &db.GetWorkoutParams{ID: row.WorkoutID, UserID: userToken.ID}); IsNotFound(gone) {
				continue
			}
			return stored, "", err
		}
		if kept {
			stored++
		}
	}
	if unread > 0 {
		problem = fmt.Sprintf("the activities came in, but COROS's own figures for %d of them couldn't be read: %s", unread, problem)
	}
	return stored, problem, nil
}
