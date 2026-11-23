package models

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"gitea.deepak.science/deepak/taiga/internal/backup"
	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

// ErrNotEmpty means a restore was pointed at a deployment that already has accounts.
var ErrNotEmpty = errors.New("this deployment already has accounts; restore only into an empty one")

// RestoreReport is what restoring one account did, and what it couldn't.
type RestoreReport struct {
	UserID   int32
	Workouts int
	// Unreadable is the files whose workout this taiga couldn't make again, with why; they're kept as files without a workout.
	Unreadable map[string]string
}

// RestoreAccount makes the account and everything of it the archive holds, in a deployment with no other account of that email.
// The files' bytes must already be in fileRepo.
// Workouts are made from their files as an upload makes them, so their derived data is this taiga's; the rest is put back over them.
func (m *storeModel) RestoreAccount(ctx context.Context, acct *backup.Account, fileRepo filerepo.FileRepo, now time.Time) (*RestoreReport, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	user, err := q.CreateUser(ctx, &db.CreateUserParams{Email: acct.Email, DisplayName: acct.DisplayName, Password: acct.Password, EmailVerified: acct.EmailVerified})
	if err != nil {
		return nil, fmt.Errorf("creating the account: %w", err)
	}
	token := &tokens.UserToken{ID: user.ID, Email: user.Email}
	report := &RestoreReport{UserID: user.ID, Unreadable: map[string]string{}}
	for _, id := range acct.Identities {
		if _, err := q.CreateIdentity(ctx, &db.CreateIdentityParams{UserID: user.ID, Issuer: id.Issuer, Subject: id.Subject, EmailAtLink: id.EmailAtLink}); err != nil {
			return nil, fmt.Errorf("linking %s: %w", id.Issuer, err)
		}
	}
	d := &acct.Data

	// Stretches go first, so making each workout times it over them.
	stretchIDs := make([]int32, len(d.Stretches))
	for i, s := range d.Stretches {
		row, err := q.CreateStretch(ctx, &db.CreateStretchParams{UserID: user.ID, Name: s.Name, Sport: s.Sport, Meters: s.Meters, Points: s.Points})
		if err != nil {
			return nil, fmt.Errorf("stretch %q: %w", s.Name, err)
		}
		stretchIDs[i] = row.ID
	}

	fileIDs := map[string]int32{}
	workoutIDs := map[string]int32{}
	for _, f := range d.Files {
		hash := f.SHA256
		af, err := q.CreateActivityFile(ctx, &db.CreateActivityFileParams{Timestamp: timestamptz(f.Timestamp), FileRepoHash: &hash, UserID: &user.ID})
		if err != nil {
			return nil, fmt.Errorf("file %s: %w", hash, err)
		}
		fileIDs[hash] = af.ID
		if !f.Workout {
			continue
		}
		w, _, err := m.CreateWorkoutFromActivityFile(ctx, af.ID, token, fileRepo)
		if errors.Is(err, workouts.ErrUnreadableFile) || errors.Is(err, workouts.ErrUnsupportedActivity) {
			report.Unreadable[hash] = err.Error()
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("making the workout from %s: %w", hash, err)
		}
		workoutIDs[hash] = w.ID
		report.Workouts++
	}
	for _, w := range d.Workouts {
		if _, err := m.AddWorkout(ctx, &Workout{
			DistanceMiles: w.DistanceMiles, TimeSeconds: w.TimeSeconds, SpeedMph: w.SpeedMph, PaceMinPerMile: w.PaceMinPerMile,
			StartTime: optionalTimestamptz(w.StartTime), EndTime: optionalTimestamptz(w.EndTime), Sport: w.Sport, SubSport: w.SubSport,
		}, token); err != nil {
			return nil, fmt.Errorf("hand-entered workout: %w", err)
		}
		report.Workouts++
	}
	for _, c := range d.Corrections {
		id, ok := workoutIDs[c.File]
		if !ok {
			continue
		}
		if _, err := m.CorrectWorkout(ctx, id, Corrections{Sport: c.Sport, ExcludedReason: c.ExcludedReason}, token, fileRepo); err != nil {
			return nil, fmt.Errorf("correcting the workout from %s: %w", c.File, err)
		}
	}
	ref := func(hash *string) *int32 {
		if hash == nil {
			return nil
		}
		if id, ok := workoutIDs[*hash]; ok {
			return &id
		}
		return nil
	}
	for i, s := range d.Stretches {
		if src := ref(s.SourceFile); src != nil {
			if err := q.SetStretchSourceWorkout(ctx, &db.SetStretchSourceWorkoutParams{ID: stretchIDs[i], UserID: user.ID, SourceWorkoutID: src}); err != nil {
				return nil, err
			}
		}
	}

	if err := restoreRoutes(ctx, q, d, user.ID, ref); err != nil {
		return nil, err
	}
	if err := restoreSources(ctx, q, d, user.ID, fileIDs, workoutIDs); err != nil {
		return nil, err
	}
	// After every reprocess above, which drops a reading whose workout's span moved.
	if err := restoreFetched(ctx, q, d, user.ID, workoutIDs); err != nil {
		return nil, err
	}
	// Detection finds the duplicates and regroups the routes, keeping the restored names; the server's worker runs it.
	if err := q.EnqueueDetectRoutes(ctx, &db.EnqueueDetectRoutesParams{UserID: user.ID, RunAt: timestamptz(now)}); err != nil {
		return nil, err
	}
	return report, nil
}

// restoreRoutes puts the routes back as detection left them, so the next detection keeps their names.
func restoreRoutes(ctx context.Context, q db.Querier, d *backup.Data, userID int32, ref func(*string) *int32) error {
	routeIDs := map[string]int32{}
	for _, r := range d.Routes {
		row, err := q.CreateRoute(ctx, &db.CreateRouteParams{UserID: userID, RepresentativeWorkoutID: ref(r.Representative)})
		if err != nil {
			return fmt.Errorf("route %s: %w", r.Key, err)
		}
		routeIDs[r.Key] = row.ID
		if r.Name != nil {
			if _, err := q.RenameRoute(ctx, &db.RenameRouteParams{ID: row.ID, UserID: userID, Name: r.Name}); err != nil {
				return err
			}
		}
		for _, hash := range r.Workouts {
			if id := ref(&hash); id != nil {
				if err := q.AddWorkoutRoute(ctx, &db.AddWorkoutRouteParams{WorkoutID: *id, RouteID: row.ID, UserID: userID}); err != nil {
					return err
				}
			}
		}
	}
	for _, o := range d.RouteOverrides {
		id := ref(&o.File)
		if id == nil {
			continue
		}
		var route *int32
		if o.Route != nil {
			rid, ok := routeIDs[*o.Route]
			if !ok {
				continue
			}
			route = &rid
		}
		if err := q.UpsertRouteOverride(ctx, &db.UpsertRouteOverrideParams{WorkoutID: *id, UserID: userID, RouteID: route, WithGroup: o.WithGroup}); err != nil {
			return err
		}
	}
	return nil
}

func restoreSources(ctx context.Context, q db.Querier, d *backup.Data, userID int32, fileIDs, workoutIDs map[string]int32) error {
	for _, sa := range d.SourceActivities {
		var file *int32
		if sa.File != nil {
			if id, ok := fileIDs[*sa.File]; ok {
				file = &id
			}
		}
		if err := q.CreateSourceActivity(ctx, &db.CreateSourceActivityParams{
			UserID: userID, Kind: sa.Kind, ExternalID: sa.ExternalID, ActivityFileID: file,
			StartTime: optionalTimestamptz(sa.StartTime), Files: sa.Files, FetchedAt: timestamptz(sa.FetchedAt), SportType: sa.SportType,
		}); err != nil {
			return fmt.Errorf("source activity %s: %w", sa.ExternalID, err)
		}
		if sa.DetailTriedAt != nil {
			if err := q.MarkSourceDetailTried(ctx, &db.MarkSourceDetailTriedParams{UserID: userID, Kind: sa.Kind, ExternalID: sa.ExternalID, DetailTriedAt: timestamptz(*sa.DetailTriedAt)}); err != nil {
				return err
			}
		}
	}
	for _, at := range d.SourceMatchAttempts {
		id, ok := workoutIDs[at.File]
		if !ok {
			continue
		}
		if err := q.MarkSourceMatchTried(ctx, &db.MarkSourceMatchTriedParams{WorkoutID: id, Kind: at.Kind, UserID: userID, TriedAt: timestamptz(at.TriedAt)}); err != nil {
			return err
		}
	}
	return nil
}

func restoreFetched(ctx context.Context, q db.Querier, d *backup.Data, userID int32, workoutIDs map[string]int32) error {
	for _, r := range d.Weather {
		id, ok := workoutIDs[r.File]
		if !ok {
			continue
		}
		// A processor change can rebuild the workout over another span; a reading for the old one is the wrong hours, and leaving it out has the sweep ask again.
		w, err := q.GetWorkout(ctx, &db.GetWorkoutParams{ID: id, UserID: userID})
		if err != nil {
			return err
		}
		if !sameSecond(w.StartTime, timestamptz(r.AskedStart)) || !sameSecond(w.EndTime, timestamptz(r.AskedEnd)) {
			continue
		}
		if err := q.UpsertWorkoutWeather(ctx, &db.UpsertWorkoutWeatherParams{
			WorkoutID: id, Source: r.Source, UserID: userID, FetchedAt: timestamptz(r.FetchedAt),
			Latitude: r.Latitude, Longitude: r.Longitude, AskedStart: timestamptz(r.AskedStart), AskedEnd: timestamptz(r.AskedEnd),
			StartTemperatureC: r.Start.TemperatureC, StartDewPointC: r.Start.DewPointC, StartHumidityPct: r.Start.HumidityPct,
			StartWindSpeedMps: r.Start.WindSpeedMps, StartWindDirectionDeg: r.Start.WindDirectionDeg, StartCloudCoverPct: r.Start.CloudCoverPct,
			EndTemperatureC: r.End.TemperatureC, EndDewPointC: r.End.DewPointC, EndHumidityPct: r.End.HumidityPct,
			EndWindSpeedMps: r.End.WindSpeedMps, EndWindDirectionDeg: r.End.WindDirectionDeg, EndCloudCoverPct: r.End.CloudCoverPct,
			PrecipitationMm: r.PrecipitationMM,
		}); err != nil {
			return fmt.Errorf("weather for %s: %w", r.File, err)
		}
	}
	for _, c := range d.COROSMetadata {
		id, ok := workoutIDs[c.File]
		if !ok {
			continue
		}
		if err := q.UpsertCOROSMetadata(ctx, &db.UpsertCOROSMetadataParams{
			WorkoutID: id, UserID: userID, ExternalID: c.ExternalID, FetchedAt: timestamptz(c.FetchedAt), Reply: c.Reply,
			WorkoutSeconds: c.WorkoutSeconds, TotalSeconds: c.TotalSeconds, DistanceMeters: c.DistanceMeters,
			AvgPaceSPerKm: c.AvgPaceSPerKm, MovingAvgPaceSPerKm: c.MovingAvgPaceSPerKm, AdjustedPaceSPerKm: c.AdjustedPaceSPerKm,
			BestKmSeconds: c.BestKmSeconds, AvgHeartRate: c.AvgHeartRate, AvgCadence: c.AvgCadence,
			AvgStrideLengthM: c.AvgStrideLengthM, AvgPowerWatts: c.AvgPowerWatts,
			ElevationGainM: c.ElevationGainM, ElevationLossM: c.ElevationLossM, Calories: c.Calories,
			TrainingLoad: c.TrainingLoad, AerobicTe: c.AerobicTE, AnaerobicTe: c.AnaerobicTE,
			TrainingFocus: c.TrainingFocus, Performance: c.Performance,
		}); err != nil {
			return fmt.Errorf("COROS's figures for %s: %w", c.File, err)
		}
	}
	return nil
}

// CheckEmpty is ErrNotEmpty unless the deployment has no accounts yet.
func (m *storeModel) CheckEmpty(ctx context.Context) error {
	q, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	n, err := q.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("restore refused: %d accounts exist", n)
		return ErrNotEmpty
	}
	return nil
}

func optionalTimestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return timestamptz(*t)
}
