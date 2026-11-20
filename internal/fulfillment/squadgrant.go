package fulfillment

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

var (
	// ErrSquadGrantUnavailable refuses temporary squad access for a
	// subscription with no live, running entitlement to add it to.
	ErrSquadGrantUnavailable = errors.New("squad_access_unavailable")
	// ErrSquadGrantInvalid refuses a grant with no squad, or hours outside
	// 1–720.
	ErrSquadGrantInvalid = errors.New("squad_access_invalid")
)

// MaxSquadGrantHours is the longest temporary squad access, thirty days.
const MaxSquadGrantHours = 720

// SquadGrantInput is one temporary squad access to grant.
type SquadGrantInput struct {
	CustomerID     pgtype.UUID
	SubscriptionID pgtype.UUID
	SquadID        string
	Hours          int
	// Source is "operator" or "promo"; GrantedBy is the operator and
	// OrderID the promo code's zero-amount order.
	Source         string
	GrantedBy      pgtype.UUID
	OrderID        pgtype.UUID
	Reason         string
	IdempotencyKey string
}

// InsertJob queues the job of one fulfillment operation inside a transaction.
type InsertJob func(ctx context.Context, tx pgx.Tx, operationID string) error

// GrantSquadAccessTx grants temporary access to one squad inside the
// caller's transaction: the grant row, a `grant_squad` operation that pushes
// the subscription's squads with it added, and a `revoke_squad` operation
// scheduled for when the hours are up. A repeated key returns the grant it
// made the first time.
func GrantSquadAccessTx(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, insert InsertJob, now time.Time, input SquadGrantInput) (dbgen.SquadAccessGrant, error) {
	squad, err := parseUUID(input.SquadID)
	if err != nil || input.Hours < 1 || input.Hours > MaxSquadGrantHours || input.IdempotencyKey == "" {
		return dbgen.SquadAccessGrant{}, ErrSquadGrantInvalid
	}
	if existing, getErr := queries.GetSquadAccessGrantByKey(ctx, input.IdempotencyKey); getErr == nil {
		if existing.SubscriptionID != input.SubscriptionID || existing.SquadID != squad {
			return dbgen.SquadAccessGrant{}, errors.New("idempotency key was already used with a different grant")
		}
		return existing, nil
	} else if !errors.Is(getErr, pgx.ErrNoRows) {
		return dbgen.SquadAccessGrant{}, getErr
	}
	entitlement, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: input.CustomerID, SubscriptionID: input.SubscriptionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.SquadAccessGrant{}, ErrSquadGrantUnavailable
	}
	if err != nil {
		return dbgen.SquadAccessGrant{}, err
	}
	if entitlement.Status == "paused" || entitlement.Status == "pending" || !commerce.EntitlementLive(entitlement.Status, entitlement.EndsAt.Time, now) {
		return dbgen.SquadAccessGrant{}, ErrSquadGrantUnavailable
	}
	reason := pgtype.Text{String: input.Reason, Valid: input.Reason != ""}
	grant, err := queries.InsertSquadAccessGrant(ctx, dbgen.InsertSquadAccessGrantParams{
		UserID: input.CustomerID, SubscriptionID: input.SubscriptionID, SquadID: squad, Source: input.Source,
		GrantedBy: input.GrantedBy, OrderID: input.OrderID, Reason: reason,
		IdempotencyKey: input.IdempotencyKey, Hours: int32(input.Hours),
	})
	if err != nil {
		return dbgen.SquadAccessGrant{}, err
	}
	key := "squad-grant:" + uuidString(grant.ID)
	for _, step := range []struct {
		operation, suffix string
		effectiveAt       time.Time
	}{
		{"grant_squad", ":grant", now},
		{"revoke_squad", ":revoke", grant.EndsAt.Time},
	} {
		if err = queueGrantOperation(ctx, tx, queries, insert, entitlement, step.operation, key+step.suffix, step.effectiveAt, grant.ID); err != nil {
			return dbgen.SquadAccessGrant{}, err
		}
	}
	return grant, nil
}

// queueGrantOperation writes one operation that pushes an entitlement's own
// state, which the claim then overlays with the subscription's temporary
// squads.
func queueGrantOperation(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, insert InsertJob, entitlement dbgen.Entitlement, operation, key string, effectiveAt time.Time, grantID pgtype.UUID) error {
	desired, err := json.Marshal(map[string]any{
		"effectiveAt": effectiveAt.UTC(), "endsAt": entitlement.EndsAt.Time,
		"trafficAllowanceBytes": nullableInt8(entitlement.TrafficAllowanceBytes),
		"deviceLimit":           nullableInt4(entitlement.DeviceLimit),
		"squadIds":              databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds),
		"grantId":               uuidString(grantID),
	})
	if err != nil {
		return err
	}
	created, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
		EntitlementID: entitlement.ID, Operation: operation, IdempotencyKey: key,
		CorrelationID: "squad-grant:" + uuidString(grantID), DesiredState: desired,
	})
	if err != nil {
		return err
	}
	if insert == nil {
		return nil
	}
	return insert(ctx, tx, uuidString(created.ID))
}

// endScheduledGrant ends the grant a `revoke_squad` operation was scheduled
// to end, once its hours are up. A grant an operator revoked is already over.
func endScheduledGrant(ctx context.Context, queries *dbgen.Queries, desired desiredState, now time.Time) error {
	if desired.GrantID == "" {
		return nil
	}
	id, err := parseUUID(desired.GrantID)
	if err != nil {
		return nil
	}
	grant, err := queries.GetSquadAccessGrant(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if grant.Status == "active" && !now.Before(grant.EndsAt.Time) {
		return queries.EndSquadAccessGrant(ctx, id)
	}
	return nil
}

// withSquadGrants adds the subscription's temporary squads to the squads an
// operation pushes, so a reconcile keeps them while their hours last.
func withSquadGrants(ctx context.Context, queries *dbgen.Queries, subscriptionID pgtype.UUID, desired desiredState) (desiredState, error) {
	if !subscriptionID.Valid {
		return desired, nil
	}
	grants, err := queries.ListActiveSquadGrants(ctx, subscriptionID)
	if err != nil || len(grants) == 0 {
		return desired, err
	}
	squads := slices.Clone(desired.SquadIDs)
	for _, grant := range grants {
		squad := uuidString(grant.SquadID)
		if !slices.Contains(squads, squad) {
			squads = append(squads, squad)
		}
	}
	slices.Sort(squads)
	desired.SquadIDs = squads
	return desired, nil
}

// GrantSquadAccess grants temporary access to one squad in its own
// transaction, for an operator.
func (service *Service) GrantSquadAccess(ctx context.Context, input SquadGrantInput) (dbgen.SquadAccessGrant, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.SquadAccessGrant{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	grant, err := GrantSquadAccessTx(ctx, tx, dbgen.New(tx), service.insertJob, service.clock().UTC(), input)
	if err != nil {
		return dbgen.SquadAccessGrant{}, err
	}
	return grant, tx.Commit(ctx)
}

// RevokeSquadAccess ends a grant before its hours are up and pushes the
// subscription's squads without it.
func (service *Service) RevokeSquadAccess(ctx context.Context, customerID, subscriptionID, grantID pgtype.UUID) (dbgen.SquadAccessGrant, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.SquadAccessGrant{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	grant, err := queries.RevokeSquadAccessGrant(ctx, dbgen.RevokeSquadAccessGrantParams{GrantID: grantID, SubscriptionID: subscriptionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.SquadAccessGrant{}, ErrSquadGrantUnavailable
	}
	if err != nil {
		return dbgen.SquadAccessGrant{}, err
	}
	if err = queueRevokeOnCurrent(ctx, tx, queries, service.insertJob, service.clock().UTC(), grant, ":revoked"); err != nil {
		return dbgen.SquadAccessGrant{}, err
	}
	return grant, tx.Commit(ctx)
}

// queueRevokeOnCurrent pushes a subscription's squads without an ended grant,
// against whatever entitlement the subscription holds now.
func queueRevokeOnCurrent(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, insert InsertJob, now time.Time, grant dbgen.SquadAccessGrant, suffix string) error {
	entitlement, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: grant.UserID, SubscriptionID: grant.SubscriptionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return queueGrantOperation(ctx, tx, queries, insert, entitlement, "revoke_squad",
		"squad-grant:"+uuidString(grant.ID)+suffix+":"+uuidString(entitlement.ID), now, grant.ID)
}

// SquadGrants lists a subscription's recent temporary squad grants.
func (service *Service) SquadGrants(ctx context.Context, subscriptionID pgtype.UUID) ([]dbgen.SquadAccessGrant, error) {
	return dbgen.New(service.pool).ListSquadGrantsForSubscription(ctx, subscriptionID)
}

func (service *Service) insertJob(ctx context.Context, tx pgx.Tx, operationID string) error {
	if service.client == nil {
		return nil
	}
	_, err := service.client.InsertTx(ctx, tx, JobArgs{OperationID: operationID}, InsertOpts())
	return err
}

// endExpiredGrants is the backstop for a grant whose scheduled revoke was
// cancelled because a renewal retired the entitlement it was queued for: the
// grant is ended and the subscription's current entitlement is pushed
// without it.
func (scheduler *Scheduler) endExpiredGrants(ctx context.Context) error {
	tx, err := scheduler.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	ended, err := queries.EndExpiredSquadGrants(ctx)
	if err != nil {
		return err
	}
	insert := func(ctx context.Context, tx pgx.Tx, operationID string) error {
		_, insertErr := scheduler.client.InsertTx(ctx, tx, JobArgs{OperationID: operationID}, InsertOpts())
		return insertErr
	}
	for _, grant := range ended {
		if err = queueRevokeOnCurrent(ctx, tx, queries, insert, scheduler.clock().UTC(), grant, ":expired"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
