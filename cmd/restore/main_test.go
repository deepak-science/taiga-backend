package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/backup"
	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/weather"
	"github.com/muktihari/fit/profile/typedef"
)

var day = time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)

type deployment struct {
	m    models.Model
	q    db.Querier
	repo filerepo.FileRepo
}

func newDeployment(t *testing.T) *deployment {
	s := testsupport.NewStore(t)
	q, err := s.GetQuerier()
	require.NoError(t, err)
	return &deployment{m: models.NewFromStore(s), q: q, repo: testsupport.NewFileRepo(t)}
}

func (d *deployment) user(t *testing.T, email string) *tokens.UserToken {
	resp, err := d.m.CreateUser(t.Context(), &models.CreateUserRequest{Email: email, DisplayName: email, Password: "password123", EmailVerified: true})
	require.NoError(t, err)
	return &tokens.UserToken{ID: resp.Id, Email: email}
}

func (d *deployment) upload(t *testing.T, user *tokens.UserToken, content []byte) *models.Workout {
	af, _, err := d.m.AddActivityFile(t.Context(), bytes.NewReader(content), day, user, d.repo)
	require.NoError(t, err)
	w, _, err := d.m.CreateWorkoutFromActivityFile(t.Context(), af.ID, user, d.repo)
	require.NoError(t, err)
	return w
}

// detect runs what the restore leaves queued, as the server's worker would.
func (d *deployment) detect(t *testing.T) {
	users, err := d.q.ListUsers(t.Context())
	require.NoError(t, err)
	for _, u := range users {
		tok := &tokens.UserToken{ID: u.ID, Email: u.Email}
		require.NoError(t, d.m.DetectDuplicates(t.Context(), day, tok))
		require.NoError(t, d.m.DetectRoutes(t.Context(), tok))
	}
}

func (d *deployment) backup(t *testing.T) (*backup.Archive, []byte) {
	a, err := d.m.ExportDeployment(t.Context(), true, day)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, backup.Write(t.Context(), &buf, a, d.repo.Fetch))
	return a, buf.Bytes()
}

// comparable drops what a restore can't keep: when rows were made.
func comparable(a *backup.Archive) *backup.Archive {
	a.Manifest.CreatedAt = time.Time{}
	for i := range a.Accounts {
		acct := &a.Accounts[i]
		acct.CreatedAt = time.Time{}
		for j := range acct.Identities {
			acct.Identities[j].CreatedAt = time.Time{}
		}
		for j := range acct.Files {
			acct.Files[j].CreatedAt = time.Time{}
		}
		for j := range acct.Workouts {
			acct.Workouts[j].CreatedAt = time.Time{}
		}
		for j := range acct.Stretches {
			acct.Stretches[j].CreatedAt = time.Time{}
		}
		for j := range acct.Routes {
			acct.Routes[j].CreatedAt = time.Time{}
		}
		for j := range acct.RouteOverrides {
			acct.RouteOverrides[j].CreatedAt = time.Time{}
		}
	}
	return a
}

func TestBackupThenRestoreGivesTheSameBackup(t *testing.T) {
	ctx := context.Background()
	src := newDeployment(t)
	alice := src.user(t, "alice@example.com")
	bob := src.user(t, "bob@example.com")
	at := func(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

	splits := []testsupport.Split{{Meters: 2000, Seconds: 600}}
	first := src.upload(t, alice, testsupport.EncodeFIT(t, day, splits))
	second := src.upload(t, alice, testsupport.EncodeFIT(t, day.AddDate(0, 0, 1), splits))
	elsewhere := src.upload(t, alice, testsupport.EncodeFITHeading(t, day.AddDate(0, 0, 2), splits, 180))
	src.upload(t, bob, testsupport.EncodeFIT(t, day.AddDate(0, 0, 3), splits))
	_, _, err := src.m.AddActivityFile(ctx, strings.NewReader("not a FIT file"), day, alice, src.repo)
	require.NoError(t, err)
	_, err = src.m.AddWorkout(ctx, &models.Workout{DistanceMiles: new(3.0), TimeSeconds: new(1800.0)}, alice)
	require.NoError(t, err)

	_, err = src.q.CreateIdentity(ctx, &db.CreateIdentityParams{UserID: alice.ID, Issuer: "https://auth.example.com/", Subject: "alice-1", EmailAtLink: "alice@example.com"})
	require.NoError(t, err)
	src.detect(t)
	route, err := src.m.WorkoutRoute(ctx, first.ID, alice)
	require.NoError(t, err)
	_, err = src.m.RenameRoute(ctx, route.ID, new("Lakeshore"), alice)
	require.NoError(t, err)
	_, err = src.m.SetRouteOverride(ctx, elsewhere.ID, nil, alice)
	require.NoError(t, err)
	_, err = src.m.CorrectWorkout(ctx, second.ID, models.Corrections{ExcludedReason: "ran with a friend"}, alice, src.repo)
	require.NoError(t, err)
	_, err = src.m.CreateStretch(ctx, models.NewStretch{Name: "The hill", WorkoutID: first.ID, StartMeters: 500, EndMeters: 1500}, alice)
	require.NoError(t, err)
	fake := testsupport.NewFakeWeather(t)
	require.NoError(t, src.m.FetchWeather(ctx, first.ID, weather.NewOpenMeteo(fake.URL()), day.AddDate(0, 1, 0), alice))
	load := 120.0
	require.NoError(t, src.q.UpsertCOROSMetadata(ctx, &db.UpsertCOROSMetadataParams{WorkoutID: first.ID, UserID: alice.ID, ExternalID: "1", FetchedAt: at(day), Reply: "Activity Details", TrainingLoad: &load}))
	require.NoError(t, src.q.CreateSourceActivity(ctx, &db.CreateSourceActivityParams{UserID: alice.ID, Kind: "coros", ExternalID: "1", ActivityFileID: first.ActivityFileID, StartTime: at(day), Files: 1, FetchedAt: at(day)}))
	require.NoError(t, src.q.CreateSourceActivity(ctx, &db.CreateSourceActivityParams{UserID: alice.ID, Kind: "coros", ExternalID: "deleted", FetchedAt: at(day)}))
	require.NoError(t, src.q.MarkSourceMatchTried(ctx, &db.MarkSourceMatchTriedParams{WorkoutID: second.ID, Kind: "coros", UserID: alice.ID, TriedAt: at(day)}))

	// The correction queued detection again, as the worker would run it.
	src.detect(t)
	before, archive := src.backup(t)
	require.Len(t, before.Accounts, 2)
	assert.NotEmpty(t, before.Accounts[0].Weather, "the test covers every kind it can")
	assert.NotEmpty(t, before.Accounts[0].Routes)
	assert.NotEmpty(t, before.Accounts[0].RouteOverrides)
	assert.NotEmpty(t, before.Accounts[0].Stretches)
	assert.NotEmpty(t, before.Accounts[0].COROSMetadata)

	dst := newDeployment(t)
	var out bytes.Buffer
	require.NoError(t, run(ctx, dst.m, dst.repo, bytes.NewReader(archive), &out))
	assert.Contains(t, out.String(), "restored alice@example.com")
	dst.detect(t)
	after, _ := dst.backup(t)
	assert.Equal(t, comparable(before), comparable(after))

	t.Run("signs in with the same password", func(t *testing.T) {
		u, err := dst.m.VerifyUserByEmailPassword(ctx, "alice@example.com", "password123")
		require.NoError(t, err)
		assert.Equal(t, "alice@example.com", u.Email)
	})

	t.Run("the stretch times the restored workouts", func(t *testing.T) {
		stretches, err := dst.m.Stretches(ctx, models.Window{}, &tokens.UserToken{ID: 1})
		require.NoError(t, err)
		require.Len(t, stretches, 1)
		assert.Equal(t, 1, stretches[0].EffortCount, "the second run is excluded, and elsewhere doesn't pass it")
	})

	t.Run("only into an empty deployment", func(t *testing.T) {
		err := run(ctx, dst.m, dst.repo, bytes.NewReader(archive), &out)
		assert.ErrorIs(t, err, models.ErrNotEmpty)
	})

	t.Run("not a user's export", func(t *testing.T) {
		export, err := src.m.Export(ctx, true, day, alice)
		require.NoError(t, err)
		var buf bytes.Buffer
		require.NoError(t, backup.Write(ctx, &buf, export, src.repo.Fetch))
		err = run(ctx, newDeployment(t).m, dst.repo, &buf, &out)
		assert.ErrorContains(t, err, "one user's export")
	})
}

func TestARestoreKeepsTheSameRecordingOfADuplicate(t *testing.T) {
	ctx := context.Background()
	src := newDeployment(t)
	alice := src.user(t, "alice@example.com")
	splits := []testsupport.Split{{Meters: 2000, Seconds: 600}}
	start := day.AddDate(0, 0, 5)
	// Uploaded first but dated earlier, so a restore going by the files newest first would make the second watch's workout first.
	for i, name := range []string{"Watch A", "Watch B"} {
		fit := testsupport.EncodeFITDevice(t, start, splits, typedef.ManufacturerCoros, 0, name)
		af, _, err := src.m.AddActivityFile(ctx, bytes.NewReader(fit), day.AddDate(0, 0, 9+i), alice, src.repo)
		require.NoError(t, err)
		_, _, err = src.m.CreateWorkoutFromActivityFile(ctx, af.ID, alice, src.repo)
		require.NoError(t, err)
	}
	copyOf := func(d *deployment) string {
		d.detect(t)
		ws, err := d.m.Workouts(ctx, models.WorkoutQuery{}, alice)
		require.NoError(t, err)
		copies := []string{}
		for _, w := range ws {
			if w.DuplicateOf != nil {
				copies = append(copies, w.Source)
			}
		}
		require.Len(t, copies, 1)
		return copies[0]
	}
	require.Equal(t, "Watch B", copyOf(src), "between equal sources the first uploaded is kept")

	_, archive := src.backup(t)
	dst := newDeployment(t)
	require.NoError(t, run(ctx, dst.m, dst.repo, bytes.NewReader(archive), io.Discard))
	assert.Equal(t, "Watch B", copyOf(dst))
}

func TestARestoreLeavesOutWeatherForAnotherSpan(t *testing.T) {
	ctx := context.Background()
	src := newDeployment(t)
	alice := src.user(t, "alice@example.com")
	w := src.upload(t, alice, testsupport.EncodeFIT(t, day, []testsupport.Split{{Meters: 2000, Seconds: 600}}))
	fake := testsupport.NewFakeWeather(t)
	require.NoError(t, src.m.FetchWeather(ctx, w.ID, weather.NewOpenMeteo(fake.URL()), day.AddDate(0, 1, 0), alice))

	a, err := src.m.ExportDeployment(ctx, true, day)
	require.NoError(t, err)
	readings := a.Accounts[0].Weather
	require.GreaterOrEqual(t, len(readings), 2)
	// As if this taiga's processor now ends the workout a minute later than the one that asked.
	readings[0].AskedEnd = readings[0].AskedEnd.Add(-time.Minute)
	var buf bytes.Buffer
	require.NoError(t, backup.Write(ctx, &buf, a, src.repo.Fetch))

	dst := newDeployment(t)
	require.NoError(t, run(ctx, dst.m, dst.repo, &buf, io.Discard))
	after, err := dst.m.ExportDeployment(ctx, true, day)
	require.NoError(t, err)
	assert.Len(t, after.Accounts[0].Weather, len(readings)-1, "the reading for the old span is left for the sweep to ask again")
}
