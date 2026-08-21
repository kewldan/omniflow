package customerauthpg

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newUndialledPool returns a pool that is never connected.
//
// pgxpool defers dialling until a query needs a connection, and the unit tests
// in this package exercise only the paths that never issue one — the bot API
// cache, the rotation bookkeeping — so a port nothing listens on is the honest
// stand-in: a test that accidentally reaches the database fails loudly rather
// than passing against a mock.
func newUndialledPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://omniflow:omniflow@127.0.0.1:1/omniflow")
	if err != nil {
		t.Fatalf("build pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
