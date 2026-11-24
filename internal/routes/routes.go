package routes

import (
	"encoding/json"
	"net/http"

	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/oidc"
	"gitea.deepak.science/deepak/taiga/internal/sources"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"github.com/go-chi/chi/v5"
)

// Handlers holds dependencies for HTTP handlers
// type Handlers struct {
// 	db *sql.DB
// }

type options struct {
	registrationOpen bool
	passwordLogin    bool
	oidc             *oidc.Registry
	sources          *sources.Manager
	admins           models.Admins
	deployment       Deployment
}

type Option func(*options)

// WithRegistration sets whether POST /auth/register accepts new users; it's open unless set.
func WithRegistration(open bool) Option {
	return func(o *options) { o.registrationOpen = open }
}

// WithPasswordLogin sets whether /auth/register and /auth/tokens work at all; on unless set.
func WithPasswordLogin(on bool) Option {
	return func(o *options) { o.passwordLogin = on }
}

// WithOIDC offers sign-in through the registry's providers.
func WithOIDC(r *oidc.Registry) Option {
	return func(o *options) { o.oidc = r }
}

// WithSources lets users connect accounts elsewhere to import from; without it there is nothing to connect.
func WithSources(g *sources.Manager) Option {
	return func(o *options) { o.sources = g }
}

// New creates a new handlers instance
func New(m models.Model, tok tokens.Toker, fileRepo filerepo.FileRepo, opts ...Option) http.Handler {
	o := options{registrationOpen: true, passwordLogin: true, oidc: &oidc.Registry{}}
	for _, opt := range opts {
		opt(&o)
	}

	r := chi.NewRouter()

	// Basic routes
	r.Get("/", hello)
	r.Get("/ping", ping)

	r.Mount("/health", newHealthRouter(m))
	r.Mount("/auth", newAuthRouter(m, tok, o))

	// Where the source sends the browser back, which arrives with a cookie and no token.
	r.Get("/sources/{source}/callback", sourceCallback(o.sources))

	// authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(tok.Authenticator)
		r.Mount("/activity_files", newActivityFileRouter(m, fileRepo))
		r.Mount("/workouts", newWorkoutRouter(m, fileRepo))
		r.Mount("/segments", newSegmentRouter(m))
		r.Mount("/jobs", newJobRouter(m))
		r.Mount("/routes", newRouteRouter(m))
		r.Mount("/stretches", newStretchRouter(m))
		r.Get("/sources", getSources(m, o.sources))
		r.Post("/sources/{source}", connectSource(o.sources))
		r.Delete("/sources/{source}", disconnectSource(m, o.sources))
		r.Post("/sources/{source}/sync", syncSource(m, o.sources))
		r.Get("/export", getExport(m, fileRepo))
		r.Post("/import", postImport(m, fileRepo))
		r.Mount("/admin", newAdminRouter(m, o))
	})

	return r
}

// Hello handles the root endpoint with a simple greeting
func hello(w http.ResponseWriter, r *http.Request) {
	response := map[string]any{
		"message": "Hello, World!",
		"status":  "success",
		"service": "taiga",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

// Ping handles simple ping requests
func ping(w http.ResponseWriter, r *http.Request) {
	response := map[string]string{
		"ping": "pong",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}
