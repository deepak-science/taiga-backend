package store

import (
	"context"
	"fmt"
)

// golang-migrate's own table, the same on both databases; sqlc never sees it, since it isn't in the migrations.
const schemaVersionQuery = `SELECT version, dirty FROM schema_migrations LIMIT 1`

func (s *pgStore) SchemaVersion(ctx context.Context) (version int64, dirty bool, err error) {
	err = s.pgPool.QueryRow(ctx, schemaVersionQuery).Scan(&version, &dirty)
	return version, dirty, err
}

func (s *sqliteStore) SchemaVersion(ctx context.Context) (version int64, dirty bool, err error) {
	err = s.db.QueryRowContext(ctx, schemaVersionQuery).Scan(&version, &dirty)
	return version, dirty, err
}

func (m *errorStore) SchemaVersion(ctx context.Context) (int64, bool, error) {
	return 0, false, fmt.Errorf("error store can't actually query")
}
