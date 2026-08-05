package eventlog

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultDSN matches deploy/docker-compose.yaml's postgres service
// (single shared instance, factory_projection is the projection store's
// own database within it — see deploy/postgres-init/01-projection-store.sql).
const defaultDSN = "postgres://temporal:temporal@localhost:5432/factory_projection?sslmode=disable"

// NewPool connects to the projection store, using PROJECTION_STORE_DSN if
// set or the default matching the local docker-compose setup otherwise.
func NewPool(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := os.Getenv("PROJECTION_STORE_DSN")
	if dsn == "" {
		dsn = defaultDSN
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("eventlog: connect to projection store: %w", err)
	}
	return pool, nil
}
