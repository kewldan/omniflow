package fulfillment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel/codes"
)

type Worker struct {
	river.WorkerDefaults[JobArgs]
	pool        *pgxpool.Pool
	provisioner remnawave.Provisioner
	metrics     *platform.Metrics
	clock       func() time.Time
}

func NewWorker(pool *pgxpool.Pool, provisioner remnawave.Provisioner, metrics ...*platform.Metrics) *Worker {
	worker := &Worker{pool: pool, provisioner: provisioner, clock: time.Now}
	if len(metrics) > 0 {
		worker.metrics = metrics[0]
	}
	return worker
}

// Work runs one fulfillment operation inside its own span, so a slow Remnawave
// call, the database round trips it causes, and the retry that follows all show
// up under one trace.
func (worker *Worker) Work(ctx context.Context, job *river.Job[JobArgs]) error {
	ctx, span := platform.StartSpan(ctx, "fulfillment.operation", platform.JobAttributes(JobArgs{}.Kind(), job.Attempt)...)
	defer span.End()
	started := time.Now()
	err := worker.work(ctx, job)
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
		span.SetStatus(codes.Error, classifyError(err))
	}
	worker.metrics.ObserveJob(JobArgs{}.Kind(), outcome, time.Since(started))
	return err
}

type desiredState struct {
	EffectiveAt           time.Time `json:"effectiveAt"`
	EndsAt                time.Time `json:"endsAt"`
	TrafficAllowanceBytes *int64    `json:"trafficAllowanceBytes"`
	DeviceLimit           *int      `json:"deviceLimit"`
	SquadIDs              []string  `json:"squadIds"`
}

func (worker *Worker) work(ctx context.Context, job *river.Job[JobArgs]) error {
	operationID, err := parseUUID(job.Args.OperationID)
	if err != nil {
		return river.JobCancel(err)
	}
	tx, err := worker.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	operation, err := queries.LockFulfillmentOperation(ctx, operationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return river.JobCancel(err)
		}
		return err
	}
	if operation.Status == "succeeded" || operation.Status == "cancelled" {
		return tx.Commit(ctx)
	}
	entitlement, err := queries.GetEntitlement(ctx, operation.EntitlementID)
	if err != nil {
		return err
	}
	var desired desiredState
	if err := json.Unmarshal(operation.DesiredState, &desired); err != nil {
		return worker.failPermanent(ctx, tx, queries, operation, "invalid_desired_state")
	}
	if !desired.EffectiveAt.IsZero() && worker.clock().Before(desired.EffectiveAt) {
		return river.JobSnooze(desired.EffectiveAt.Sub(worker.clock()))
	}
	operation, err = queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "running", AttemptCount: operation.AttemptCount + 1, NextAttemptAt: pgtype.Timestamptz{Time: worker.clock(), Valid: true}})
	if err != nil {
		return err
	}
	subscription, err := worker.subscriptionFor(ctx, queries, entitlement)
	if err != nil {
		return err
	}
	if operation.Operation == "reconcile" {
		if err := worker.detectDrift(ctx, queries, entitlement, subscription, desired); err != nil {
			code := classifyError(err)
			_, _ = queries.InsertFulfillmentHistory(ctx, dbgen.InsertFulfillmentHistoryParams{OperationID: operation.ID, Status: "retrying", CorrelationID: operation.CorrelationID, RequestSummary: safeDesiredSummary(desired), ResponseSummary: []byte(`{}`), ErrorCode: pgtype.Text{String: code, Valid: true}})
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return commitErr
			}
			return err
		}
	}
	// Maintenance mode holds provisioning back without losing it: the operation
	// stays pending and River snoozes the job until the dependency recovers.
	if paused, pauseErr := worker.maintenanceActive(ctx, queries); pauseErr != nil {
		return pauseErr
	} else if paused {
		if _, err = queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "retrying", AttemptCount: operation.AttemptCount - 1, NextAttemptAt: pgtype.Timestamptz{Time: worker.clock().Add(maintenanceSnooze), Valid: true}, LastErrorCode: pgtype.Text{String: "maintenance_active", Valid: true}}); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return river.JobSnooze(maintenanceSnooze)
	}
	remote, callErr := worker.apply(ctx, queries, operation.Operation, entitlement, subscription, desired)
	if callErr != nil {
		code := classifyError(callErr)
		nextAttempt := worker.clock().Add(backoff(operation.AttemptCount))
		_, _ = queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "retrying", AttemptCount: operation.AttemptCount, NextAttemptAt: pgtype.Timestamptz{Time: nextAttempt, Valid: true}, LastErrorCode: pgtype.Text{String: code, Valid: true}})
		_, _ = queries.InsertFulfillmentHistory(ctx, dbgen.InsertFulfillmentHistoryParams{OperationID: operation.ID, Status: "retrying", CorrelationID: operation.CorrelationID, RequestSummary: safeDesiredSummary(desired), ResponseSummary: []byte(`{}`), ErrorCode: pgtype.Text{String: code, Valid: true}})
		// Operators are told once a run has clearly stopped being a blip. The
		// dedupe key is the operation, so one struggling entitlement produces one
		// notice however many times it retries.
		if operation.AttemptCount >= operatorAlertAttempts {
			notifyFulfillmentFailure(ctx, queries, operation, code)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return commitErr
		}
		return callErr
	}
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
	if _, err = queries.InsertFulfillmentHistory(ctx, dbgen.InsertFulfillmentHistoryParams{OperationID: operation.ID, Status: "succeeded", CorrelationID: operation.CorrelationID, RequestSummary: safeDesiredSummary(desired), ResponseSummary: []byte(fmt.Sprintf(`{"remnawaveUserId":%d,"status":%q}`, remote.ID, remote.Status))}); err != nil {
		return err
	}
	if operation.Operation == "create" {
		if _, err = queries.SetOrderState(ctx, dbgen.SetOrderStateParams{ID: entitlement.OrderID, State: "fulfilled"}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (worker *Worker) detectDrift(ctx context.Context, queries *dbgen.Queries, entitlement dbgen.Entitlement, subscription dbgen.Subscription, desired desiredState) error {
	// Drift is always evaluated against the Remnawave user this subscription
	// owns, so one customer's second subscription can never be mistaken for the
	// first one's remote state.
	remoteID, err := worker.remoteUserID(ctx, queries, entitlement, subscription)
	if err != nil {
		return nil
	}
	if remoteID == 0 {
		_, _ = queries.InsertEntitlementDrift(ctx, dbgen.InsertEntitlementDriftParams{EntitlementID: entitlement.ID, Kind: "missing_remote", Expected: []byte(`{"exists":true}`), Observed: []byte(`{"exists":false}`)})
		return nil
	}
	remote, err := worker.provisioner.User(ctx, remoteID)
	if errors.Is(err, remnawave.ErrNotFound) {
		_, _ = queries.InsertEntitlementDrift(ctx, dbgen.InsertEntitlementDriftParams{EntitlementID: entitlement.ID, Kind: "missing_remote", Expected: []byte(`{"exists":true}`), Observed: []byte(`{"exists":false}`)})
		return nil
	}
	if err != nil {
		return err
	}
	type mismatch struct {
		kind               string
		expected, observed any
	}
	mismatches := make([]mismatch, 0, 4)
	if !remote.ExpireAt.Equal(desired.EndsAt) {
		mismatches = append(mismatches, mismatch{"expiry", desired.EndsAt, remote.ExpireAt})
	}
	if desired.TrafficAllowanceBytes != nil && remote.TrafficLimitBytes != *desired.TrafficAllowanceBytes {
		mismatches = append(mismatches, mismatch{"traffic", *desired.TrafficAllowanceBytes, remote.TrafficLimitBytes})
	}
	if desired.DeviceLimit != nil && (remote.HWIDDeviceLimit == nil || *remote.HWIDDeviceLimit != *desired.DeviceLimit) {
		mismatches = append(mismatches, mismatch{"device_limit", desired.DeviceLimit, remote.HWIDDeviceLimit})
	}
	remoteSquads := make([]string, 0, len(remote.ActiveInternalSquads))
	for _, squad := range remote.ActiveInternalSquads {
		remoteSquads = append(remoteSquads, squad.UUID)
	}
	if !sameStringSet(desired.SquadIDs, remoteSquads) {
		mismatches = append(mismatches, mismatch{"squads", desired.SquadIDs, remoteSquads})
	}
	if reconciled := reconciledStatus(entitlement.Status, remote); reconciled != entitlement.Status {
		mismatches = append(mismatches, mismatch{"status", entitlement.Status, reconciled})
	}
	for _, item := range mismatches {
		expected, _ := json.Marshal(map[string]any{"value": item.expected})
		observed, _ := json.Marshal(map[string]any{"value": item.observed})
		if _, err := queries.InsertEntitlementDrift(ctx, dbgen.InsertEntitlementDriftParams{EntitlementID: entitlement.ID, Kind: item.kind, Expected: expected, Observed: observed}); err != nil {
			return err
		}
	}
	return nil
}

// maintenanceSnooze is how long a held fulfillment job waits before it looks at
// the dependency again.
const maintenanceSnooze = time.Minute

// maintenanceActive reports whether provisioning is currently held back.
func (worker *Worker) maintenanceActive(ctx context.Context, queries *dbgen.Queries) (bool, error) {
	state, err := queries.ReadMaintenanceState(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	maintenance := commerce.Maintenance{Active: state.Active, Source: state.Source}
	return maintenance.Blocks(commerce.ActionFulfillment), nil
}

// subscriptionFor resolves the subscription an entitlement provisions. An
// entitlement written before v0.5 carries no subscription; it is adopted onto
// the customer's primary subscription so it keeps its existing Remnawave user.
func (worker *Worker) subscriptionFor(ctx context.Context, queries *dbgen.Queries, entitlement dbgen.Entitlement) (dbgen.Subscription, error) {
	if entitlement.SubscriptionID.Valid {
		subscription, err := queries.GetSubscription(ctx, entitlement.SubscriptionID)
		if err == nil {
			return subscription, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return dbgen.Subscription{}, err
		}
	}
	subscription, err := queries.GetPrimarySubscription(ctx, entitlement.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Subscription{}, nil
	}
	return subscription, err
}

// remnawaveUsername is the deterministic Remnawave username for a subscription.
// Deriving it from the subscription rather than the customer is what lets one
// customer own several independent Remnawave users.
func remnawaveUsername(subscriptionID pgtype.UUID) string {
	return "omniflow_" + strings.ReplaceAll(uuidString(subscriptionID), "-", "")[:16]
}

// legacyUsername is the pre-v0.5 customer-derived name. It is still recognised
// for a first subscription so an installation upgraded from v0.4 adopts the
// Remnawave user it already has instead of creating a second one.
func legacyUsername(userID pgtype.UUID) string {
	return "omniflow_" + strings.ReplaceAll(uuidString(userID), "-", "")[:16]
}

// remoteUserID is the Remnawave user this entitlement provisions, or zero when
// it has never been provisioned. The subscription is authoritative; the
// customer-level mapping is only consulted for a pre-v0.5 entitlement.
func (worker *Worker) remoteUserID(ctx context.Context, queries *dbgen.Queries, entitlement dbgen.Entitlement, subscription dbgen.Subscription) (int64, error) {
	if subscription.RemnawaveUserID.Valid {
		return subscription.RemnawaveUserID.Int64, nil
	}
	if subscription.ID.Valid && subscription.Slot > 1 {
		return 0, nil
	}
	mapping, err := queries.GetRemnawaveMappingByCustomer(ctx, entitlement.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return mapping.RemnawaveID, nil
}

func (worker *Worker) apply(ctx context.Context, queries *dbgen.Queries, operation string, entitlement dbgen.Entitlement, subscription dbgen.Subscription, desired desiredState) (remnawave.User, error) {
	username := legacyUsername(entitlement.UserID)
	candidates := []string{username}
	if subscription.ID.Valid {
		username = remnawaveUsername(subscription.ID)
		candidates = []string{username}
		if subscription.Slot <= 1 {
			candidates = append(candidates, legacyUsername(entitlement.UserID))
		}
	}
	provision := remnawave.ProvisionUser{Username: username, ExpireAt: desired.EndsAt, TrafficLimitBytes: desired.TrafficAllowanceBytes, HWIDDeviceLimit: desired.DeviceLimit, InternalSquadIDs: desired.SquadIDs}
	remoteID, err := worker.remoteUserID(ctx, queries, entitlement, subscription)
	if err != nil {
		return remnawave.User{}, err
	}
	createOrRecover := func() (remnawave.User, error) {
		for _, candidate := range candidates {
			existing, lookupErr := worker.provisioner.UserByUsername(ctx, candidate)
			if lookupErr == nil {
				return worker.provisioner.UpdateUser(ctx, existing.ID, provision)
			}
			if !errors.Is(lookupErr, remnawave.ErrNotFound) {
				return remnawave.User{}, lookupErr
			}
		}
		return worker.provisioner.CreateUser(ctx, provision)
	}
	if (operation == "create" || operation == "reconcile") && remoteID == 0 {
		return createOrRecover()
	}
	if operation == "create" || operation == "reconcile" {
		updated, updateErr := worker.provisioner.UpdateUser(ctx, remoteID, provision)
		if errors.Is(updateErr, remnawave.ErrNotFound) {
			return createOrRecover()
		}
		return updated, updateErr
	}
	if remoteID == 0 {
		return remnawave.User{}, remnawave.ErrNotFound
	}
	switch operation {
	case "extend", "set_limits", "set_squads":
		return worker.provisioner.UpdateUser(ctx, remoteID, provision)
	case "enable":
		if err := worker.provisioner.EnableUser(ctx, remoteID); err != nil {
			return remnawave.User{}, err
		}
	case "disable", "pause":
		// A pause is a disable as far as Remnawave is concerned. What makes it a
		// pause is the entitlement's own clock, which stopped in the same
		// transaction that enqueued this.
		if err := worker.provisioner.DisableUser(ctx, remoteID); err != nil {
			return remnawave.User{}, err
		}
	case "resume":
		// Two calls in one operation, and the order is the reason they are one
		// operation. The expiry has already been moved forward by the length of
		// the pause; pushing it before re-enabling means the user is never
		// briefly enabled with an expiry that has since passed.
		if _, err := worker.provisioner.UpdateUser(ctx, remoteID, provision); err != nil {
			return remnawave.User{}, err
		}
		if err := worker.provisioner.EnableUser(ctx, remoteID); err != nil {
			return remnawave.User{}, err
		}
	case "reset_traffic":
		if err := worker.provisioner.ResetUserTraffic(ctx, remoteID); err != nil {
			return remnawave.User{}, err
		}
	default:
		return remnawave.User{}, river.JobCancel(errors.New("unsupported fulfillment operation"))
	}
	return worker.provisioner.User(ctx, remoteID)
}

// operatorAlertAttempts is how many failed attempts a fulfillment run makes
// before an operator is told. Below it, ordinary backoff is expected to recover.
const operatorAlertAttempts = 3

func (worker *Worker) failPermanent(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, operation dbgen.FulfillmentOperation, code string) error {
	_, _ = queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "failed", AttemptCount: operation.AttemptCount + 1, NextAttemptAt: pgtype.Timestamptz{Time: worker.clock(), Valid: true}, LastErrorCode: pgtype.Text{String: code, Valid: true}})
	notifyFulfillmentFailure(ctx, queries, operation, code)
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return river.JobCancel(errors.New(code))
}

// notifyFulfillmentFailure queues an operator notice inside the worker's own
// transaction. It carries identifiers and a classified error code only: never a
// Remnawave response body, a link, or customer content.
func notifyFulfillmentFailure(ctx context.Context, queries *dbgen.Queries, operation dbgen.FulfillmentOperation, code string) {
	payload, err := json.Marshal(map[string]any{
		"entitlementId": uuidString(operation.EntitlementID),
		"operation":     operation.Operation,
		"errorCode":     code,
		"status":        "retrying",
	})
	if err != nil {
		return
	}
	_, _ = queries.EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{
		Kind: "fulfillment_failure", DedupeKey: "operation:" + uuidString(operation.ID), Payload: payload,
	})
}

func safeDesiredSummary(desired desiredState) []byte {
	value, _ := json.Marshal(map[string]any{"endsAt": desired.EndsAt, "trafficLimitConfigured": desired.TrafficAllowanceBytes != nil, "deviceLimitConfigured": desired.DeviceLimit != nil, "squadCount": len(desired.SquadIDs)})
	return value
}

func classifyError(err error) string {
	if errors.Is(err, remnawave.ErrNotFound) {
		return "remote_user_not_found"
	}
	var apiError *remnawave.APIError
	if errors.As(err, &apiError) {
		return fmt.Sprintf("remnawave_http_%d", apiError.StatusCode)
	}
	return "remnawave_unavailable"
}

func backoff(attempt int32) time.Duration {
	seconds := int64(5)
	for range min(attempt, 8) {
		seconds *= 2
	}
	return time.Duration(seconds) * time.Second
}

// reconciledStatus is what the entitlement's status should be after looking at
// Remnawave, given what it was before.
//
// It exists because a paused subscription and a disabled one are the same thing
// from Remnawave's side, and only Omniflow knows the difference. Without this,
// the first reconcile after a pause would map DISABLED back to `disabled`,
// which the table's pairing constraint refuses outright — and if it did not,
// the customer would silently lose the days the pause was preserving.
//
// `expired` counts as agreement too, and that is not a loophole. A pause freezes
// `ends_at` where it stood, so real time walks past it while the subscription is
// not being consumed; Remnawave, which knows nothing about the pause, then
// reports the user as expired. Treating that as the entitlement expiring would
// take away the days the pause exists to preserve — which is the whole feature,
// undone by a background job a week later.
//
// A paused entitlement whose remote user is *active* is real drift: somebody
// re-enabled it in Remnawave, and the customer is connecting on time nobody is
// charging for. That case falls through and is reported.
func reconciledStatus(local string, user remnawave.User) string {
	observed := observedEntitlementStatus(user)
	if local == "paused" && (observed == "disabled" || observed == "expired") {
		return "paused"
	}
	return observed
}

func observedEntitlementStatus(user remnawave.User) string {
	switch strings.ToUpper(user.Status) {
	case "ACTIVE":
		return "active"
	case "LIMITED":
		return "limited"
	case "DISABLED":
		return "disabled"
	case "EXPIRED":
		return "expired"
	default:
		return "failed"
	}
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil || !id.Valid {
		return pgtype.UUID{}, errors.New("invalid operation ID")
	}
	return id, nil
}

func uuidString(value pgtype.UUID) string {
	b := value.Bytes
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
