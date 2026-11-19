package fulfillment

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// recordingProvisioner remembers every update it was asked to make.
type recordingProvisioner struct {
	updates []remnawave.ProvisionUser
	created int
}

func (provisioner *recordingProvisioner) User(_ context.Context, id int64) (remnawave.User, error) {
	return remnawave.User{ID: id, Status: "ACTIVE"}, nil
}

func (provisioner *recordingProvisioner) UserByUsername(context.Context, string) (remnawave.User, error) {
	return remnawave.User{}, remnawave.ErrNotFound
}

func (provisioner *recordingProvisioner) CreateUser(_ context.Context, desired remnawave.ProvisionUser) (remnawave.User, error) {
	provisioner.created++
	return remnawave.User{ID: 99, Username: desired.Username}, nil
}

func (provisioner *recordingProvisioner) UpdateUser(_ context.Context, id int64, desired remnawave.ProvisionUser) (remnawave.User, error) {
	provisioner.updates = append(provisioner.updates, desired)
	return remnawave.User{ID: id, Username: desired.Username, Status: "ACTIVE"}, nil
}

func (provisioner *recordingProvisioner) EnableUser(context.Context, int64) error       { return nil }
func (provisioner *recordingProvisioner) DisableUser(context.Context, int64) error      { return nil }
func (provisioner *recordingProvisioner) ResetUserTraffic(context.Context, int64) error { return nil }

func adoptedClaim(storedUsername string) claim {
	subscriptionID := pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, Valid: true}
	return claim{
		operation:   dbgen.FulfillmentOperation{Operation: "reconcile"},
		entitlement: dbgen.Entitlement{UserID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}, Status: "active"},
		subscription: dbgen.Subscription{
			ID: subscriptionID, Slot: 1, RemnawaveUserID: pgtype.Int8{Int64: 701, Valid: true},
			RemnawaveUsername: pgtype.Text{String: storedUsername, Valid: storedUsername != ""},
		},
		desired:  desiredState{EndsAt: time.Now().Add(24 * time.Hour), remoteExpireAt: time.Now().Add(24 * time.Hour)},
		remoteID: 701,
	}
}

// TestAdoptedRemoteUserIsPatchedUnderItsOwnName pins the rule that keeps a
// carried-over subscription on the Remnawave user — and the access link — it
// already has: a known user is addressed by the name it has, not by the
// omniflow_… name Omniflow would have given it.
func TestAdoptedRemoteUserIsPatchedUnderItsOwnName(t *testing.T) {
	provisioner := &recordingProvisioner{}
	worker := NewWorker(nil, provisioner)
	if _, err := worker.apply(context.Background(), adoptedClaim("bedolaga_5001")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(provisioner.updates) != 1 || provisioner.updates[0].Username != "bedolaga_5001" || provisioner.created != 0 {
		t.Fatalf("the adopted user must be patched under its own name: %+v, created %d", provisioner.updates, provisioner.created)
	}

	native := &recordingProvisioner{}
	if _, err := NewWorker(nil, native).apply(context.Background(), adoptedClaim("")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(native.updates) != 1 || native.updates[0].Username != remnawaveUsername(adoptedClaim("").subscription.ID) {
		t.Fatalf("with no stored name the deterministic one is used: %+v", native.updates)
	}
}

func (provisioner *recordingProvisioner) DeleteAllDevices(context.Context, int64) error { return nil }
