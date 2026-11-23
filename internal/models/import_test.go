package models_test

import (
	"bytes"
	"context"
	"encoding/json"
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
)

type importSide struct {
	m    models.Model
	q    db.Querier
	repo filerepo.FileRepo
	user *tokens.UserToken
}

func newImportSide(t *testing.T, email string) *importSide {
	s := testsupport.NewStore(t)
	q, err := s.GetQuerier()
	require.NoError(t, err)
	m := models.NewFromStore(s)
	return &importSide{m: m, q: q, repo: testsupport.NewFileRepo(t), user: createUser(t, m, email)}
}

func (s *importSide) upload(t *testing.T, content []byte, timestamp time.Time) *models.Workout {
	af, _, err := s.m.AddActivityFile(t.Context(), bytes.NewReader(content), timestamp, s.user, s.repo)
	require.NoError(t, err)
	w, _, err := s.m.CreateWorkoutFromActivityFile(t.Context(), af.ID, s.user, s.repo)
	require.NoError(t, err)
	return w
}

// detect runs what an import leaves queued, as the server's worker would.
func (s *importSide) detect(t *testing.T) {
	require.NoError(t, s.m.DetectDuplicates(t.Context(), day1, s.user))
	require.NoError(t, s.m.DetectRoutes(t.Context(), s.user))
}

func (s *importSide) export(t *testing.T) *backup.Archive {
	a, err := s.m.Export(t.Context(), true, day1, s.user)
	require.NoError(t, err)
	return a
}

// importFrom sends the archive through a tar, as the endpoint has it, its files into this side's repo.
func (s *importSide) importFrom(t *testing.T, from *importSide, a *backup.Archive) *models.ImportReport {
	var buf bytes.Buffer
	require.NoError(t, backup.Write(t.Context(), &buf, a, from.repo.Fetch))
	read, err := backup.Read(&buf, func(_ string, content io.Reader) error {
		_, err := s.repo.Store(t.Context(), content)
		return err
	})
	require.NoError(t, err)
	report, err := s.m.ImportAccount(t.Context(), &read.Data, s.repo, day1, s.user)
	require.NoError(t, err)
	s.detect(t)
	return report
}

// userComparable drops what an import can't keep: when rows were made, and whose the archive was.
func userComparable(a *backup.Archive) backup.Data {
	d := a.Data
	for i := range d.Files {
		d.Files[i].CreatedAt = time.Time{}
	}
	for i := range d.Workouts {
		d.Workouts[i].CreatedAt = time.Time{}
	}
	for i := range d.Stretches {
		d.Stretches[i].CreatedAt = time.Time{}
	}
	for i := range d.Routes {
		d.Routes[i].CreatedAt = time.Time{}
	}
	for i := range d.RouteOverrides {
		d.RouteOverrides[i].CreatedAt = time.Time{}
	}
	return d
}

var importSplits = []testsupport.Split{{Meters: 2000, Seconds: 600}}

// richAccount gives the side's user every kind of state an export holds.
func richAccount(t *testing.T, s *importSide) {
	ctx := t.Context()
	at := func(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
	first := s.upload(t, testsupport.EncodeFIT(t, day1, importSplits), day1)
	second := s.upload(t, testsupport.EncodeFIT(t, day1.AddDate(0, 0, 1), importSplits), day1)
	elsewhere := s.upload(t, testsupport.EncodeFITHeading(t, day1.AddDate(0, 0, 2), importSplits, 180), day1)
	_, _, err := s.m.AddActivityFile(ctx, strings.NewReader("not a FIT file"), day1, s.user, s.repo)
	require.NoError(t, err)
	_, err = s.m.AddWorkout(ctx, &models.Workout{DistanceMiles: new(3.0), TimeSeconds: new(1800.0), StartTime: at(day1.Add(-48 * time.Hour))}, s.user)
	require.NoError(t, err)
	s.detect(t)
	route, err := s.m.WorkoutRoute(ctx, first.ID, s.user)
	require.NoError(t, err)
	_, err = s.m.RenameRoute(ctx, route.ID, new("Lakeshore"), s.user)
	require.NoError(t, err)
	_, err = s.m.SetRouteOverride(ctx, elsewhere.ID, nil, s.user)
	require.NoError(t, err)
	_, err = s.m.CorrectWorkout(ctx, second.ID, models.Corrections{ExcludedReason: "ran with a friend"}, s.user, s.repo)
	require.NoError(t, err)
	_, err = s.m.CreateStretch(ctx, models.NewStretch{Name: "The hill", WorkoutID: first.ID, StartMeters: 500, EndMeters: 1500}, s.user)
	require.NoError(t, err)
	fake := testsupport.NewFakeWeather(t)
	require.NoError(t, s.m.FetchWeather(ctx, first.ID, weather.NewOpenMeteo(fake.URL()), day1.AddDate(0, 1, 0), s.user))
	load := 120.0
	require.NoError(t, s.q.UpsertCOROSMetadata(ctx, &db.UpsertCOROSMetadataParams{WorkoutID: first.ID, UserID: s.user.ID, ExternalID: "1", FetchedAt: at(day1), Reply: "Activity Details", TrainingLoad: &load}))
	require.NoError(t, s.q.CreateSourceActivity(ctx, &db.CreateSourceActivityParams{UserID: s.user.ID, Kind: "coros", ExternalID: "1", ActivityFileID: first.ActivityFileID, StartTime: at(day1), Files: 1, FetchedAt: at(day1)}))
	require.NoError(t, s.q.MarkSourceMatchTried(ctx, &db.MarkSourceMatchTriedParams{WorkoutID: second.ID, Kind: "coros", UserID: s.user.ID, TriedAt: at(day1)}))
	s.detect(t)
}

func TestImportIntoAFreshAccountGivesTheSameExport(t *testing.T) {
	src := newImportSide(t, "alice@example.com")
	richAccount(t, src)
	archive := src.export(t)
	for _, docs := range []int{len(archive.Weather), len(archive.Routes), len(archive.RouteOverrides), len(archive.Stretches), len(archive.Corrections), len(archive.Workouts), len(archive.COROSMetadata)} {
		require.NotZero(t, docs, "the test covers every kind it can")
	}

	dst := newImportSide(t, "bob@example.com")
	report := dst.importFrom(t, src, archive)
	assert.Equal(t, 4, report.FilesAdded)
	assert.Equal(t, 4, report.Workouts, "three from files and the hand-entered one")
	assert.Len(t, report.Unreadable, 0, "the file that isn't FIT never had a workout")
	assert.Equal(t, userComparable(archive), userComparable(dst.export(t)))

	t.Run("again changes nothing", func(t *testing.T) {
		before := dst.export(t)
		report := dst.importFrom(t, src, archive)
		assert.Equal(t, &models.ImportReport{Files: 4, Unreadable: map[string]string{}}, report)
		assert.Equal(t, userComparable(before), userComparable(dst.export(t)))
	})
}

func TestImportIntoAnAccountWithSomeOfIt(t *testing.T) {
	ctx := context.Background()
	src := newImportSide(t, "alice@example.com")
	richAccount(t, src)
	archive := src.export(t)

	// The same first two runs, uploaded later, with their route named, a correction and a stretch of the account's own.
	dst := newImportSide(t, "bob@example.com")
	later := day1.AddDate(0, 0, 10)
	first := dst.upload(t, testsupport.EncodeFIT(t, day1, importSplits), later)
	second := dst.upload(t, testsupport.EncodeFIT(t, day1.AddDate(0, 0, 1), importSplits), later)
	dst.detect(t)
	route, err := dst.m.WorkoutRoute(ctx, first.ID, dst.user)
	require.NoError(t, err)
	_, err = dst.m.RenameRoute(ctx, route.ID, new("Mine"), dst.user)
	require.NoError(t, err)
	_, err = dst.m.CorrectWorkout(ctx, second.ID, models.Corrections{ExcludedReason: "felt off"}, dst.user, dst.repo)
	require.NoError(t, err)
	_, err = dst.m.CreateStretch(ctx, models.NewStretch{Name: "The hill", WorkoutID: first.ID, StartMeters: 200, EndMeters: 1200}, dst.user)
	require.NoError(t, err)
	_, err = dst.m.AddWorkout(ctx, &models.Workout{DistanceMiles: new(5.0), TimeSeconds: new(2700.0)}, dst.user)
	require.NoError(t, err)
	dst.detect(t)

	report := dst.importFrom(t, src, archive)
	assert.Equal(t, models.ImportReport{
		Files: 4, FilesAdded: 2, FilesRetimed: 2, Workouts: 2, Corrections: 1, Stretches: 1, Routes: 1, Unreadable: map[string]string{},
	}, *report, "the third run and the hand-entered one are new; the rest is the archive's over the account's")

	after := dst.export(t)
	t.Run("the archive wins", func(t *testing.T) {
		got := userComparable(after)
		want := userComparable(archive)
		assert.Equal(t, want.Files, got.Files, "the archive's timestamps")
		assert.Equal(t, want.Corrections, got.Corrections)
		assert.Equal(t, want.Routes, got.Routes, "named Lakeshore, not Mine")
		assert.Equal(t, want.RouteOverrides, got.RouteOverrides)
		require.Len(t, got.Stretches, 1)
		assert.JSONEq(t, string(want.Stretches[0].Points), string(got.Stretches[0].Points))
		assert.Equal(t, want.Weather, got.Weather)
		assert.Equal(t, want.COROSMetadata, got.COROSMetadata)
		assert.Equal(t, want.SourceActivities, got.SourceActivities)
	})

	t.Run("the stretch is timed again over its new ground", func(t *testing.T) {
		stretches, err := dst.m.Stretches(ctx, models.Window{}, dst.user)
		require.NoError(t, err)
		require.Len(t, stretches, 1)
		assert.Equal(t, 1, stretches[0].EffortCount, "the first run's; the second is excluded and the third doesn't pass it")
		detail, err := dst.m.Stretch(ctx, stretches[0].ID, models.Window{}, dst.user)
		require.NoError(t, err)
		require.Len(t, detail.Efforts, 1)
		assert.InDelta(t, 500, detail.Efforts[0].StartMeters, 20, "measured over the archive's points, from 500m")
	})

	t.Run("keeps what only the account has", func(t *testing.T) {
		var miles []float64
		for _, w := range after.Workouts {
			miles = append(miles, *w.DistanceMiles)
		}
		assert.ElementsMatch(t, []float64{3, 5}, miles)
	})

	t.Run("again changes nothing", func(t *testing.T) {
		report := dst.importFrom(t, src, archive)
		assert.Equal(t, &models.ImportReport{Files: 4, Unreadable: map[string]string{}}, report)
		assert.Equal(t, userComparable(after), userComparable(dst.export(t)))
	})
}

func TestImportKeepsHandEnteredRepeatsTogether(t *testing.T) {
	src := newImportSide(t, "alice@example.com")
	start := pgtype.Timestamptz{Time: day1, Valid: true}
	for range 2 {
		_, err := src.m.AddWorkout(t.Context(), &models.Workout{DistanceMiles: new(3.0), TimeSeconds: new(1800.0), StartTime: start}, src.user)
		require.NoError(t, err)
	}
	dst := newImportSide(t, "bob@example.com")
	report := dst.importFrom(t, src, src.export(t))
	assert.Equal(t, 1, report.Workouts, "matched by what was entered, so the second is the first again")
	raw, err := json.Marshal(dst.export(t).Workouts)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(raw), `"distance_miles":3`))
}

func TestImportFinishesAStretchAnEarlierImportLeftWithoutItsSource(t *testing.T) {
	src := newImportSide(t, "alice@example.com")
	richAccount(t, src)
	archive := src.export(t)
	require.Len(t, archive.Stretches, 1)
	s := archive.Stretches[0]

	dst := newImportSide(t, "bob@example.com")
	_, err := dst.q.CreateStretch(t.Context(), &db.CreateStretchParams{UserID: dst.user.ID, Name: s.Name, Sport: s.Sport, Meters: s.Meters, Points: s.Points})
	require.NoError(t, err)
	dst.importFrom(t, src, archive)

	stretches, err := dst.m.Stretches(t.Context(), models.Window{}, dst.user)
	require.NoError(t, err)
	require.Len(t, stretches, 1, "the same stretch, not a second one")
	assert.NotNil(t, stretches[0].SourceWorkoutID)
	assert.Equal(t, 1, stretches[0].EffortCount)
	assert.Equal(t, userComparable(archive), userComparable(dst.export(t)))
}

func TestImportNamesARouteTheAccountMergedOnce(t *testing.T) {
	ctx := context.Background()
	runs := [][]byte{
		testsupport.EncodeFIT(t, day1, importSplits),
		testsupport.EncodeFIT(t, day1.AddDate(0, 0, 1), importSplits),
		testsupport.EncodeFITHeading(t, day1.AddDate(0, 0, 2), importSplits, 180),
		testsupport.EncodeFITHeading(t, day1.AddDate(0, 0, 3), importSplits, 180),
	}
	upload := func(s *importSide) []*models.Workout {
		var ws []*models.Workout
		for _, r := range runs {
			ws = append(ws, s.upload(t, r, day1))
		}
		s.detect(t)
		return ws
	}
	src := newImportSide(t, "alice@example.com")
	srcRuns := upload(src)
	for i, name := range map[int]string{0: "North", 2: "South"} {
		route, err := src.m.WorkoutRoute(ctx, srcRuns[i].ID, src.user)
		require.NoError(t, err)
		_, err = src.m.RenameRoute(ctx, route.ID, new(name), src.user)
		require.NoError(t, err)
	}
	archive := src.export(t)
	require.Len(t, archive.Routes, 2)

	// The account has the two as one.
	dst := newImportSide(t, "bob@example.com")
	dstRuns := upload(dst)
	north, err := dst.m.WorkoutRoute(ctx, dstRuns[0].ID, dst.user)
	require.NoError(t, err)
	south, err := dst.m.WorkoutRoute(ctx, dstRuns[2].ID, dst.user)
	require.NoError(t, err)
	_, err = dst.m.MergeRoutes(ctx, north.ID, south.ID, dst.user)
	require.NoError(t, err)
	dst.detect(t)

	report := dst.importFrom(t, src, archive)
	assert.Equal(t, 1, report.Routes, "named once, by the first of the two")
	merged, err := dst.m.WorkoutRoute(ctx, dstRuns[2].ID, dst.user)
	require.NoError(t, err)
	assert.Equal(t, new("North"), merged.Name)
	again := dst.importFrom(t, src, archive)
	assert.Equal(t, 0, again.Routes, "not renamed back and forth")
}
