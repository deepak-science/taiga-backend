package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
	"github.com/go-chi/chi/v5"
)

const maxStretchNameLength = 100

func newStretchRouter(m models.Model) http.Handler {
	router := chi.NewRouter()
	router.Get("/", getStretchesFunc(m))
	router.Post("/", postStretchFunc(m))
	router.Get("/{stretchid}", getStretchFunc(m))
	router.Put("/{stretchid}", putStretchFunc(m))
	router.Delete("/{stretchid}", deleteStretchFunc(m))
	return router
}

func stretchID(r *http.Request) (int32, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "stretchid"), 10, 32)
	return int32(id), err == nil
}

// stretchName trims a name and checks it's there and not too long.
func stretchName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("a stretch needs a name")
	}
	if len([]rune(name)) > maxStretchNameLength {
		return "", fmt.Errorf("a stretch's name is at most %d characters", maxStretchNameLength)
	}
	return name, nil
}

func getStretchesFunc(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		window, err := parseWindow(r.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		stretches, err := m.Stretches(r.Context(), window, userToken)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, stretches)
	}
}

func getStretchFunc(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		id, ok := stretchID(r)
		if !ok {
			notFoundHandler(w, r)
			return
		}
		window, err := parseWindow(r.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		stretch, err := m.Stretch(r.Context(), id, window, userToken)
		if models.IsNotFound(err) {
			notFoundHandler(w, r)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, stretch)
	}
}

type postStretchRequest struct {
	Name      string `json:"name"`
	WorkoutID int32  `json:"workout_id"`
	// Where the stretch starts and ends along the workout, as its series' distance_m counts.
	StartMeters *float64 `json:"start_m"`
	EndMeters   *float64 `json:"end_m"`
}

func postStretchFunc(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		var req postStretchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			badRequestError(w, fmt.Errorf("invalid JSON: %w", err))
			return
		}
		name, err := stretchName(req.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.WorkoutID == 0 || req.StartMeters == nil || req.EndMeters == nil {
			http.Error(w, "a stretch needs workout_id, start_m and end_m", http.StatusBadRequest)
			return
		}
		stretch, err := m.CreateStretch(r.Context(), models.NewStretch{Name: name, WorkoutID: req.WorkoutID, StartMeters: *req.StartMeters, EndMeters: *req.EndMeters}, userToken)
		if errors.Is(err, models.ErrStretchSource) || errors.Is(err, workouts.ErrStretchRange) {
			unprocessableError(w, err)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(stretch); err != nil {
			serverError(w, err)
		}
	}
}

type putStretchRequest struct {
	Name string `json:"name"`
}

func putStretchFunc(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		id, ok := stretchID(r)
		if !ok {
			notFoundHandler(w, r)
			return
		}
		var req putStretchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			badRequestError(w, fmt.Errorf("invalid JSON: %w", err))
			return
		}
		name, err := stretchName(req.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		stretch, err := m.RenameStretch(r.Context(), id, name, userToken)
		if models.IsNotFound(err) {
			notFoundHandler(w, r)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, stretch)
	}
}

func deleteStretchFunc(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		id, ok := stretchID(r)
		if !ok {
			notFoundHandler(w, r)
			return
		}
		err = m.DeleteStretch(r.Context(), id, userToken)
		if models.IsNotFound(err) {
			notFoundHandler(w, r)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getWorkoutStretchesFunc lists the workout's passes over the user's stretches, in the order it ran them.
func getWorkoutStretchesFunc(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "workoutid"), 10, 32)
		if err != nil {
			notFoundHandler(w, r)
			return
		}
		efforts, err := m.WorkoutStretchEfforts(r.Context(), int32(id), userToken)
		if models.IsNotFound(err) {
			notFoundHandler(w, r)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, efforts)
	}
}

// getRouteStretchesFunc lists the stretches a route's runs pass, with the runs that missed each.
func getRouteStretchesFunc(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		id, ok := routeID(r)
		if !ok {
			notFoundHandler(w, r)
			return
		}
		window, err := parseWindow(r.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		stretches, err := m.RouteStretches(r.Context(), id, window, userToken)
		if models.IsNotFound(err) {
			notFoundHandler(w, r)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, stretches)
	}
}
