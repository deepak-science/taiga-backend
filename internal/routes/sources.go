package routes

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"gitea.deepak.science/deepak/taiga/internal/coros"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/sources"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

type sourceKind struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type sourceConnectionResponse struct {
	ID        int32     `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// Null until the first sync finishes.
	LastSyncedAt *time.Time `json:"last_synced_at"`
	// Why the last sync failed; empty when it didn't.
	LastError      string `json:"last_error"`
	NeedsReconnect bool   `json:"needs_reconnect"`
	// How many of the source's activities have been dealt with: imported, or found to be files the user already had.
	Activities int64 `json:"activities"`
}

type sourcesResponse struct {
	// What can be connected; empty when sources are off.
	Kinds       []sourceKind               `json:"kinds"`
	Connections []sourceConnectionResponse `json:"connections"`
}

func sourceNames(kind string) string {
	if kind == coros.Kind {
		return "COROS"
	}
	return kind
}

func getSources(m models.Model, g *sources.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		conns, err := m.SourceConnections(r.Context(), userToken)
		if err != nil {
			serverError(w, err)
			return
		}
		out := sourcesResponse{Kinds: []sourceKind{}, Connections: []sourceConnectionResponse{}}
		if g != nil {
			out.Kinds = append(out.Kinds, sourceKind{Kind: coros.Kind, Name: sourceNames(coros.Kind)})
		}
		for _, c := range conns {
			resp := sourceConnectionResponse{
				ID:             c.ID,
				Kind:           c.Kind,
				Name:           sourceNames(c.Kind),
				CreatedAt:      c.CreatedAt.Time,
				LastError:      c.LastError,
				NeedsReconnect: c.NeedsReconnect,
				Activities:     c.Activities,
			}
			if c.LastSyncedAt.Valid {
				resp.LastSyncedAt = &c.LastSyncedAt.Time
			}
			out.Connections = append(out.Connections, resp)
		}
		writeJSON(w, out)
	}
}

// connectSource starts connecting the signed-in user's account at the source; the frontend sends the browser to the returned URL.
// It can't redirect itself: a fetch carrying the bearer token doesn't navigate.
func connectSource(g *sources.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		if g == nil || chi.URLParam(r, "source") != coros.Kind {
			notFoundHandler(w, r)
			return
		}
		address, err := g.Begin(r.Context(), w, userToken)
		if err != nil {
			log.Printf("Could not start connecting user %d to COROS: %v", userToken.ID, err)
			http.Error(w, "COROS could not be reached to start connecting; try again later", http.StatusBadGateway)
			return
		}
		writeJSON(w, linkResponse{URL: address})
	}
}

// sourceCallback finishes connecting and sends the browser back to the frontend's #/account, with source=coros or source_error=why.
func sourceCallback(g *sources.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if g == nil || chi.URLParam(r, "source") != coros.Kind {
			notFoundHandler(w, r)
			return
		}
		params := url.Values{"source": {coros.Kind}}
		if err := g.Finish(r.Context(), w, r); err != nil {
			log.Printf("Connecting to COROS failed: %v", err)
			params = url.Values{"source_error": {sourceMessage(err)}}
		}
		http.Redirect(w, r, g.FrontendURL("#/account?"+params.Encode()), http.StatusFound)
	}
}

// sourceMessage is what to tell the user; anything unexpected stays in the log.
func sourceMessage(err error) string {
	var pe *sources.ProviderError
	switch {
	case errors.Is(err, sources.ErrFlow):
		return err.Error()
	case errors.As(err, &pe) && pe.Code == "access_denied":
		return "COROS wasn't connected: access was declined there"
	default:
		return "connecting to COROS failed"
	}
}

func sourceConnectionID(r *http.Request) (int32, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "source"), 10, 32)
	return int32(id), err == nil
}

func disconnectSource(m models.Model, g *sources.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		id, ok := sourceConnectionID(r)
		if !ok {
			notFoundHandler(w, r)
			return
		}
		// With sources turned off there's no one to tell at COROS, but a connection made before can still be forgotten.
		if g != nil {
			err = g.Disconnect(r.Context(), id, userToken)
		} else {
			err = m.DisconnectSource(r.Context(), id, userToken)
		}
		switch {
		case models.IsNotFound(err):
			notFoundHandler(w, r)
		case err != nil:
			serverError(w, err)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

var errNeedsReconnect = errors.New("the source turned this connection away; connect it again")

// syncSource queues a sync now rather than at the next sweep.
func syncSource(m models.Model, g *sources.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		id, ok := sourceConnectionID(r)
		if !ok || g == nil {
			notFoundHandler(w, r)
			return
		}
		conn, err := m.SourceConnection(r.Context(), id, userToken)
		switch {
		case models.IsNotFound(err):
			notFoundHandler(w, r)
			return
		case err != nil:
			serverError(w, err)
			return
		case conn.NeedsReconnect:
			conflictError(w, errNeedsReconnect)
			return
		}
		if err := m.EnqueueSyncSource(r.Context(), time.Now(), userToken); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}
