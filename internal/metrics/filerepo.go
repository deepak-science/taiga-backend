package metrics

import (
	"context"
	"io"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/filerepo"
)

// FileRepo times every operation on repo under backend ("local" or "s3").
func FileRepo(repo filerepo.FileRepo, backend string) filerepo.FileRepo {
	return &timedRepo{repo: repo, backend: backend}
}

type timedRepo struct {
	repo    filerepo.FileRepo
	backend string
}

func (t *timedRepo) observe(op string, start time.Time, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	fileRepo.WithLabelValues(t.backend, op, outcome).Observe(time.Since(start).Seconds())
}

func (t *timedRepo) Store(ctx context.Context, r io.Reader) (string, error) {
	start := time.Now()
	hash, err := t.repo.Store(ctx, r)
	t.observe("store", start, err)
	return hash, err
}

func (t *timedRepo) Exists(ctx context.Context, hash string) (bool, error) {
	start := time.Now()
	ok, err := t.repo.Exists(ctx, hash)
	t.observe("exists", start, err)
	return ok, err
}

func (t *timedRepo) Fetch(ctx context.Context, hash string) (io.ReadCloser, error) {
	start := time.Now()
	rc, err := t.repo.Fetch(ctx, hash)
	t.observe("fetch", start, err)
	return rc, err
}
