package routes

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"github.com/go-chi/chi/v5"
)

// Signing in from a CLI (ROADMAP M14): the CLI starts a sign-in and polls, a signed-in user approves its code from the frontend's #/device page, and the session it gets is kept alive with a refresh token.

// mountDeviceSignIn adds the endpoints a CLI calls with no token yet.
func mountDeviceSignIn(router chi.Router, m models.Model, tok tokens.Toker, o options) {
	router.Post("/device", startDevice(m, o))
	router.Post("/device/token", pollDevice(m, tok))
	router.Post("/tokens/refresh", refreshToken(m, tok))
	router.Post("/tokens/revoke", revokeToken(m))
}

// mountDeviceApproval adds the endpoints the signed-in user's browser calls.
func mountDeviceApproval(router chi.Router, m models.Model) {
	router.Get("/device/{usercode}", getDeviceRequest(m))
	router.Post("/device/approve", decideDevice(m, true))
	router.Post("/device/deny", decideDevice(m, false))
	router.Get("/sessions", getSessions(m))
	router.Delete("/sessions/{sessionid}", deleteSession(m))
}

// decodeSmall reads a small JSON body; these endpoints take a field or two.
func decodeSmall(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		badRequestError(w, err)
		return false
	}
	return true
}

// writeSecretJSON is writeJSON for a response carrying a token, which nothing should keep a copy of.
func writeSecretJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, v)
}

// oauthError answers as OAuth's token endpoints do, so a client can switch on the code: {"error": "authorization_pending"}.
func oauthError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

type startDeviceRequest struct {
	// ClientName says what's asking, e.g. the machine's hostname; the approval page shows it.
	ClientName string `json:"client_name"`
}

type startDeviceResponse struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	// Where the user approves it; the complete one carries the code, and still asks before approving.
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func startDevice(m models.Model, o options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req startDeviceRequest
		// A CLI may send no body at all.
		if r.ContentLength != 0 && !decodeSmall(w, r, &req) {
			return
		}
		start, err := m.StartDeviceAuthorization(r.Context(), req.ClientName, time.Now())
		if errors.Is(err, models.ErrTooManyDevices) {
			http.Error(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeSecretJSON(w, startDeviceResponse{
			DeviceCode:              start.DeviceCode,
			UserCode:                start.UserCode,
			VerificationURI:         o.oidc.FrontendURL("#/device"),
			VerificationURIComplete: o.oidc.FrontendURL("#/device?" + url.Values{"code": {start.UserCode}}.Encode()),
			ExpiresIn:               int(models.DeviceCodeTTL.Seconds()),
			Interval:                int(models.DevicePollInterval.Seconds()),
		})
	}
}

type pollDeviceRequest struct {
	DeviceCode string `json:"device_code"`
}

// cliTokens is what a CLI signs in with: the access token every request carries, and for a new session the refresh token that buys the next one.
type cliTokens struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in"`
}

// deviceErrors are the answers a poll gives until there's a grant.
var deviceErrors = []error{models.ErrAuthorizationPending, models.ErrSlowDown, models.ErrExpiredToken, models.ErrAccessDenied, models.ErrInvalidGrant}

func pollDevice(m models.Model, tok tokens.Toker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pollDeviceRequest
		if !decodeSmall(w, r, &req) {
			return
		}
		grant, err := m.ExchangeDeviceCode(r.Context(), req.DeviceCode, time.Now())
		for _, known := range deviceErrors {
			if errors.Is(err, known) {
				oauthError(w, known.Error())
				return
			}
		}
		if err != nil {
			serverError(w, err)
			return
		}
		token, err := tok.EncodeUser(&tokens.UserToken{ID: grant.User.ID, Email: grant.User.Email})
		if err != nil {
			serverError(w, err)
			return
		}
		log.Printf("Signed a CLI in as user %d", grant.User.ID)
		writeSecretJSON(w, cliTokens{Token: token, RefreshToken: grant.RefreshToken, ExpiresIn: int(tokens.Lifetime.Seconds())})
	}
}

type refreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func refreshToken(m models.Model, tok tokens.Toker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req refreshTokenRequest
		if !decodeSmall(w, r, &req) {
			return
		}
		user, err := m.RefreshCLISession(r.Context(), req.RefreshToken, time.Now())
		if errors.Is(err, models.ErrInvalidGrant) {
			oauthError(w, models.ErrInvalidGrant.Error())
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		token, err := tok.EncodeUser(&tokens.UserToken{ID: user.ID, Email: user.Email})
		if err != nil {
			serverError(w, err)
			return
		}
		writeSecretJSON(w, cliTokens{Token: token, ExpiresIn: int(tokens.Lifetime.Seconds())})
	}
}

// revokeToken is a CLI signing out: holding the refresh token is all it takes, and an unknown one is already signed out.
func revokeToken(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req refreshTokenRequest
		if !decodeSmall(w, r, &req) {
			return
		}
		if err := m.RevokeCLISessionByToken(r.Context(), req.RefreshToken); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func getDeviceRequest(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := tokens.UserTokenFromContext(r.Context()); err != nil {
			unauthorizedHandler(w, r)
			return
		}
		req, err := m.DeviceRequest(r.Context(), chi.URLParam(r, "usercode"), time.Now())
		if models.IsNotFound(err) {
			notFoundHandler(w, r)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, req)
	}
}

type decideDeviceRequest struct {
	UserCode string `json:"user_code"`
}

// decideDevice approves the waiting CLI as the signed-in user, or turns it away; 404 once the code is decided or has run out.
func decideDevice(m models.Model, approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		var req decideDeviceRequest
		if !decodeSmall(w, r, &req) {
			return
		}
		if approve {
			err = m.ApproveDevice(r.Context(), req.UserCode, time.Now(), userToken)
		} else {
			err = m.DenyDevice(r.Context(), req.UserCode, time.Now())
		}
		if models.IsNotFound(err) {
			notFoundHandler(w, r)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if approve {
			log.Printf("User %d approved a CLI sign-in", userToken.ID)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func getSessions(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		sessions, err := m.CLISessions(r.Context(), userToken)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, sessions)
	}
}

func deleteSession(m models.Model) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "sessionid"), 10, 32)
		if err != nil {
			notFoundHandler(w, r)
			return
		}
		err = m.RevokeCLISession(r.Context(), int32(id), userToken)
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
