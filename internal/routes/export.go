package routes

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/backup"
	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

// exportWriteWindow is how long one write to the client may take; the server's own write timeout would cut a long export off.
const exportWriteWindow = 2 * time.Minute

// getExport streams the user's backup as a tar (docs/backup.md); ?fetched=false leaves out weather and COROS's figures.
func getExport(m models.Model, fileRepo filerepo.FileRepo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		fetched := true
		if v := r.URL.Query().Get("fetched"); v != "" {
			if fetched, err = strconv.ParseBool(v); err != nil {
				badRequestError(w, fmt.Errorf("fetched must be true or false"))
				return
			}
		}
		now := time.Now()
		a, err := m.Export(r.Context(), fetched, now, userToken)
		if err != nil {
			serverError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="taiga-%s.tar"`, now.UTC().Format("2006-01-02")))
		w.WriteHeader(http.StatusOK)
		out := &deadlineWriter{w: w, rc: http.NewResponseController(w)}
		// The router's timeout skips /export, so the request's context ends only when the client goes, which stops a read from the repo too.
		if err := backup.Write(r.Context(), out, a, fileRepo.Fetch); err != nil {
			log.Printf("export for user %d stopped: %v", userToken.ID, err)
			// The status is already sent, so cutting the connection is the only way left to say it failed.
			panic(http.ErrAbortHandler)
		}
	}
}

// deadlineWriter gives each write its own deadline, so a slow client is cut off but a long export isn't.
type deadlineWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	// Not every ResponseWriter can set one (a test's recorder can't); it then writes without.
	_ = d.rc.SetWriteDeadline(time.Now().Add(exportWriteWindow))
	return d.w.Write(p)
}
