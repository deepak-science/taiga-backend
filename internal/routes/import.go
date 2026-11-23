package routes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/backup"
	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

// maxImportBytes bounds an import's body, which is streamed and never held whole.
const maxImportBytes = 2 << 30

// maxImportFileBytes bounds one file in it: uploads are 32MB, and the largest real activity file is a few.
const maxImportFileBytes = 64 << 20

// maxImportDocumentBytes bounds its documents together, which are decoded whole and kept: an export of five years' runs has 1.4MB.
const maxImportDocumentBytes = 32 << 20

var errImportFileTooLarge = fmt.Errorf("an imported file is at most %d bytes", maxImportFileBytes)

// importWindow is how long importing may take once the archive is read: every workout new to the account is made from its file before the answer.
const importWindow = 30 * time.Minute

// postImport reads a user's export (docs/backup.md), sent as the request body, into the signed-in account, and answers with what it did.
// One import runs at a time: two at once would each add a hand-entered workout the other hadn't yet.
func postImport(m models.Model, fileRepo filerepo.FileRepo) http.HandlerFunc {
	var running sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		userToken, err := tokens.UserTokenFromContext(r.Context())
		if err != nil {
			unauthorizedHandler(w, r)
			return
		}
		if !running.TryLock() {
			conflictError(w, errors.New("an import is already running; try again once it's done"))
			return
		}
		defer running.Unlock()

		ctx := r.Context()
		rc := http.NewResponseController(w)
		body := &deadlineReader{r: http.MaxBytesReader(w, r.Body, maxImportBytes), rc: rc}
		// The files go into the repo as they're read; they're content-addressed, so an archive turned away later leaves nothing wrong behind there.
		a, err := backup.ReadWithin(body, func(hash string, content io.Reader) error {
			// The repo holds a file in memory to store it, so one is capped well under the body's limit.
			capped := &io.LimitedReader{R: content, N: maxImportFileBytes + 1}
			stored, err := fileRepo.Store(ctx, capped)
			if capped.N == 0 {
				return fmt.Errorf("%w: file %s", errImportFileTooLarge, hash)
			}
			if err != nil {
				return fmt.Errorf("storing file %s: %w", hash, err)
			}
			if stored != hash {
				// The repo names a file by what it read, so this is the archive's bytes not matching their name.
				return fmt.Errorf("%w: file %s doesn't match its hash", backup.ErrInvalid, hash)
			}
			return nil
		}, maxImportDocumentBytes)
		// The server's write timeout runs from the request's start, so a long upload would leave no time for any answer, an error's included.
		_ = rc.SetWriteDeadline(time.Now().Add(importWindow))
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			http.Error(w, fmt.Sprintf("an import is at most %d bytes", maxImportBytes), http.StatusRequestEntityTooLarge)
			return
		case errors.Is(err, errImportFileTooLarge):
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		case errors.Is(err, backup.ErrInvalid):
			unprocessableError(w, err)
			return
		case err != nil:
			serverError(w, err)
			return
		}
		if a.Manifest.Kind != backup.KindUser {
			unprocessableError(w, errors.New("that's a deployment's backup, not one user's export; restore it into an empty deployment instead"))
			return
		}

		// The work stops with the answer's deadline, rather than running on, holding the lock, for an answer no one gets; what it did stays, and importing again finishes it.
		ctx, cancel := context.WithTimeout(ctx, importWindow)
		defer cancel()
		report, err := m.ImportAccount(ctx, &a.Data, fileRepo, time.Now(), userToken)
		switch {
		// A document whose JSON reads but whose content doesn't, such as a stretch's points.
		case errors.Is(err, backup.ErrInvalid):
			unprocessableError(w, err)
			return
		case err != nil:
			serverError(w, err)
			return
		}
		writeJSON(w, report)
	}
}

// deadlineReader gives each read its own deadline, so a stalled client is cut off but a long upload isn't.
type deadlineReader struct {
	r  io.Reader
	rc *http.ResponseController
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	// Not every ResponseWriter can set one (a test's recorder can't); it then reads without.
	_ = d.rc.SetReadDeadline(time.Now().Add(exportWriteWindow))
	return d.r.Read(p)
}
