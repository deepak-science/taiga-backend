package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/weather"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

const JobFetchWeather = "fetch_weather"

// weatherSweepSize caps the workouts one sweep queues for each source, which paces a backfill to a couple of hundred requests an hour.
const weatherSweepSize = 100

// ErrNoWeather means no source has answered for the workout: it has no track, weather is off, or the fetch is still to come.
var ErrNoWeather = errors.New("workout has no weather")

// WeatherReading is one source's answer for a workout.
type WeatherReading struct {
	Source          string             `json:"source"`
	FetchedAt       time.Time          `json:"fetched_at"`
	Start           weather.Conditions `json:"start"`
	End             weather.Conditions `json:"end"`
	PrecipitationMM float64            `json:"precipitation_mm"`
}

// WorkoutWeather is the reading from the best source the workout has, with every source's under Sources, best first.
type WorkoutWeather struct {
	*WeatherReading
	Sources []*WeatherReading `json:"sources"`
}

// EnqueueWeatherDue queues a fetch for every user's tracked workouts that some source should have by now and doesn't.
// It is the first fetch for a new run, the later look once a slower source has caught up, and the backfill.
func (m *storeModel) EnqueueWeatherDue(ctx context.Context, now time.Time) (int64, error) {
	querier, err := m.store.GetQuerier()
	if err != nil {
		return 0, err
	}
	var queued int64
	for _, source := range weather.Sources {
		n, err := querier.EnqueueWeatherDue(ctx, &db.EnqueueWeatherDueParams{
			RunAt:   timestamptz(now),
			EndedBy: timestamptz(now.Add(-source.MinAge)),
			Source:  source.Name,
			AtMost:  weatherSweepSize,
		})
		if err != nil {
			return queued, err
		}
		queued += n
	}
	return queued, nil
}

func (m *storeModel) EnqueueFetchWeather(ctx context.Context, workoutID int32, now time.Time, userToken *tokens.UserToken) error {
	querier, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	return querier.EnqueueFetchWeather(ctx, &db.EnqueueFetchWeatherParams{UserID: userToken.ID, WorkoutID: &workoutID, RunAt: timestamptz(now)})
}

// FetchWeather asks each source the workout is old enough for and has no reading from, or one for a span it no longer has.
// A source with nothing yet is skipped without error; the next sweep asks again.
func (m *storeModel) FetchWeather(ctx context.Context, workoutID int32, provider weather.Provider, now time.Time, userToken *tokens.UserToken) error {
	workout, err := m.Workout(ctx, workoutID, userToken)
	if err != nil {
		return err
	}
	querier, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	raw, err := querier.GetWorkoutTrack(ctx, &db.GetWorkoutTrackParams{WorkoutID: workoutID, UserID: userToken.ID})
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var track workouts.Track
	if err := json.Unmarshal(raw, &track); err != nil {
		return fmt.Errorf("decoding track of workout %d: %w", workoutID, err)
	}
	if len(track.Points) == 0 || !workout.StartTime.Valid || !workout.EndTime.Valid {
		return nil
	}
	lat, lon := weather.Round(track.Points[0][0], track.Points[0][1])
	start, end := workout.StartTime.Time, workout.EndTime.Time

	rows, err := querier.ListWorkoutWeather(ctx, &db.ListWorkoutWeatherParams{WorkoutID: workoutID, UserID: userToken.ID})
	if err != nil {
		return err
	}
	current := map[string]bool{}
	for _, row := range rows {
		current[row.Source] = row.AskedStart.Time.Equal(start) && row.AskedEnd.Time.Equal(end) && row.Latitude == lat && row.Longitude == lon
	}
	for _, source := range weather.Sources {
		if current[source.Name] || now.Sub(end) < source.MinAge {
			continue
		}
		reading, err := provider.Lookup(ctx, source.Name, lat, lon, start, end)
		if errors.Is(err, weather.ErrNotYet) {
			continue
		}
		if err != nil {
			return fmt.Errorf("weather for workout %d from %s: %w", workoutID, source.Name, err)
		}
		if err := querier.UpsertWorkoutWeather(ctx, &db.UpsertWorkoutWeatherParams{
			WorkoutID:             workoutID,
			Source:                source.Name,
			UserID:                userToken.ID,
			FetchedAt:             timestamptz(now),
			Latitude:              lat,
			Longitude:             lon,
			AskedStart:            timestamptz(start),
			AskedEnd:              timestamptz(end),
			StartTemperatureC:     reading.Start.TemperatureC,
			StartDewPointC:        reading.Start.DewPointC,
			StartHumidityPct:      reading.Start.HumidityPct,
			StartWindSpeedMps:     reading.Start.WindSpeedMPS,
			StartWindDirectionDeg: reading.Start.WindDirectionDeg,
			StartCloudCoverPct:    reading.Start.CloudCoverPct,
			EndTemperatureC:       reading.End.TemperatureC,
			EndDewPointC:          reading.End.DewPointC,
			EndHumidityPct:        reading.End.HumidityPct,
			EndWindSpeedMps:       reading.End.WindSpeedMPS,
			EndWindDirectionDeg:   reading.End.WindDirectionDeg,
			EndCloudCoverPct:      reading.End.CloudCoverPct,
			PrecipitationMm:       reading.PrecipitationMM,
		}); err != nil {
			return err
		}
	}
	return nil
}

// WorkoutWeather returns the workout's weather; ErrNoWeather when no source has answered for it.
func (m *storeModel) WorkoutWeather(ctx context.Context, workoutID int32, userToken *tokens.UserToken) (*WorkoutWeather, error) {
	if _, err := m.Workout(ctx, workoutID, userToken); err != nil {
		return nil, err
	}
	querier, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	rows, err := querier.ListWorkoutWeather(ctx, &db.ListWorkoutWeatherParams{WorkoutID: workoutID, UserID: userToken.ID})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNoWeather
	}
	readings := make([]*WeatherReading, len(rows))
	for i, row := range rows {
		readings[i] = &WeatherReading{
			Source:    row.Source,
			FetchedAt: row.FetchedAt.Time,
			Start: weather.Conditions{
				TemperatureC:     row.StartTemperatureC,
				DewPointC:        row.StartDewPointC,
				HumidityPct:      row.StartHumidityPct,
				WindSpeedMPS:     row.StartWindSpeedMps,
				WindDirectionDeg: row.StartWindDirectionDeg,
				CloudCoverPct:    row.StartCloudCoverPct,
			},
			End: weather.Conditions{
				TemperatureC:     row.EndTemperatureC,
				DewPointC:        row.EndDewPointC,
				HumidityPct:      row.EndHumidityPct,
				WindSpeedMPS:     row.EndWindSpeedMps,
				WindDirectionDeg: row.EndWindDirectionDeg,
				CloudCoverPct:    row.EndCloudCoverPct,
			},
			PrecipitationMM: row.PrecipitationMm,
		}
	}
	sort.SliceStable(readings, func(i, j int) bool {
		return weather.Rank(readings[i].Source) < weather.Rank(readings[j].Source)
	})
	return &WorkoutWeather{WeatherReading: readings[0], Sources: readings}, nil
}
