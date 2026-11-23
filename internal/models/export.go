package models

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"gitea.deepak.science/deepak/taiga/internal/backup"
	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

// Export is the user's archive but the files' bytes, read in one snapshot so it's one moment's state: a file a sync imports between two of its queries can't be left out while its source activity is kept.
// fetched false leaves out weather and COROS's figures.
func (m *storeModel) Export(ctx context.Context, fetched bool, now time.Time, userToken *tokens.UserToken) (*backup.Archive, error) {
	a := &backup.Archive{Manifest: backup.Manifest{
		Format:           backup.Format,
		Version:          backup.Version,
		ProcessorVersion: workouts.ProcessorVersion,
		CreatedAt:        now.UTC(),
		Fetched:          fetched,
	}}
	err := m.store.InSnapshot(ctx, func(q db.Querier) error {
		return exportInto(ctx, q, a, userToken.ID)
	})
	if err != nil {
		return nil, fmt.Errorf("exporting: %w", err)
	}
	return a, nil
}

func exportInto(ctx context.Context, q db.Querier, a *backup.Archive, userID int32) error {
	user, err := q.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	a.Manifest.Account = &backup.Account{Email: user.Email, DisplayName: user.DisplayName}

	files, err := q.ListActivityFilesByUser(ctx, &userID)
	if err != nil {
		return err
	}
	ws, err := q.ListWorkoutsByUser(ctx, userID)
	if err != nil {
		return err
	}
	fileHash := map[int32]string{}
	for _, f := range files {
		if f.FileRepoHash != nil {
			fileHash[f.ID] = *f.FileRepoHash
		}
	}
	// What a workout is called in the archive: its file's hash. A hand-entered one is never referred to.
	workoutFile := map[int32]string{}
	hasWorkout := map[int32]bool{}
	a.Workouts = []backup.Workout{}
	a.Corrections = []backup.Correction{}
	for _, w := range ws {
		if w.ActivityFileID == nil {
			a.Workouts = append(a.Workouts, backup.Workout{
				StartTime:      timePtr(w.StartTime),
				EndTime:        timePtr(w.EndTime),
				DistanceMiles:  w.DistanceMiles,
				TimeSeconds:    w.TimeSeconds,
				SpeedMph:       w.SpeedMph,
				PaceMinPerMile: w.PaceMinPerMile,
				Sport:          w.Sport,
				SubSport:       w.SubSport,
				CreatedAt:      w.CreatedAt.Time.UTC(),
			})
			continue
		}
		hash, ok := fileHash[*w.ActivityFileID]
		if !ok {
			continue
		}
		workoutFile[w.ID] = hash
		hasWorkout[*w.ActivityFileID] = true
		if w.SportOverride != "" || w.UserExcludedReason != "" {
			a.Corrections = append(a.Corrections, backup.Correction{File: hash, Sport: w.SportOverride, ExcludedReason: w.UserExcludedReason})
		}
	}
	a.Files = []backup.File{}
	for _, f := range files {
		if f.FileRepoHash == nil {
			continue
		}
		a.Files = append(a.Files, backup.File{
			SHA256:    *f.FileRepoHash,
			Timestamp: f.Timestamp.Time.UTC(),
			CreatedAt: f.CreatedAt.Time.UTC(),
			Workout:   hasWorkout[f.ID],
		})
	}
	ref := func(id *int32) *string {
		if id == nil {
			return nil
		}
		if hash, ok := workoutFile[*id]; ok {
			return &hash
		}
		return nil
	}

	stretches, err := q.ListStretchesByUser(ctx, userID)
	if err != nil {
		return err
	}
	a.Stretches = []backup.Stretch{}
	for _, s := range stretches {
		a.Stretches = append(a.Stretches, backup.Stretch{
			Name:       s.Name,
			Sport:      s.Sport,
			Meters:     s.Meters,
			Points:     json.RawMessage(s.Points),
			SourceFile: ref(s.SourceWorkoutID),
			CreatedAt:  s.CreatedAt.Time.UTC(),
		})
	}

	if err := exportRoutes(ctx, q, a, userID, ref); err != nil {
		return err
	}
	if err := exportSources(ctx, q, a, userID, fileHash, workoutFile); err != nil {
		return err
	}
	a.Weather = []backup.Weather{}
	a.COROSMetadata = []backup.COROSMetadata{}
	if a.Manifest.Fetched {
		return exportFetched(ctx, q, a, userID, workoutFile)
	}
	return nil
}

func exportRoutes(ctx context.Context, q db.Querier, a *backup.Archive, userID int32, ref func(*int32) *string) error {
	routes, err := q.ListRoutesByUser(ctx, userID)
	if err != nil {
		return err
	}
	memberships, err := q.ListWorkoutRoutesByUser(ctx, userID)
	if err != nil {
		return err
	}
	overrides, err := q.ListRouteOverridesByUser(ctx, userID)
	if err != nil {
		return err
	}
	members := map[int32][]string{}
	for _, wr := range memberships {
		if hash := ref(&wr.WorkoutID); hash != nil {
			members[wr.RouteID] = append(members[wr.RouteID], *hash)
		}
	}
	keys := map[int32]string{}
	a.Routes = []backup.Route{}
	for i, r := range routes {
		keys[r.ID] = fmt.Sprintf("route-%d", i+1)
		runs := members[r.ID]
		if runs == nil {
			runs = []string{}
		}
		a.Routes = append(a.Routes, backup.Route{
			Key:            keys[r.ID],
			Name:           r.Name,
			Representative: ref(r.RepresentativeWorkoutID),
			Workouts:       runs,
			CreatedAt:      r.CreatedAt.Time.UTC(),
		})
	}
	a.RouteOverrides = []backup.RouteOverride{}
	for _, o := range overrides {
		hash := ref(&o.WorkoutID)
		if hash == nil {
			continue
		}
		var route *string
		if o.RouteID != nil {
			key, ok := keys[*o.RouteID]
			if !ok {
				continue
			}
			route = &key
		}
		a.RouteOverrides = append(a.RouteOverrides, backup.RouteOverride{File: *hash, Route: route, WithGroup: o.WithGroup, CreatedAt: o.CreatedAt.Time.UTC()})
	}
	return nil
}

func exportSources(ctx context.Context, q db.Querier, a *backup.Archive, userID int32, fileHash, workoutFile map[int32]string) error {
	activities, err := q.ListSourceActivitiesByUser(ctx, userID)
	if err != nil {
		return err
	}
	a.SourceActivities = []backup.SourceActivity{}
	for _, sa := range activities {
		var file *string
		if sa.ActivityFileID != nil {
			if hash, ok := fileHash[*sa.ActivityFileID]; ok {
				file = &hash
			}
		}
		a.SourceActivities = append(a.SourceActivities, backup.SourceActivity{
			Kind:          sa.Kind,
			ExternalID:    sa.ExternalID,
			File:          file,
			StartTime:     timePtr(sa.StartTime),
			Files:         sa.Files,
			FetchedAt:     sa.FetchedAt.Time.UTC(),
			SportType:     sa.SportType,
			DetailTriedAt: timePtr(sa.DetailTriedAt),
		})
	}
	attempts, err := q.ListSourceMatchAttemptsByUser(ctx, userID)
	if err != nil {
		return err
	}
	a.SourceMatchAttempts = []backup.SourceMatchAttempt{}
	for _, at := range attempts {
		if hash, ok := workoutFile[at.WorkoutID]; ok {
			a.SourceMatchAttempts = append(a.SourceMatchAttempts, backup.SourceMatchAttempt{File: hash, Kind: at.Kind, TriedAt: at.TriedAt.Time.UTC()})
		}
	}
	return nil
}

func exportFetched(ctx context.Context, q db.Querier, a *backup.Archive, userID int32, workoutFile map[int32]string) error {
	readings, err := q.ListWorkoutWeatherByUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, r := range readings {
		hash, ok := workoutFile[r.WorkoutID]
		if !ok {
			continue
		}
		a.Weather = append(a.Weather, backup.Weather{
			File:       hash,
			Source:     r.Source,
			FetchedAt:  r.FetchedAt.Time.UTC(),
			Latitude:   r.Latitude,
			Longitude:  r.Longitude,
			AskedStart: r.AskedStart.Time.UTC(),
			AskedEnd:   r.AskedEnd.Time.UTC(),
			Start: backup.Conditions{
				TemperatureC: r.StartTemperatureC, DewPointC: r.StartDewPointC, HumidityPct: r.StartHumidityPct,
				WindSpeedMps: r.StartWindSpeedMps, WindDirectionDeg: r.StartWindDirectionDeg, CloudCoverPct: r.StartCloudCoverPct,
			},
			End: backup.Conditions{
				TemperatureC: r.EndTemperatureC, DewPointC: r.EndDewPointC, HumidityPct: r.EndHumidityPct,
				WindSpeedMps: r.EndWindSpeedMps, WindDirectionDeg: r.EndWindDirectionDeg, CloudCoverPct: r.EndCloudCoverPct,
			},
			PrecipitationMM: r.PrecipitationMm,
		})
	}
	figures, err := q.ListCOROSMetadataByUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, c := range figures {
		hash, ok := workoutFile[c.WorkoutID]
		if !ok {
			continue
		}
		a.COROSMetadata = append(a.COROSMetadata, backup.COROSMetadata{
			File: hash, ExternalID: c.ExternalID, FetchedAt: c.FetchedAt.Time.UTC(), Reply: c.Reply,
			WorkoutSeconds: c.WorkoutSeconds, TotalSeconds: c.TotalSeconds, DistanceMeters: c.DistanceMeters,
			AvgPaceSPerKm: c.AvgPaceSPerKm, MovingAvgPaceSPerKm: c.MovingAvgPaceSPerKm, AdjustedPaceSPerKm: c.AdjustedPaceSPerKm,
			BestKmSeconds: c.BestKmSeconds, AvgHeartRate: c.AvgHeartRate, AvgCadence: c.AvgCadence,
			AvgStrideLengthM: c.AvgStrideLengthM, AvgPowerWatts: c.AvgPowerWatts,
			ElevationGainM: c.ElevationGainM, ElevationLossM: c.ElevationLossM, Calories: c.Calories,
			TrainingLoad: c.TrainingLoad, AerobicTE: c.AerobicTe, AnaerobicTE: c.AnaerobicTe,
			TrainingFocus: c.TrainingFocus, Performance: c.Performance,
		})
	}
	return nil
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}
