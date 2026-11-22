package store_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/config"
	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/store"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
)

func newSqliteStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.GetStore(&config.Config{Db: config.DBConfig{
		Driver:        "sqlite",
		FilePath:      filepath.Join(t.TempDir(), "test.db"),
		MigrationPath: filepath.Join(testsupport.RepoRoot(), "migrations-sqlite"),
		AutoMigrateUp: true,
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestInTxCommitsOrRollsBack(t *testing.T) {
	ctx := context.Background()
	s := newSqliteStore(t)
	boom := errors.New("boom")

	err := s.InTx(ctx, func(q db.Querier) error {
		if _, err := q.CreateUser(ctx, &db.CreateUserParams{Email: "rolled@example.com", DisplayName: "r"}); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)

	require.NoError(t, s.InTx(ctx, func(q db.Querier) error {
		_, err := q.CreateUser(ctx, &db.CreateUserParams{Email: "kept@example.com", DisplayName: "k"})
		return err
	}))

	q, err := s.GetQuerier()
	require.NoError(t, err)
	_, err = q.SelectEmailPasswordForAuth(ctx, "rolled@example.com")
	assert.Error(t, err, "the failed transaction's insert was rolled back")
	_, err = q.SelectEmailPasswordForAuth(ctx, "kept@example.com")
	assert.NoError(t, err)
}

func TestGetStoreRejectsWhatItCannotOpen(t *testing.T) {
	_, err := store.GetStore(&config.Config{Db: config.DBConfig{Driver: "oracle"}})
	assert.ErrorContains(t, err, "oracle")

	_, err = store.GetStore(&config.Config{Db: config.DBConfig{Driver: "sqlite"}})
	assert.Error(t, err, "sqlite needs a file path")

	_, err = store.GetStore(&config.Config{Db: config.DBConfig{
		Driver:        "sqlite",
		FilePath:      filepath.Join(t.TempDir(), "test.db"),
		MigrationPath: filepath.Join(t.TempDir(), "no-migrations-here"),
		AutoMigrateUp: true,
	}})
	assert.Error(t, err, "a store whose migrations can't run isn't handed out")
}

func TestHealthyUntilClosed(t *testing.T) {
	ctx := context.Background()
	s := newSqliteStore(t)
	require.NoError(t, s.Healthy(ctx))

	require.NoError(t, s.Close())
	assert.Error(t, s.Healthy(ctx))
}

func TestMigratedDataOutlivesTheStore(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{Db: config.DBConfig{
		Driver:        "sqlite",
		FilePath:      filepath.Join(t.TempDir(), "test.db"),
		MigrationPath: filepath.Join(testsupport.RepoRoot(), "migrations-sqlite"),
		AutoMigrateUp: true,
	}}

	first, err := store.GetStore(cfg)
	require.NoError(t, err)
	q, err := first.GetQuerier()
	require.NoError(t, err)
	_, err = q.CreateUser(ctx, &db.CreateUserParams{Email: "kept@example.com", DisplayName: "k"})
	require.NoError(t, err)
	require.NoError(t, first.Close())

	// Opening again migrates again, which has to leave an up-to-date database alone.
	second, err := store.GetStore(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	q, err = second.GetQuerier()
	require.NoError(t, err)
	_, err = q.SelectEmailPasswordForAuth(ctx, "kept@example.com")
	assert.NoError(t, err)
}

func TestConcurrentTransactionsWaitTheirTurn(t *testing.T) {
	ctx := context.Background()
	s := newSqliteStore(t)

	// Each reads, then writes: the shape that gets SQLITE_BUSY straight away unless transactions take the write lock up front.
	const writers = 16
	errs := make(chan error, writers)
	for i := range writers {
		go func() {
			errs <- s.InTx(ctx, func(q db.Querier) error {
				if _, err := q.SelectEmailPasswordForAuth(ctx, "nobody@example.com"); err == nil {
					return errors.New("found a user nobody made")
				}
				_, err := q.CreateUser(ctx, &db.CreateUserParams{Email: fmt.Sprintf("user%d@example.com", i), DisplayName: "u"})
				return err
			})
		}()
	}
	for range writers {
		assert.NoError(t, <-errs)
	}

	q, err := s.GetQuerier()
	require.NoError(t, err)
	for i := range writers {
		_, err := q.SelectEmailPasswordForAuth(ctx, fmt.Sprintf("user%d@example.com", i))
		assert.NoError(t, err)
	}
}

func TestErrorStoreFailsEverything(t *testing.T) {
	ctx := context.Background()
	s := store.NewErrorStore()

	assert.Error(t, s.Healthy(ctx))
	_, err := s.GetQuerier()
	assert.Error(t, err)
	assert.Error(t, s.InTx(ctx, func(db.Querier) error { return nil }))
	assert.NoError(t, s.Close())
}
