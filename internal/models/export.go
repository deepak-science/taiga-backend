package models

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
	a := &backup.Archive{Manifest: manifest(backup.KindUser, fetched, now)}
	err := m.store.InSnapshot(ctx, func(q db.Querier) error {
		user, err := q.GetUser(ctx, userToken.ID)
		if err != nil {
			return err
		}
		a.Manifest.Owner = &backup.Owner{Email: user.Email, DisplayName: user.DisplayName}
		return exportInto(ctx, q, &a.Data, fetched, userToken.ID)
	})
	if err != nil {
		return nil, fmt.Errorf("exporting: %w", err)
	}
	return a, nil
}

// ExportDeployment is every account's archive, with what it takes to sign in to each again, from one snapshot.
func (m *storeModel) ExportDeployment(ctx context.Context, fetched bool, now time.Time) (*backup.Archive, error) {
	a := &backup.Archive{Manifest: manifest(backup.KindDeployment, fetched, now), Accounts: []backup.Account{}}
	err := m.store.InSnapshot(ctx, func(q db.Querier) error {
		users, err := q.ListUsers(ctx)
		if err != nil {
			return err
		}
		for i, u := range users {
			acct := backup.Account{
				Key:           fmt.Sprintf("account-%d", i+1),
				Email:         u.Email,
				DisplayName:   u.DisplayName,
				Password:      u.Password,
				EmailVerified: u.EmailVerified,
				Admin:         u.IsAdmin,
				CreatedAt:     u.CreatedAt.Time.UTC(),
				Identities:    []backup.Identity{},
			}
			ids, err := q.ListIdentitiesByUser(ctx, u.ID)
			if err != nil {
				return err
			}
			for _, id := range ids {
				acct.Identities = append(acct.Identities, backup.Identity{Issuer: id.Issuer, Subject: id.Subject, EmailAtLink: id.EmailAtLink, CreatedAt: id.CreatedAt.Time.UTC()})
			}
			if err := exportInto(ctx, q, &acct.Data, fetched, u.ID); err != nil {
				return fmt.Errorf("account %d: %w", u.ID, err)
			}
			a.Accounts = append(a.Accounts, acct)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("backing up: %w", err)
	}
	return a, nil
}

func manifest(kind string, fetched bool, now time.Time) backup.Manifest {
	return backup.Manifest{
		Format:           backup.Format,
		Kind:             kind,
		Version:          backup.Version,
		ProcessorVersion: workouts.ProcessorVersion,
		CreatedAt:        now.UTC(),
		Fetched:          fetched,
	}
}

func exportInto(ctx context.Context, q db.Querier, d *backup.Data, fetched bool, userID int32) error {

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
	// The file's workout's id, for the order below.
	hasWorkout := map[int32]int32{}
	d.Workouts = []backup.Workout{}
	d.Corrections = []backup.Correction{}
	for _, w := range ws {
		if w.ActivityFileID == nil {
			d.Workouts = append(d.Workouts, backup.Workout{
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
		hasWorkout[*w.ActivityFileID] = w.ID
		if w.SportOverride != "" || w.UserExcludedReason != "" {
			d.Corrections = append(d.Corrections, backup.Correction{File: hash, Sport: w.SportOverride, ExcludedReason: w.UserExcludedReason})
		}
	}
	// In the order their workouts were made, which a restore makes them in again: duplicate detection keeps the lower id between equal sources.
	// Files without a workout follow, in upload order.
	slices.SortStableFunc(files, func(a, b *db.ActivityFile) int {
		wa, oka := hasWorkout[a.ID]
		wb, okb := hasWorkout[b.ID]
		switch {
		case oka && okb:
			return cmp.Compare(wa, wb)
		case oka != okb:
			if oka {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.ID, b.ID)
	})
	d.Files = []backup.File{}
	for _, f := range files {
		if f.FileRepoHash == nil {
			continue
		}
		_, made := hasWorkout[f.ID]
		d.Files = append(d.Files, backup.File{
			SHA256:    *f.FileRepoHash,
			Timestamp: f.Timestamp.Time.UTC(),
			CreatedAt: f.CreatedAt.Time.UTC(),
			Workout:   made,
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
	d.Stretches = []backup.Stretch{}
	for _, s := range stretches {
		d.Stretches = append(d.Stretches, backup.Stretch{
			Name:       s.Name,
			Sport:      s.Sport,
			Meters:     s.Meters,
			Points:     json.RawMessage(s.Points),
			SourceFile: ref(s.SourceWorkoutID),
			CreatedAt:  s.CreatedAt.Time.UTC(),
		})
	}

	if err := exportRoutes(ctx, q, d, userID, ref); err != nil {
		return err
	}
	if err := exportSources(ctx, q, d, userID, fileHash, workoutFile); err != nil {
		return err
	}
	d.Weather = []backup.Weather{}
	d.COROSMetadata = []backup.COROSMetadata{}
	if fetched {
		return exportFetched(ctx, q, d, userID, workoutFile)
	}
	return nil
}

func exportRoutes(ctx context.Context, q db.Querier, d *backup.Data, userID int32, ref func(*int32) *string) error {
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
	d.Routes = []backup.Route{}
	for i, r := range routes {
		keys[r.ID] = fmt.Sprintf("route-%d", i+1)
		runs := members[r.ID]
		if runs == nil {
			runs = []string{}
		}
		d.Routes = append(d.Routes, backup.Route{
			Key:            keys[r.ID],
			Name:           r.Name,
			Representative: ref(r.RepresentativeWorkoutID),
			Workouts:       runs,
			CreatedAt:      r.CreatedAt.Time.UTC(),
		})
	}
	d.RouteOverrides = []backup.RouteOverride{}
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
		d.RouteOverrides = append(d.RouteOverrides, backup.RouteOverride{File: *hash, Route: route, WithGroup: o.WithGroup, CreatedAt: o.CreatedAt.Time.UTC()})
	}
	return nil
}

func exportSources(ctx context.Context, q db.Querier, d *backup.Data, userID int32, fileHash, workoutFile map[int32]string) error {
	activities, err := q.ListSourceActivitiesByUser(ctx, userID)
	if err != nil {
		return err
	}
	d.SourceActivities = []backup.SourceActivity{}
	for _, sa := range activities {
		var file *string
		if sa.ActivityFileID != nil {
			if hash, ok := fileHash[*sa.ActivityFileID]; ok {
				file = &hash
			}
		}
		d.SourceActivities = append(d.SourceActivities, backup.SourceActivity{
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
	d.SourceMatchAttempts = []backup.SourceMatchAttempt{}
	for _, at := range attempts {
		if hash, ok := workoutFile[at.WorkoutID]; ok {
			d.SourceMatchAttempts = append(d.SourceMatchAttempts, backup.SourceMatchAttempt{File: hash, Kind: at.Kind, TriedAt: at.TriedAt.Time.UTC()})
		}
	}
	return nil
}

func exportFetched(ctx context.Context, q db.Querier, d *backup.Data, userID int32, workoutFile map[int32]string) error {
	readings, err := q.ListWorkoutWeatherByUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, r := range readings {
		hash, ok := workoutFile[r.WorkoutID]
		if !ok {
			continue
		}
		d.Weather = append(d.Weather, backup.Weather{
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
		d.COROSMetadata = append(d.COROSMetadata, backup.COROSMetadata{
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
