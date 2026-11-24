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
)

// ErrNotEmpty means a restore was pointed at a deployment that already has accounts.
var ErrNotEmpty = errors.New("this deployment already has accounts; restore only into an empty one")

// RestoreReport is what restoring one account did, and what it couldn't.
type RestoreReport struct {
	UserID int32
	ImportReport
}

// RestoreAccount makes the account and everything of it the archive holds, in a deployment with no other account of that email.
// The files' bytes must already be in fileRepo.
// It brings the data in as an import does, into an account with nothing to match.
func (m *storeModel) RestoreAccount(ctx context.Context, acct *backup.Account, fileRepo filerepo.FileRepo, now time.Time) (*RestoreReport, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	user, err := q.CreateUser(ctx, &db.CreateUserParams{Email: acct.Email, DisplayName: acct.DisplayName, Password: acct.Password, EmailVerified: acct.EmailVerified})
	if err != nil {
		return nil, fmt.Errorf("creating the account: %w", err)
	}
	if acct.Admin {
		if _, err := q.SetUserAdmin(ctx, &db.SetUserAdminParams{ID: user.ID, IsAdmin: true}); err != nil {
			return nil, fmt.Errorf("granting admin: %w", err)
		}
	}
	token := &tokens.UserToken{ID: user.ID, Email: user.Email}
	report := &RestoreReport{UserID: user.ID}
	for _, id := range acct.Identities {
		if _, err := q.CreateIdentity(ctx, &db.CreateIdentityParams{UserID: user.ID, Issuer: id.Issuer, Subject: id.Subject, EmailAtLink: id.EmailAtLink}); err != nil {
			return nil, fmt.Errorf("linking %s: %w", id.Issuer, err)
		}
	}
	imported, err := m.importData(ctx, &acct.Data, fileRepo, now, token)
	if err != nil {
		return nil, err
	}
	report.ImportReport = *imported
	return report, nil
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
