package routes

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/coros"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"github.com/go-chi/chi/v5"
)

// Build is what the binary was built from, stamped by the flake's ldflags; empty in a plain go build.
type Build struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Deployment is what the admin's deployment page says is switched on, beside what routes already knows.
type Deployment struct {
	Build Build
	// The weather provider, empty when weather is off.
	Weather string
	Metrics bool
}

// WithAdmins sets who holds the admin role besides those granted it in the database: the verified emails auth.admins lists.
func WithAdmins(a models.Admins) Option {
	return func(o *options) { o.admins = a }
}

// WithDeployment fills in the admin's deployment page.
func WithDeployment(d Deployment) Option {
	return func(o *options) { o.deployment = d }
}

const adminJobListLimit = 200

func newAdminRouter(m models.Model, o options) http.Handler {
	router := chi.NewRouter()
	router.Use(adminOnly(m, o.admins))
	router.Get("/deployment", getDeployment(m, o))
	router.Get("/accounts", getAdminAccounts(m, o.admins))
	router.Post("/accounts", postAdminAccount(m))
	router.Delete("/accounts/{userid}/sessions", deleteAdminSessions(m))
	router.Get("/jobs", getAdminJobs(m))
	router.Post("/jobs/{jobid}/retry", retryAdminJob(m))
	router.Delete("/jobs/{jobid}", dropAdminJob(m))
	router.Post("/reprocess", postAdminReprocess(m))
	router.Get("/sources", getAdminSources(m))
	return router
}

// adminOnly answers 404 to anyone without the role, so the surface doesn't show, and logs every admin request with who made it.
// The role is read from the database each time, not the token, so revoking it works at once.
func adminOnly(m models.Model, admins models.Admins) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userToken, err := tokens.UserTokenFromContext(r.Context())
			if err != nil {
				notFoundHandler(w, r)
				return
			}
			ok, err := isAdmin(r.Context(), m, admins, userToken.ID)
			if err != nil {
				serverError(w, err)
				return
			}
			if !ok {
				notFoundHandler(w, r)
				return
			}
			log.Printf("admin: user %d (%s) %s %s", userToken.ID, userToken.Email, r.Method, r.URL.RequestURI())
			next.ServeHTTP(w, r)
		})
	}
}

func isAdmin(ctx context.Context, m models.Model, admins models.Admins, userID int32) (bool, error) {
	status, err := m.AdminStatus(ctx, userID)
	if models.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return admins.Is(status), nil
}

type meResponse struct {
	ID          int32  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	// Whether to show the admin link; the admin routes' 404 is what actually keeps others out.
	Admin bool `json:"admin"`
}

func getMe(m models.Model, admins models.Admins) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		user, err := m.User(r.Context(), userToken.ID)
		if models.IsNotFound(err) {
			unauthorizedHandler(w, r)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		admin, err := isAdmin(r.Context(), m, admins, userToken.ID)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, meResponse{ID: user.ID, Email: user.Email, DisplayName: user.DisplayName, Admin: admin})
	}
}

type deploymentFeatures struct {
	RegistrationOpen bool     `json:"registration_open"`
	PasswordLogin    bool     `json:"password_login"`
	OIDCProviders    []string `json:"oidc_providers"`
	// Empty when weather is off.
	Weather string   `json:"weather"`
	Sources []string `json:"sources"`
	Metrics bool     `json:"metrics"`
}

type deploymentResponse struct {
	Build Build `json:"build"`
	*models.DeploymentState
	Features deploymentFeatures `json:"features"`
	Queue    queueResponse      `json:"queue"`
}

type queueResponse struct {
	Jobs []jobCount `json:"jobs"`
	// How long the longest-waiting job of each kind that could run has waited, in seconds.
	OldestDueSeconds map[string]float64 `json:"oldest_due_seconds"`
	Connections      []connectionCount  `json:"connections"`
}

type jobCount struct {
	Kind  string `json:"kind"`
	State string `json:"state"`
	Jobs  int64  `json:"jobs"`
}

type connectionCount struct {
	Kind        string `json:"kind"`
	State       string `json:"state"`
	Connections int64  `json:"connections"`
}

// getDeployment reads the queue through MetricsSnapshot, so this page and Grafana count from the same queries.
func getDeployment(m models.Model, o options) http.HandlerFunc {
	features := deploymentFeatures{
		RegistrationOpen: o.passwordLogin && o.registrationOpen,
		PasswordLogin:    o.passwordLogin,
		OIDCProviders:    []string{},
		Weather:          o.deployment.Weather,
		Sources:          []string{},
		Metrics:          o.deployment.Metrics,
	}
	for _, p := range o.oidc.Providers() {
		features.OIDCProviders = append(features.OIDCProviders, p.Name)
	}
	if o.sources != nil {
		features.Sources = append(features.Sources, coros.Kind)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		state, err := m.DeploymentState(r.Context())
		if err != nil {
			serverError(w, err)
			return
		}
		snap, err := m.MetricsSnapshot(r.Context(), time.Now())
		if err != nil {
			serverError(w, err)
			return
		}
		queue := queueResponse{Jobs: []jobCount{}, OldestDueSeconds: map[string]float64{}, Connections: []connectionCount{}}
		for _, j := range snap.Jobs {
			queue.Jobs = append(queue.Jobs, jobCount{Kind: j.Kind, State: j.State, Jobs: j.Jobs})
		}
		for k, d := range snap.OldestDue {
			queue.OldestDueSeconds[k] = d.Seconds()
		}
		for _, c := range snap.Connections {
			queue.Connections = append(queue.Connections, connectionCount{Kind: c.Kind, State: c.State, Connections: c.Connections})
		}
		writeJSON(w, deploymentResponse{Build: o.deployment.Build, DeploymentState: state, Features: features, Queue: queue})
	}
}

func getAdminAccounts(m models.Model, admins models.Admins) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accounts, err := m.AdminAccounts(r.Context(), admins)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, accounts)
	}
}

type adminAccountRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
}

// minPasswordLength is useradd's.
const minPasswordLength = 8

// postAdminAccount is useradd through the API: the email counts as verified, since an admin vouches for it, and the account is never an admin.
func postAdminAccount(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req adminAccountRequest
		if !decodeSmall(w, r, &req) {
			return
		}
		req.Email = strings.TrimSpace(req.Email)
		req.DisplayName = strings.TrimSpace(req.DisplayName)
		if req.Email == "" || !strings.Contains(req.Email, "@") {
			badRequestError(w, errors.New("an email is required"))
			return
		}
		if len(req.Password) < minPasswordLength {
			badRequestError(w, fmt.Errorf("the password must be at least %d characters", minPasswordLength))
			return
		}
		if req.DisplayName == "" {
			req.DisplayName = req.Email
		}
		created, err := m.CreateUserUnlessTaken(r.Context(), &models.CreateUserRequest{Email: req.Email, DisplayName: req.DisplayName, Password: req.Password, EmailVerified: true})
		if errors.Is(err, models.ErrEmailTaken) {
			conflictError(w, errors.New("an account with this email already exists"))
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		log.Printf("admin: created user %d", created.Id)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, created)
	}
}

type endSessionsResponse struct {
	Ended int64 `json:"ended"`
}

func deleteAdminSessions(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := pathID(w, r, "userid")
		if !ok {
			return
		}
		if _, err := m.User(r.Context(), userID); err != nil {
			if models.IsNotFound(err) {
				notFoundHandler(w, r)
				return
			}
			serverError(w, err)
			return
		}
		n, err := m.EndSessions(r.Context(), userID)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, endSessionsResponse{Ended: n})
	}
}

func getAdminJobs(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f := models.JobFilter{State: q.Get("state"), Kind: q.Get("kind")}
		switch f.State {
		case "", models.JobQueued, models.JobRunning, models.JobDone, models.JobDead:
		default:
			badRequestError(w, fmt.Errorf("unknown job state %q", f.State))
			return
		}
		if f.Kind != "" && !slices.Contains(models.JobKinds, f.Kind) {
			badRequestError(w, fmt.Errorf("unknown job kind %q", f.Kind))
			return
		}
		if s := q.Get("user_id"); s != "" {
			id, err := strconv.ParseInt(s, 10, 32)
			if err != nil || id <= 0 {
				badRequestError(w, fmt.Errorf("user_id must be an account's id"))
				return
			}
			f.UserID = int32(id)
		}
		jobs, err := m.AdminJobs(r.Context(), f, adminJobListLimit)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, jobs)
	}
}

func retryAdminJob(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r, "jobid")
		if !ok {
			return
		}
		job, err := m.AdminRetryJob(r.Context(), id, time.Now())
		switch {
		case errors.Is(err, models.ErrJobNotDead), errors.Is(err, models.ErrJobPending):
			conflictError(w, err)
		case models.IsNotFound(err):
			notFoundHandler(w, r)
		case err != nil:
			serverError(w, err)
		default:
			writeJSON(w, job)
		}
	}
}

func dropAdminJob(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r, "jobid")
		if !ok {
			return
		}
		err := m.AdminDropJob(r.Context(), id)
		switch {
		case errors.Is(err, models.ErrJobRunning):
			conflictError(w, err)
		case models.IsNotFound(err):
			notFoundHandler(w, r)
		case err != nil:
			serverError(w, err)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

type adminReprocessRequest struct {
	// Nil reprocesses every account's workouts.
	UserID *int32 `json:"user_id"`
}

func postAdminReprocess(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req adminReprocessRequest
		if !decodeSmall(w, r, &req) {
			return
		}
		var userID int32
		if req.UserID != nil {
			if *req.UserID <= 0 {
				badRequestError(w, errors.New("user_id must be an account's id, or null for every account"))
				return
			}
			userID = *req.UserID
		}
		n, err := m.AdminReprocess(r.Context(), userID, time.Now())
		if models.IsNotFound(err) {
			unprocessableError(w, errors.New("no such account"))
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, reprocessAllResponse{Enqueued: n})
	}
}

func getAdminSources(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := m.AdminSources(r.Context(), time.Now())
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, s)
	}
}

// pathID reads a positive id from the path, answering 404 for anything else.
func pathID(w http.ResponseWriter, r *http.Request, name string) (int32, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 32)
	if err != nil || id <= 0 {
		notFoundHandler(w, r)
		return 0, false
	}
	return int32(id), true
}

// noteSeen records a token issued for the account, for the admin's accounts page; failing to is no reason to refuse the sign-in.
func noteSeen(ctx context.Context, m models.Model, userID int32) {
	if err := m.NoteSeen(ctx, userID, time.Now()); err != nil {
		log.Printf("noting user %d seen: %v", userID, err)
	}
}
