package models

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

// ErrJobRunning means a running job belongs to its worker until it finishes.
var ErrJobRunning = errors.New("the job is running; wait for it to finish or die")

// ErrNoSuchAccount means no account has that email, or more than one does in different case.
var ErrNoSuchAccount = errors.New("no single account has that email")

// AdminStatus is what deciding whether an account is an admin takes, read fresh on each admin request.
type AdminStatus = db.GetUserAdminRow

// Admins is the role as configured: the accounts given it in the database, plus those whose verified email auth.admins lists.
type Admins struct {
	emails []string
}

func NewAdmins(emails []string) Admins {
	a := Admins{}
	for _, e := range emails {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			a.emails = append(a.emails, e)
		}
	}
	return a
}

// Is reports whether the account holds the role.
// A listed email counts only once it's verified: open registration takes any email, so anyone could otherwise register an admin's before they do.
func (a Admins) Is(s *AdminStatus) bool {
	if s.IsAdmin {
		return true
	}
	return s.EmailVerified && slices.Contains(a.emails, strings.ToLower(s.Email))
}

func (m *storeModel) AdminStatus(ctx context.Context, userID int32) (*AdminStatus, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	return q.GetUserAdmin(ctx, userID)
}

// SetAdmin grants or revokes the role on the one account with that email, matched without case.
func (m *storeModel) SetAdmin(ctx context.Context, email string, admin bool) (*UserNoPassword, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	users, err := q.GetUsersByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	// Two accounts can differ only in case; then the exact one is meant, and without one neither is a safe guess.
	var match *db.GetUsersByEmailRow
	for _, u := range users {
		if len(users) == 1 || u.Email == email {
			match = u
		}
	}
	if match == nil {
		return nil, ErrNoSuchAccount
	}
	if _, err := q.SetUserAdmin(ctx, &db.SetUserAdminParams{ID: match.ID, IsAdmin: admin}); err != nil {
		return nil, err
	}
	return &UserNoPassword{ID: match.ID, Email: match.Email, DisplayName: match.DisplayName}, nil
}

// NoteSeen records that a token was issued for the account; it's what an admin sees as when it was last used.
func (m *storeModel) NoteSeen(ctx context.Context, userID int32, now time.Time) error {
	q, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	return q.TouchUserSeen(ctx, &db.TouchUserSeenParams{ID: userID, LastSeenAt: timestamptz(now)})
}

// AdminAccount is what an admin sees of an account: who it is, how it signs in, and how much it holds, never what.
type AdminAccount struct {
	ID            int32      `json:"id"`
	Email         string     `json:"email"`
	DisplayName   string     `json:"display_name"`
	EmailVerified bool       `json:"email_verified"`
	Admin         bool       `json:"admin"`
	CreatedAt     time.Time  `json:"created_at"`
	LastSeenAt    *time.Time `json:"last_seen_at"`
	Password      bool       `json:"password"`
	// The issuers of the account's linked sign-ins, in the order they were linked.
	Identities []string `json:"identities"`
	Workouts   int64    `json:"workouts"`
	Files      int64    `json:"files"`
	// Files with no workout, most often ones that couldn't be read.
	FilesWithoutWorkout int64 `json:"files_without_workout"`
	CLISessions         int64 `json:"cli_sessions"`
}

func (m *storeModel) AdminAccounts(ctx context.Context, admins Admins) ([]*AdminAccount, error) {
	out := []*AdminAccount{}
	err := m.store.InSnapshot(ctx, func(q db.Querier) error {
		rows, err := q.ListAccountsForAdmin(ctx)
		if err != nil {
			return err
		}
		issuers, err := q.ListAllIdentityIssuers(ctx)
		if err != nil {
			return err
		}
		byUser := map[int32][]string{}
		for _, i := range issuers {
			byUser[i.UserID] = append(byUser[i.UserID], i.Issuer)
		}
		for _, r := range rows {
			a := &AdminAccount{
				ID:                  r.ID,
				Email:               r.Email,
				DisplayName:         r.DisplayName,
				EmailVerified:       r.EmailVerified,
				Admin:               admins.Is(&AdminStatus{Email: r.Email, EmailVerified: r.EmailVerified, IsAdmin: r.IsAdmin}),
				CreatedAt:           r.CreatedAt.Time.UTC(),
				Password:            r.HasPassword,
				Identities:          byUser[r.ID],
				Workouts:            r.Workouts,
				Files:               r.Files,
				FilesWithoutWorkout: r.FilesWithoutWorkout,
				CLISessions:         r.CliSessions,
			}
			if a.Identities == nil {
				a.Identities = []string{}
			}
			if r.LastSeenAt.Valid {
				t := r.LastSeenAt.Time.UTC()
				a.LastSeenAt = &t
			}
			out = append(out, a)
		}
		return nil
	})
	return out, err
}

// EndSessions revokes every CLI session the account has; a browser's token can't be taken back and runs out within tokens.Lifetime.
func (m *storeModel) EndSessions(ctx context.Context, userID int32) (int64, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return 0, err
	}
	return q.DeleteCliSessionsByUser(ctx, userID)
}

// AdminJob is a job with the account it's for.
type AdminJob struct {
	*Job
	Email string `json:"email"`
}

// JobFilter narrows the admin's job list; a zero field doesn't filter.
type JobFilter struct {
	State  string
	Kind   string
	UserID int32
}

func (m *storeModel) AdminJobs(ctx context.Context, f JobFilter, limit int32) ([]*AdminJob, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	rows, err := q.ListJobsForAdmin(ctx, &db.ListJobsForAdminParams{State: f.State, Kind: f.Kind, UserID: f.UserID, RowLimit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]*AdminJob, 0, len(rows))
	for _, r := range rows {
		out = append(out, &AdminJob{
			Job: &Job{
				ID: r.ID, UserID: r.UserID, Kind: r.Kind, WorkoutID: r.WorkoutID, State: r.State, Attempts: r.Attempts,
				RunAt: r.RunAt, LeaseUntil: r.LeaseUntil, LastError: redactedPtr(r.LastError), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			},
			Email: r.Email,
		})
	}
	return out, nil
}

// AdminRetryJob requeues any user's dead job: not-found when there's no such job, ErrJobNotDead when it isn't dead, ErrJobPending when a newer one has its work.
func (m *storeModel) AdminRetryJob(ctx context.Context, id int32, now time.Time) (*Job, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	job, err := q.RequeueDeadJobForAdmin(ctx, &db.RequeueDeadJobForAdminParams{ID: id, RunAt: timestamptz(now)})
	if IsNotFound(err) {
		existing, getErr := q.GetJobForAdmin(ctx, id)
		if getErr != nil {
			return nil, getErr
		}
		return nil, notRequeued(existing)
	}
	if err != nil {
		return nil, err
	}
	job.LastError = redactedPtr(job.LastError)
	return job, nil
}

// AdminDropJob deletes a queued or dead job: not-found when there's no such job, ErrJobRunning while it runs.
// Whatever queued it may queue it again: the hourly sweeps do for syncs and weather, and a boot does for stale workouts.
func (m *storeModel) AdminDropJob(ctx context.Context, id int32) error {
	q, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	n, err := q.DeleteIdleJob(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		if _, err := q.GetJobForAdmin(ctx, id); err != nil {
			return err
		}
		return ErrJobRunning
	}
	return nil
}

// AdminReprocess queues reprocessing of one user's file-backed workouts, or every user's for userID 0; ones already queued aren't counted.
func (m *storeModel) AdminReprocess(ctx context.Context, userID int32, now time.Time) (int64, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return 0, err
	}
	if userID == 0 {
		return q.EnqueueAllWorkouts(ctx, timestamptz(now))
	}
	if _, err := q.GetUser(ctx, userID); err != nil {
		return 0, err
	}
	return q.EnqueueUserWorkouts(ctx, &db.EnqueueUserWorkoutsParams{UserID: userID, RunAt: timestamptz(now)})
}

// AdminSourceConnection is a connection's state with whose it is, never its tokens.
type AdminSourceConnection struct {
	ID             int32      `json:"id"`
	UserID         int32      `json:"user_id"`
	Email          string     `json:"email"`
	Kind           string     `json:"kind"`
	ClientID       int32      `json:"client_id"`
	CreatedAt      time.Time  `json:"created_at"`
	SyncedThrough  time.Time  `json:"synced_through"`
	LastSyncedAt   *time.Time `json:"last_synced_at"`
	LastError      string     `json:"last_error"`
	NeedsReconnect bool       `json:"needs_reconnect"`
	// How many of the source's activities have been dealt with.
	Activities int64 `json:"activities"`
	// Files the source has issued for the user in the last 24 hours, against FilesLimit.
	FilesToday int `json:"files_today"`
	FilesLimit int `json:"files_limit"`
}

// AdminSourceClient is the deployment's registration with a source, never its secret.
type AdminSourceClient struct {
	ID          int32     `json:"id"`
	Kind        string    `json:"kind"`
	Issuer      string    `json:"issuer"`
	RedirectURI string    `json:"redirect_uri"`
	CreatedAt   time.Time `json:"created_at"`
}

type AdminSources struct {
	Connections []*AdminSourceConnection `json:"connections"`
	Clients     []*AdminSourceClient     `json:"clients"`
}

func (m *storeModel) AdminSources(ctx context.Context, now time.Time) (*AdminSources, error) {
	out := &AdminSources{Connections: []*AdminSourceConnection{}, Clients: []*AdminSourceClient{}}
	err := m.store.InSnapshot(ctx, func(q db.Querier) error {
		conns, err := q.ListSourceConnectionsForAdmin(ctx, timestamptz(now.Add(-24*time.Hour)))
		if err != nil {
			return err
		}
		for _, c := range conns {
			a := &AdminSourceConnection{
				ID: c.ID, UserID: c.UserID, Email: c.Email, Kind: c.Kind, ClientID: c.ClientID,
				CreatedAt: c.CreatedAt.Time.UTC(), SyncedThrough: c.SyncedThrough.Time.UTC(),
				LastError: redacted(c.LastError), NeedsReconnect: c.NeedsReconnect,
				Activities: c.Activities, FilesToday: int(c.FilesSince), FilesLimit: SourceDailyFiles,
			}
			if c.LastSyncedAt.Valid {
				t := c.LastSyncedAt.Time.UTC()
				a.LastSyncedAt = &t
			}
			out.Connections = append(out.Connections, a)
		}
		clients, err := q.ListSourceClientsForAdmin(ctx)
		if err != nil {
			return err
		}
		for _, c := range clients {
			out.Clients = append(out.Clients, &AdminSourceClient{ID: c.ID, Kind: c.Kind, Issuer: c.Issuer, RedirectURI: c.RedirectUri, CreatedAt: c.CreatedAt.Time.UTC()})
		}
		return nil
	})
	return out, err
}

// DeploymentState is what the database says about the deployment as a whole.
type DeploymentState struct {
	ProcessorVersion string `json:"processor_version"`
	// File-backed workouts not yet rebuilt by ProcessorVersion; the worker queues them on boot.
	WorkoutsBehind int64 `json:"workouts_behind"`
	Accounts       int64 `json:"accounts"`
	Workouts       int64 `json:"workouts"`
	Files          int64 `json:"files"`
	// The migration the database is at; dirty means one failed partway and needs fixing by hand.
	MigrationVersion int64 `json:"migration_version"`
	MigrationDirty   bool  `json:"migration_dirty"`
}

func (m *storeModel) DeploymentState(ctx context.Context) (*DeploymentState, error) {
	out := &DeploymentState{ProcessorVersion: workouts.ProcessorVersion}
	version, dirty, err := m.store.SchemaVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the migration version: %w", err)
	}
	out.MigrationVersion, out.MigrationDirty = version, dirty
	err = m.store.InSnapshot(ctx, func(q db.Querier) error {
		behind, err := q.CountStaleWorkouts(ctx, workouts.ProcessorVersion)
		if err != nil {
			return fmt.Errorf("counting stale workouts: %w", err)
		}
		totals, err := q.CountDeploymentTotals(ctx)
		if err != nil {
			return fmt.Errorf("counting: %w", err)
		}
		out.WorkoutsBehind, out.Accounts, out.Workouts, out.Files = behind, totals.Accounts, totals.Workouts, totals.Files
		return nil
	})
	return out, err
}

// User is the account by id; not-found once it's gone.
func (m *storeModel) User(ctx context.Context, id int32) (*UserNoPassword, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	u, err := q.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	return &UserNoPassword{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName}, nil
}

// CreateUserUnlessTaken is CreateUser that's ErrEmailTaken when an account has the email in any case, as a provider's sign-up checks.
func (m *storeModel) CreateUserUnlessTaken(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	existing, err := q.GetUsersByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, ErrEmailTaken
	}
	return m.CreateUser(ctx, req)
}

// quoted is a Go-quoted span, which is how errors here carry what came from outside: a COROS reply's excerpt, or a request's URL with a workout's rounded location in it.
var quoted = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// redacted is an error as an admin may see it: what failed and where, without the quoted account data some errors carry.
func redacted(s string) string {
	return quoted.ReplaceAllString(s, `"…"`)
}

func redactedPtr(s *string) *string {
	if s == nil {
		return nil
	}
	r := redacted(*s)
	return &r
}
