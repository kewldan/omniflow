package fulfillment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/riverqueue/river"
)

// A fulfillment run is three steps, and only the middle one talks to
// Remnawave.
//
//  1. claim: a short transaction locks the operation, decides whether it should
//     run at all, and marks it `running` with a lease. Everything the
//     Remnawave calls need is read here.
//  2. call: the Remnawave requests, outside any transaction. They are
//     idempotent by construction — the username is derived from the
//     subscription and looked up before anything is created — so repeating
//     them after a crash is safe.
//  3. record: a second short transaction writes the result, but only if the
//     claim is still this run's. A run whose lease lapsed and was reclaimed by
//     another drops its result rather than overwrite the newer one.
//
// The point is what is *not* held across the HTTP calls: no transaction, no
// row lock, no pool connection. A slow panel used to pin all three for up to
// five sequential ten-second requests per operation.

// claimLease is how long a claimed operation belongs to the run that claimed
// it. It comfortably covers the longest run — five sequential requests with a
// ten-second timeout each — and a run that has not recorded by then is
// presumed dead: the next attempt may take the operation over.
const claimLease = 2 * time.Minute

// recordAttempts bounds how often a short transaction is retried after a
// serialization failure. Only the database work is retried; Remnawave is never
// called again from here.
const recordAttempts = 4

// claim is what the claim transaction hands to the Remnawave phase.
type claim struct {
	operation    dbgen.FulfillmentOperation
	entitlement  dbgen.Entitlement
	subscription dbgen.Subscription
	desired      desiredState
	// remoteID is the Remnawave user the operation addresses, or zero when the
	// subscription has never been provisioned.
	remoteID int64
}

// drift is one divergence a reconcile observed, recorded with the result.
type drift struct {
	kind               string
	expected, observed []byte
}

// outcome is what the Remnawave phase produced.
type outcome struct {
	remote remnawave.User
	drifts []drift
	err    error
}

func (worker *Worker) work(ctx context.Context, job *river.Job[JobArgs]) error {
	operationID, err := parseUUID(job.Args.OperationID)
	if err != nil {
		return river.JobCancel(err)
	}
	var (
		claimed claim
		proceed bool
	)
	err = retrySerializable(ctx, func() error {
		var claimErr error
		claimed, proceed, claimErr = worker.claim(ctx, operationID)
		return claimErr
	})
	if err != nil || !proceed {
		return err
	}
	result := worker.call(ctx, claimed)
	return worker.record(ctx, claimed, result)
}

// claim is the first transaction. It reports proceed=false, with a nil error
// or a River control error, whenever the operation must not reach Remnawave
// on this run.
func (worker *Worker) claim(ctx context.Context, operationID pgtype.UUID) (claim, bool, error) {
	tx, err := worker.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return claim{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	operation, err := queries.LockFulfillmentOperation(ctx, operationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return claim{}, false, river.JobCancel(err)
		}
		return claim{}, false, err
	}
	if operation.Status == "succeeded" || operation.Status == "cancelled" {
		return claim{}, false, tx.Commit(ctx)
	}
	now := worker.clock()
	if held, until := leaseHeld(operation.Status, operation.NextAttemptAt.Time, now); held {
		// Another run claimed this operation and is still inside its lease.
		// Waiting it out is the only safe move: running beside it would make
		// the same Remnawave calls twice and race it to the record.
		return claim{}, false, river.JobSnooze(until.Sub(now) + time.Second)
	}
	entitlement, err := queries.GetEntitlement(ctx, operation.EntitlementID)
	if err != nil {
		return claim{}, false, err
	}
	// An operation outlives the entitlement it was written for when a newer
	// entitlement of the same subscription is provisioned first — a reconcile
	// queued before a renewal settled, for instance. Applying it now would
	// push the old expiry over the new one and write `active` back onto a row
	// that has been retired, which is two live entitlements and an expiry
	// that flickers every fifteen minutes. It is cancelled, with the reason,
	// before anything is asked of Remnawave.
	if reason, obsolete := obsoleteOperation(entitlement.Status); obsolete {
		return claim{}, false, worker.cancel(ctx, tx, queries, operation, reason)
	}
	var desired desiredState
	if err := json.Unmarshal(operation.DesiredState, &desired); err != nil {
		return claim{}, false, worker.failPermanent(ctx, tx, queries, operation, "invalid_desired_state")
	}
	if !desired.EffectiveAt.IsZero() && now.Before(desired.EffectiveAt) {
		return claim{}, false, river.JobSnooze(desired.EffectiveAt.Sub(now))
	}
	// Maintenance mode holds provisioning back without losing it: the operation
	// is not claimed, keeps its attempt count, and River snoozes the job until
	// the dependency recovers.
	if paused, pauseErr := worker.maintenanceActive(ctx, queries); pauseErr != nil {
		return claim{}, false, pauseErr
	} else if paused {
		if _, err = queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "retrying", AttemptCount: operation.AttemptCount, NextAttemptAt: pgtype.Timestamptz{Time: now.Add(maintenanceSnooze), Valid: true}, LastErrorCode: pgtype.Text{String: "maintenance_active", Valid: true}}); err != nil {
			return claim{}, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return claim{}, false, err
		}
		return claim{}, false, river.JobSnooze(maintenanceSnooze)
	}
	// The grace period is the plan's promise that access outlives the paid end
	// by a little, so the customer can renew without a gap. It only means
	// anything if Remnawave is told, so it is folded into the expiry here.
	graceSeconds, err := queries.GetPlanVersionGracePeriod(ctx, entitlement.PlanVersionID)
	if err != nil {
		return claim{}, false, err
	}
	desired = desired.withGrace(time.Duration(graceSeconds) * time.Second)
	subscription, err := worker.subscriptionFor(ctx, queries, entitlement)
	if err != nil {
		return claim{}, false, err
	}
	remoteID, err := worker.remoteUserID(ctx, queries, entitlement, subscription)
	if err != nil {
		return claim{}, false, err
	}
	// The lease is recorded in next_attempt_at: for a running operation it is
	// the instant after which the run is presumed dead. The revival scan reads
	// the same column, so a claim abandoned by a crash is picked up exactly as
	// a stalled retry is.
	operation, err = queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "running", AttemptCount: operation.AttemptCount + 1, NextAttemptAt: pgtype.Timestamptz{Time: now.Add(claimLease), Valid: true}})
	if err != nil {
		return claim{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return claim{}, false, err
	}
	return claim{
		operation: operation, entitlement: entitlement, subscription: subscription,
		desired: desired, remoteID: remoteID,
	}, true, nil
}

// leaseHeld reports whether a running operation is still inside the lease of
// the run that claimed it, and until when.
func leaseHeld(status string, leaseUntil, now time.Time) (bool, time.Time) {
	if status != "running" || !now.Before(leaseUntil) {
		return false, time.Time{}
	}
	return true, leaseUntil
}

// call is the Remnawave phase. It holds no transaction and no connection.
func (worker *Worker) call(ctx context.Context, claimed claim) outcome {
	var result outcome
	if claimed.operation.Operation == "reconcile" {
		drifts, err := worker.observeDrift(ctx, claimed)
		result.drifts = drifts
		if err != nil {
			result.err = err
			return result
		}
	}
	result.remote, result.err = worker.apply(ctx, claimed)
	return result
}

// observeDrift compares the Remnawave user this subscription owns against the
// desired state. Drift is always evaluated against that user, so one
// customer's second subscription can never be mistaken for the first one's
// remote state.
func (worker *Worker) observeDrift(ctx context.Context, claimed claim) ([]drift, error) {
	missing := []drift{{kind: "missing_remote", expected: []byte(`{"exists":true}`), observed: []byte(`{"exists":false}`)}}
	if claimed.remoteID == 0 {
		return missing, nil
	}
	remote, err := worker.provisioner.User(ctx, claimed.remoteID)
	if errors.Is(err, remnawave.ErrNotFound) {
		return missing, nil
	}
	if err != nil {
		return nil, err
	}
	return driftsBetween(claimed.desired, claimed.entitlement.Status, remote), nil
}

// driftsBetween lists every way the remote user disagrees with the desired
// state.
func driftsBetween(desired desiredState, entitlementStatus string, remote remnawave.User) []drift {
	type mismatch struct {
		kind               string
		expected, observed any
	}
	mismatches := make([]mismatch, 0, 4)
	if !remote.ExpireAt.Equal(desired.remoteExpireAt) {
		mismatches = append(mismatches, mismatch{"expiry", desired.remoteExpireAt, remote.ExpireAt})
	}
	// A nil desired limit means unlimited, and unlimited is a value Remnawave
	// can disagree with: a user still carrying the previous plan's cap is
	// drift, not "nothing configured".
	if !trafficLimitAgrees(desired.TrafficAllowanceBytes, remote.TrafficLimitBytes) {
		mismatches = append(mismatches, mismatch{"traffic", desired.TrafficAllowanceBytes, remote.TrafficLimitBytes})
	}
	if !deviceLimitAgrees(desired.DeviceLimit, remote.HWIDDeviceLimit) {
		mismatches = append(mismatches, mismatch{"device_limit", desired.DeviceLimit, remote.HWIDDeviceLimit})
	}
	remoteSquads := make([]string, 0, len(remote.ActiveInternalSquads))
	for _, squad := range remote.ActiveInternalSquads {
		remoteSquads = append(remoteSquads, squad.UUID)
	}
	if !sameStringSet(desired.SquadIDs, remoteSquads) {
		mismatches = append(mismatches, mismatch{"squads", desired.SquadIDs, remoteSquads})
	}
	if reconciled := reconciledStatus(entitlementStatus, remote); reconciled != entitlementStatus {
		mismatches = append(mismatches, mismatch{"status", entitlementStatus, reconciled})
	}
	drifts := make([]drift, 0, len(mismatches))
	for _, item := range mismatches {
		expected, _ := json.Marshal(map[string]any{"value": item.expected})
		observed, _ := json.Marshal(map[string]any{"value": item.observed})
		drifts = append(drifts, drift{kind: item.kind, expected: expected, observed: observed})
	}
	return drifts
}

// record is the last transaction: it writes what the Remnawave phase produced,
// guarded by the claim.
func (worker *Worker) record(ctx context.Context, claimed claim, result outcome) error {
	if result.err != nil {
		if err := retrySerializable(ctx, func() error { return worker.recordFailure(ctx, claimed, result) }); err != nil {
			return err
		}
		return result.err
	}
	return retrySerializable(ctx, func() error { return worker.recordSuccess(ctx, claimed, result) })
}

// lockClaim re-locks the operation and reports whether this run still owns it.
// A run whose lease lapsed may have been taken over by another, which then
// owns the operation and its result; this run's result is stale and dropped.
func lockClaim(ctx context.Context, queries *dbgen.Queries, claimed claim) (dbgen.FulfillmentOperation, bool, error) {
	operation, err := queries.LockFulfillmentOperation(ctx, claimed.operation.ID)
	if err != nil {
		return dbgen.FulfillmentOperation{}, false, err
	}
	owned := operation.Status == "running" && operation.AttemptCount == claimed.operation.AttemptCount
	return operation, owned, nil
}

func (worker *Worker) recordFailure(ctx context.Context, claimed claim, result outcome) error {
	tx, err := worker.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	operation, owned, err := lockClaim(ctx, queries, claimed)
	if err != nil {
		return err
	}
	if !owned {
		return tx.Commit(ctx)
	}
	if err = insertDrifts(ctx, queries, claimed.entitlement.ID, result.drifts); err != nil {
		return err
	}
	code := classifyError(result.err)
	nextAttempt := worker.clock().Add(backoff(operation.AttemptCount))
	if _, err = queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "retrying", AttemptCount: operation.AttemptCount, NextAttemptAt: pgtype.Timestamptz{Time: nextAttempt, Valid: true}, LastErrorCode: pgtype.Text{String: code, Valid: true}}); err != nil {
		return err
	}
	if _, err = queries.InsertFulfillmentHistory(ctx, dbgen.InsertFulfillmentHistoryParams{OperationID: operation.ID, Status: "retrying", CorrelationID: operation.CorrelationID, RequestSummary: safeDesiredSummary(claimed.desired), ResponseSummary: []byte(`{}`), ErrorCode: pgtype.Text{String: code, Valid: true}}); err != nil {
		return err
	}
	// Operators are told once a run has clearly stopped being a blip. The
	// dedupe key is the operation, so one struggling entitlement produces one
	// notice however many times it retries.
	if operation.AttemptCount >= operatorAlertAttempts {
		notifyFulfillmentFailure(ctx, queries, operation, code)
	}
	return tx.Commit(ctx)
}

func (worker *Worker) recordSuccess(ctx context.Context, claimed claim, result outcome) error {
	tx, err := worker.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	operation, owned, err := lockClaim(ctx, queries, claimed)
	if err != nil {
		return err
	}
	if !owned {
		return tx.Commit(ctx)
	}
	// The entitlement is read again: it may have been retired while Remnawave
	// was being called, and writing the observed state back would put `active`
	// onto a superseded row. The next reconcile of the live entitlement pushes
	// its own state over whatever this run left in Remnawave.
	entitlement, err := queries.GetEntitlement(ctx, claimed.entitlement.ID)
	if err != nil {
		return err
	}
	if reason, obsolete := obsoleteOperation(entitlement.Status); obsolete {
		return worker.cancel(ctx, tx, queries, operation, reason)
	}
	if err = insertDrifts(ctx, queries, entitlement.ID, result.drifts); err != nil {
		return err
	}
	remote := result.remote
	subscription := claimed.subscription
	observed, _ := json.Marshal(map[string]any{"status": remote.Status, "expireAt": remote.ExpireAt, "trafficLimitBytes": remote.TrafficLimitBytes, "deviceLimit": remote.HWIDDeviceLimit})
	if subscription.ID.Valid {
		if _, err = queries.UpsertSubscriptionRemnawaveUser(ctx, dbgen.UpsertSubscriptionRemnawaveUserParams{SubscriptionID: subscription.ID, RemnawaveUserID: pgtype.Int8{Int64: remote.ID, Valid: true}, RemnawaveUsername: pgtype.Text{String: remote.Username, Valid: remote.Username != ""}, ObservedState: observed}); err != nil {
			return err
		}
	}
	// The customer-level mapping stays the primary subscription's Remnawave
	// user, so the v0.2 self-service screens keep resolving unchanged.
	if subscription.Slot <= 1 {
		if _, err = queries.UpsertRemnawaveMapping(ctx, dbgen.UpsertRemnawaveMappingParams{UserID: entitlement.UserID, RemnawaveID: remote.ID, ObservedState: observed}); err != nil {
			return err
		}
	}
	if _, err = queries.UpdateEntitlementObservedState(ctx, dbgen.UpdateEntitlementObservedStateParams{EntitlementID: entitlement.ID, Status: reconciledStatus(entitlement.Status, remote), RemnawaveUserID: pgtype.Int8{Int64: remote.ID, Valid: true}, ObservedState: observed}); err != nil {
		return err
	}
	if err = queries.ResolveEntitlementDrifts(ctx, entitlement.ID); err != nil {
		return err
	}
	if operation.Operation == "create" {
		if err = queries.SupersedePreviousEntitlements(ctx, dbgen.SupersedePreviousEntitlementsParams{UserID: entitlement.UserID, CurrentEntitlementID: entitlement.ID, SubscriptionID: entitlement.SubscriptionID}); err != nil {
			return err
		}
	}
	if _, err = queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "succeeded", AttemptCount: operation.AttemptCount, NextAttemptAt: pgtype.Timestamptz{Time: worker.clock(), Valid: true}}); err != nil {
		return err
	}
	if _, err = queries.InsertFulfillmentHistory(ctx, dbgen.InsertFulfillmentHistoryParams{OperationID: operation.ID, Status: "succeeded", CorrelationID: operation.CorrelationID, RequestSummary: safeDesiredSummary(claimed.desired), ResponseSummary: []byte(fmt.Sprintf(`{"remnawaveUserId":%d,"status":%q}`, remote.ID, remote.Status))}); err != nil {
		return err
	}
	if operation.Operation == "create" {
		if _, err = queries.SetOrderState(ctx, dbgen.SetOrderStateParams{ID: entitlement.OrderID, State: "fulfilled"}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func insertDrifts(ctx context.Context, queries *dbgen.Queries, entitlementID pgtype.UUID, drifts []drift) error {
	for _, item := range drifts {
		if _, err := queries.InsertEntitlementDrift(ctx, dbgen.InsertEntitlementDriftParams{EntitlementID: entitlementID, Kind: item.kind, Expected: item.expected, Observed: item.observed}); err != nil {
			return err
		}
	}
	return nil
}

// retrySerializable runs a short transaction again when PostgreSQL aborted it
// to keep the history serializable. It is only ever wrapped around database
// work, never around a Remnawave call.
func retrySerializable(ctx context.Context, run func() error) error {
	for attempt := 1; ; attempt++ {
		err := run()
		if err == nil || attempt >= recordAttempts || !serializationFailure(err) || ctx.Err() != nil {
			return err
		}
	}
}

func serializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}
