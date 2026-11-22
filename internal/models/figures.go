package models

import (
	"context"
	"encoding/json"
	"fmt"

	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

// storeFigures replaces what the file states for the workout and its laps, and returns the workout with them.
func storeFigures(ctx context.Context, q db.Querier, workoutID int32, run *workouts.Run, userToken *tokens.UserToken) (*Workout, error) {
	laps := workouts.BuildLaps(run)
	if len(laps) == 0 {
		if err := q.DeleteWorkoutLaps(ctx, &db.DeleteWorkoutLapsParams{WorkoutID: workoutID, UserID: userToken.ID}); err != nil {
			return nil, err
		}
	} else {
		encoded, err := json.Marshal(laps)
		if err != nil {
			return nil, fmt.Errorf("encoding laps for workout %d: %w", workoutID, err)
		}
		if err := q.UpsertWorkoutLaps(ctx, &db.UpsertWorkoutLapsParams{WorkoutID: workoutID, UserID: userToken.ID, Laps: encoded}); err != nil {
			return nil, err
		}
	}
	f := run.Summary.Figures.Counted(run.Summary.Sport)
	return q.SetWorkoutFigures(ctx, &db.SetWorkoutFiguresParams{
		ID:                       workoutID,
		UserID:                   userToken.ID,
		TimerSeconds:             f.TimerSeconds,
		AvgHeartRate:             f.AvgHeartRate,
		MaxHeartRate:             f.MaxHeartRate,
		MinHeartRate:             f.MinHeartRate,
		AscentMeters:             f.AscentMeters,
		DescentMeters:            f.DescentMeters,
		Calories:                 f.Calories,
		TotalSteps:               f.TotalSteps,
		AvgCadence:               f.AvgCadence,
		AvgPowerWatts:            f.AvgPowerWatts,
		AvgEffortPaceMps:         f.AvgEffortPaceMPS,
		AvgStepLengthM:           f.AvgStepLengthMeters,
		AvgVerticalOscillationMm: f.AvgVerticalOscillationMM,
		AvgGroundContactMs:       f.AvgGroundContactMS,
		AvgVerticalRatioPct:      f.AvgVerticalRatioPct,
	})
}

// WorkoutLaps returns the stored laps as JSON, an empty list for a workout whose file recorded none or that has no file.
func (m *storeModel) WorkoutLaps(ctx context.Context, workoutID int32, userToken *tokens.UserToken) ([]byte, error) {
	if _, err := m.Workout(ctx, workoutID, userToken); err != nil {
		return nil, err
	}
	querier, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	laps, err := querier.GetWorkoutLaps(ctx, &db.GetWorkoutLapsParams{WorkoutID: workoutID, UserID: userToken.ID})
	if IsNotFound(err) {
		return []byte("[]"), nil
	}
	return laps, err
}
