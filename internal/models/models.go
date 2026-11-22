package models

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/config"
	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/store"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/weather"
	"github.com/jackc/pgx/v5"
)

var ErrNoFileContent = errors.New("activity file has no file content")

// ErrActivityFileInUse means workouts still reference the activity file.
var ErrActivityFileInUse = errors.New("activity file still has workouts; delete them first")

// ErrNoActivityFile means a workout was entered by hand, so there's nothing to reprocess it from.
var ErrNoActivityFile = errors.New("workout has no activity file")

// ErrDerivedWorkout means an edit would change fields that reprocessing owns.
var ErrDerivedWorkout = errors.New("workout comes from an activity file, so its summary is rebuilt from the file; reprocess it instead of editing, or correct its sport or exclusion with PUT /workouts/{id}/corrections")

// ErrNoSeries means the workout has no stored records to graph or map.
var ErrNoSeries = errors.New("workout has no recorded series: it was entered by hand, or is waiting to be reprocessed")

// ErrJobNotDead means only a dead job can be retried.
var ErrJobNotDead = errors.New("only dead jobs can be retried")

// IsNotFound reports a missing row from either store: the sqlite wrapper surfaces database/sql's ErrNoRows, the postgres store pgx's.
func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows)
}

type Model interface {
	Healthy(ctx context.Context) error
	Close() error
	CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error)
	VerifyUserByEmailPassword(ctx context.Context, email string, password string) (*UserNoPassword, error)
	SignInWithIdentity(ctx context.Context, id Identity, policy SignInPolicy) (*UserNoPassword, error)
	LinkIdentity(ctx context.Context, id Identity, userToken *tokens.UserToken) error
	Identities(ctx context.Context, userToken *tokens.UserToken) (*UserIdentities, error)
	UnlinkIdentity(ctx context.Context, id int32, passwordLogin bool, issuers []string, userToken *tokens.UserToken) error
	// Signing in from a CLI: it asks, a signed-in user approves its code in a browser, and it polls for the grant.
	StartDeviceAuthorization(ctx context.Context, clientName string, now time.Time) (*DeviceStart, error)
	DeviceRequest(ctx context.Context, userCode string, now time.Time) (*DeviceRequest, error)
	ApproveDevice(ctx context.Context, userCode string, now time.Time, userToken *tokens.UserToken) error
	DenyDevice(ctx context.Context, userCode string, now time.Time) error
	// ExchangeDeviceCode returns ErrAuthorizationPending, ErrSlowDown, ErrExpiredToken, ErrAccessDenied or ErrInvalidGrant until there's a grant.
	ExchangeDeviceCode(ctx context.Context, deviceCode string, now time.Time) (*CLIGrant, error)
	// A CLI session's refresh token buys new access tokens until it's revoked or lapses unused; ErrInvalidGrant after that.
	RefreshCLISession(ctx context.Context, refreshToken string, now time.Time) (*UserNoPassword, error)
	RevokeCLISessionByToken(ctx context.Context, refreshToken string) error
	CLISessions(ctx context.Context, userToken *tokens.UserToken) ([]*CLISession, error)
	RevokeCLISession(ctx context.Context, id int32, userToken *tokens.UserToken) error
	PruneCLIAuth(ctx context.Context, now time.Time) error
	// Sources: accounts elsewhere that new activities are read from (docs/sources.md).
	SourceClient(ctx context.Context, kind, issuer, redirectURI string) (*SourceClient, error)
	SourceClientByID(ctx context.Context, id int32) (*SourceClient, error)
	SaveSourceClient(ctx context.Context, kind, issuer, redirectURI, registration string) (*SourceClient, error)
	ReplaceSourceClient(ctx context.Context, id int32, was, registration string) (*SourceClient, error)
	ConnectSource(ctx context.Context, kind string, clientID int32, credentials string, now time.Time, userToken *tokens.UserToken) (*SourceConnection, error)
	SourceConnections(ctx context.Context, userToken *tokens.UserToken) ([]SourceConnectionInfo, error)
	SourceConnection(ctx context.Context, id int32, userToken *tokens.UserToken) (*SourceConnection, error)
	SourceConnectionByKind(ctx context.Context, kind string, userToken *tokens.UserToken) (*SourceConnection, error)
	DisconnectSource(ctx context.Context, id int32, userToken *tokens.UserToken) error
	SetSourceCredentials(ctx context.Context, id int32, credentials string, userToken *tokens.UserToken) error
	FailSourceSync(ctx context.Context, id int32, reason string, needsReconnect bool, userToken *tokens.UserToken) error
	EnqueueSyncSource(ctx context.Context, now time.Time, userToken *tokens.UserToken) error
	EnqueueSourceSyncsDue(ctx context.Context, now time.Time) (int64, error)
	SyncSource(ctx context.Context, conn *SourceConnection, src ActivitySource, fileRepo filerepo.FileRepo, now time.Time, userToken *tokens.UserToken) (*SyncResult, error)
	ActivityFile(ctx context.Context, id int32, userToken *tokens.UserToken) (*ActivityFile, error)
	ActivityFiles(ctx context.Context, userToken *tokens.UserToken) ([]*ActivityFile, error)
	ActivityFilesWithoutWorkout(ctx context.Context, userToken *tokens.UserToken) ([]*ActivityFile, error)
	// AddActivityFile returns the user's existing file, with created false, when they've already uploaded the same content.
	AddActivityFile(ctx context.Context, fileReader io.Reader, timestamp time.Time, userToken *tokens.UserToken, fileRepo filerepo.FileRepo) (af *ActivityFile, created bool, err error)
	UpdateActivityFile(ctx context.Context, id int32, timestamp time.Time, fileRepoHash *string, userToken *tokens.UserToken) (*ActivityFile, error)
	DeleteActivityFile(ctx context.Context, id int32, userToken *tokens.UserToken) error
	GetActivityFileContent(ctx context.Context, id int32, userToken *tokens.UserToken, fileRepo filerepo.FileRepo) (io.ReadCloser, error)
	Workout(ctx context.Context, id int32, userToken *tokens.UserToken) (*Workout, error)
	Workouts(ctx context.Context, query WorkoutQuery, userToken *tokens.UserToken) ([]*Workout, error)
	// WorkoutTotals sums each sport's counted workouts starting in the window.
	WorkoutTotals(ctx context.Context, window Window, userToken *tokens.UserToken) (*WorkoutTotals, error)
	// PeriodicTotals sums each sport's counted workouts per local week or month (EveryWeek, EveryMonth) across the window.
	PeriodicTotals(ctx context.Context, every string, window Window, loc *time.Location, userToken *tokens.UserToken) ([]*PeriodTotals, error)
	AddWorkout(ctx context.Context, workout *Workout, userToken *tokens.UserToken) (*Workout, error)
	UpdateWorkout(ctx context.Context, id int32, workout *Workout, userToken *tokens.UserToken) (*Workout, error)
	DeleteWorkout(ctx context.Context, id int32, userToken *tokens.UserToken) error
	GetWorkoutsByActivityFile(ctx context.Context, activityFileID int32, userToken *tokens.UserToken) ([]*Workout, error)
	// CreateWorkoutFromActivityFile returns the file's existing workout, with created false, if it already has one.
	CreateWorkoutFromActivityFile(ctx context.Context, activityFileID int32, userToken *tokens.UserToken, fileRepo filerepo.FileRepo) (w *Workout, created bool, err error)
	WorkoutSegments(ctx context.Context, workoutID int32, userToken *tokens.UserToken) ([]*WorkoutSegment, error)
	// BestSegments ranks only workouts of sport (one of workouts.ScoredSports).
	BestSegments(ctx context.Context, name, sport string, limit int32, window Window, userToken *tokens.UserToken) ([]*SegmentEffort, error)
	// YearlyBests is the best effort at every standard segment in each local year, for one sport.
	YearlyBests(ctx context.Context, sport string, loc *time.Location, userToken *tokens.UserToken) ([]*YearBests, error)
	CompareWorkouts(ctx context.Context, workoutIDs []int32, userToken *tokens.UserToken) ([]*WorkoutComparison, error)
	WorkoutSeries(ctx context.Context, workoutID int32, userToken *tokens.UserToken) ([]byte, error)
	// ReprocessWorkout rebuilds the workout's summary, segments and series from its activity file.
	ReprocessWorkout(ctx context.Context, workoutID int32, userToken *tokens.UserToken, fileRepo filerepo.FileRepo) (*Workout, error)
	// CorrectWorkout replaces a file-backed workout's corrections and reprocesses it with them.
	CorrectWorkout(ctx context.Context, workoutID int32, corrections Corrections, userToken *tokens.UserToken, fileRepo filerepo.FileRepo) (*Workout, error)
	EnqueueReprocessAll(ctx context.Context, now time.Time, userToken *tokens.UserToken) (int64, error)
	Routes(ctx context.Context, window Window, userToken *tokens.UserToken) ([]*RouteSummary, error)
	Route(ctx context.Context, id int32, window Window, userToken *tokens.UserToken) (*RouteDetail, error)
	RenameRoute(ctx context.Context, id int32, name *string, userToken *tokens.UserToken) (*RouteSummary, error)
	WorkoutRoute(ctx context.Context, workoutID int32, userToken *tokens.UserToken) (*RouteSummary, error)
	// The user's say over routes, applied on every detection; each change regroups the routes before returning.
	RouteOverride(ctx context.Context, workoutID int32, userToken *tokens.UserToken) (*RouteOverride, error)
	SetRouteOverride(ctx context.Context, workoutID int32, routeID *int32, userToken *tokens.UserToken) (*RouteOverride, error)
	ClearRouteOverride(ctx context.Context, workoutID int32, userToken *tokens.UserToken) error
	MergeRoutes(ctx context.Context, into, from int32, userToken *tokens.UserToken) (*RouteSummary, error)
	UnmergeRoute(ctx context.Context, id int32, userToken *tokens.UserToken) error
	// EnqueueDetectRoutes is called after anything that changes the user's tracks: a new, reprocessed or deleted workout.
	EnqueueDetectRoutes(ctx context.Context, now time.Time, userToken *tokens.UserToken) error
	DetectRoutes(ctx context.Context, userToken *tokens.UserToken) error
	// DetectDuplicates runs first in the same job, so routes are grouped without the copies.
	DetectDuplicates(ctx context.Context, now time.Time, userToken *tokens.UserToken) error
	// Stretches are named pieces of ground cut from a workout; every workout of the sport that passes over one gets an effort.
	Stretches(ctx context.Context, window Window, userToken *tokens.UserToken) ([]*StretchSummary, error)
	Stretch(ctx context.Context, id int32, window Window, userToken *tokens.UserToken) (*StretchDetail, error)
	// CreateStretch times the user's existing workouts over the new stretch before returning.
	CreateStretch(ctx context.Context, req NewStretch, userToken *tokens.UserToken) (*StretchDetail, error)
	RenameStretch(ctx context.Context, id int32, name string, userToken *tokens.UserToken) (*StretchDetail, error)
	DeleteStretch(ctx context.Context, id int32, userToken *tokens.UserToken) error
	WorkoutStretchEfforts(ctx context.Context, workoutID int32, userToken *tokens.UserToken) ([]*WorkoutStretchEffort, error)
	Jobs(ctx context.Context, state string, limit int32, userToken *tokens.UserToken) ([]*Job, error)
	Job(ctx context.Context, id int32, userToken *tokens.UserToken) (*Job, error)
	RetryDeadJob(ctx context.Context, id int32, now time.Time, userToken *tokens.UserToken) (*Job, error)
	// The worker's side of the queue, across all users.
	EnqueueStaleWorkouts(ctx context.Context, now time.Time) (int64, error)
	ClaimJob(ctx context.Context, now, leaseUntil time.Time) (*Job, error)
	FinishJob(ctx context.Context, id int32, now time.Time) error
	RetryJobLater(ctx context.Context, id int32, runAt time.Time, jobErr error, now time.Time) error
	KillJob(ctx context.Context, id int32, jobErr error, now time.Time) error
	PruneJobs(ctx context.Context, cutoff time.Time) (int64, error)
	// Weather comes from outside (weather.Provider), fetched by fetch_weather jobs and never by processing.
	EnqueueWeatherDue(ctx context.Context, now time.Time) (int64, error)
	EnqueueFetchWeather(ctx context.Context, workoutID int32, now time.Time, userToken *tokens.UserToken) error
	FetchWeather(ctx context.Context, workoutID int32, provider weather.Provider, now time.Time, userToken *tokens.UserToken) error
	WorkoutWeather(ctx context.Context, workoutID int32, userToken *tokens.UserToken) (*WorkoutWeather, error)
}

type storeModel struct {
	store store.Store
}

func New(cfg *config.Config) (Model, error) {

	s, err := store.GetStore(cfg)
	if err != nil {
		if s != nil {
			if closeErr := s.Close(); closeErr != nil {
				log.Printf("error closing store after init failure: %v", closeErr)
			}
		}
		return nil, fmt.Errorf("failed to initialize database :%w", err)
	}

	return &storeModel{
		store: s,
	}, nil
}

// mostly for convenience for testing
func NewFromStore(s store.Store) Model {
	return &storeModel{
		store: s,
	}
}

func (m *storeModel) Close() error {
	return m.store.Close()
}

func (m *storeModel) Healthy(ctx context.Context) error {
	return m.store.Healthy(ctx)
}
