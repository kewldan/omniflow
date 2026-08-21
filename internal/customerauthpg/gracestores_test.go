package customerauthpg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// failingGraceStore stands in for a Valkey that cannot be reached.
type failingGraceStore struct{}

func (failingGraceStore) Claim(context.Context, string, string, time.Duration) (bool, error) {
	return false, errors.New("valkey is unreachable")
}

func (failingGraceStore) Lookup(context.Context, string) (string, bool, error) {
	return "", false, errors.New("valkey is unreachable")
}

func (failingGraceStore) Forget(context.Context, string) error {
	return errors.New("valkey is unreachable")
}

// alreadyClaimedGraceStore stands in for a store another request has already
// won the claim on, and that holds no entry for any lookup.
type alreadyClaimedGraceStore struct{}

func (alreadyClaimedGraceStore) Claim(context.Context, string, string, time.Duration) (bool, error) {
	return false, nil
}

func (alreadyClaimedGraceStore) Lookup(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (alreadyClaimedGraceStore) Forget(context.Context, string) error { return nil }

// sessionTokenRow is a live session row as the token lookup returns it.
func sessionTokenRow() dbgen.GetCustomerSessionByTokenRow {
	var id pgtype.UUID
	_ = id.Scan("2f1c0c2e-0000-4000-8000-000000000000")
	return dbgen.GetCustomerSessionByTokenRow{ID: id, UserID: id, UserStatus: "active"}
}
