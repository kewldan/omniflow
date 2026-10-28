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
	EffectiveAt time.Time `json:"effectiveAt"`
	// EndsAt is the paid end of the entitlement. It is not what Remnawave is
	// told: the worker adds the plan's grace period to it first, and that sum
	// is both what is pushed and what drift is measured against.
	EndsAt                time.Time `json:"endsAt"`
	TrafficAllowanceBytes *int64    `json:"trafficAllowanceBytes"`
	DeviceLimit           *int      `json:"deviceLimit"`
	SquadIDs              []string  `json:"squadIds"`
	// ResetTraffic asks for the user's traffic counter to start afresh once
	// the new state is applied. Settlement sets it for an extension, a
	// renewal, and an upgrade: the customer paid for a new period, and a
	// LIMITED user who renews must come back as ACTIVE rather than stay
	// LIMITED on last period's usage.
	ResetTraffic bool `json:"resetTraffic,omitempty"`
	// remoteExpireAt is EndsAt plus the grace period, resolved by the worker
	// per operation rather than stored, so every creator agrees on it.
	remoteExpireAt time.Time
}

// withGrace resolves the expiry Remnawave is asked to enforce.
func (desired desiredState) withGrace(grace time.Duration) desiredState {
	desired.remoteExpireAt = commerce.RemoteExpiry(desired.EndsAt, grace)
	return desired
}

// trafficLimitAgrees reports whether Remnawave's traffic cap matches the
// desired allowance, where a nil allowance is unlimited and Remnawave spells
// unlimited as zero.
func trafficLimitAgrees(desired *int64, remote int64) bool {
	if desired == nil {
		return remote == 0
	}
	return remote == *desired
}

// deviceLimitAgrees is the same comparison for the device limit, where
// Remnawave spells unlimited as null or zero.
func deviceLimitAgrees(desired *int, remote *int) bool {
	if desired == nil {
		return remote == nil || *remote == 0
	}
	return remote != nil && *remote == *desired
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

// errSubscriptionUnresolved reports an entitlement whose subscription cannot be
// established with certainty. It is a permanent failure an operator looks at,
// never a reason to guess.
var errSubscriptionUnresolved = errors.New("the entitlement's subscription cannot be resolved")

// subscriptionFor resolves the subscription an entitlement provisions: its
// own, which must agree with its order's.
//
// It never adopts. An entitlement once carried no subscription only before
// v0.5, and the upgrade migration gave every one of those slot 1; one without
// a subscription today was settled from an order whose subscription had been
// removed, and adopting it onto the customer's primary subscription is how a
// thirty-day second subscription overwrote a primary with months left. Such
// an entitlement takes its order's subscription when the order has one, and
// otherwise fails permanently rather than land on the wrong Remnawave user.
func (worker *Worker) subscriptionFor(ctx context.Context, queries *dbgen.Queries, entitlement dbgen.Entitlement) (dbgen.Subscription, error) {
	order, err := queries.GetOrder(ctx, entitlement.OrderID)
	if err != nil {
		return dbgen.Subscription{}, err
	}
	// A gift's entitlement belongs to the recipient while its order is the
	// buyer's, and that order targets no subscription; only an order of the
	// entitlement's own customer says which subscription it bought for.
	ordered := pgtype.UUID{}
	if order.UserID == entitlement.UserID {
		ordered = order.SubscriptionID
	}
	target := entitlement.SubscriptionID
	switch {
	case !target.Valid && !ordered.Valid:
		return dbgen.Subscription{}, errSubscriptionUnresolved
	case !target.Valid:
		target = ordered
	case ordered.Valid && ordered != target:
		return dbgen.Subscription{}, errSubscriptionUnresolved
	}
	subscription, err := queries.GetSubscription(ctx, target)
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Subscription{}, errSubscriptionUnresolved
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

// apply makes the Remnawave calls one operation needs. It runs outside any
// transaction and reads nothing from the database: everything it needs was
// resolved by the claim.
func (worker *Worker) apply(ctx context.Context, claimed claim) (remnawave.User, error) {
	operation, entitlement, subscription := claimed.operation.Operation, claimed.entitlement, claimed.subscription
	desired, remoteID := claimed.desired, claimed.remoteID
	var err error
	username := legacyUsername(entitlement.UserID)
	candidates := []string{username}
	if subscription.ID.Valid {
		username = remnawaveUsername(subscription.ID)
		candidates = []string{username}
		if subscription.Slot <= 1 {
			candidates = append(candidates, legacyUsername(entitlement.UserID))
		}
	}
	provision := remnawave.ProvisionUser{Username: username, ExpireAt: desired.remoteExpireAt, TrafficLimitBytes: desired.TrafficAllowanceBytes, HWIDDeviceLimit: desired.DeviceLimit, InternalSquadIDs: desired.SquadIDs}
	// createOrRecover reports whether the user was created fresh: a brand-new
	// user has no traffic to reset and nothing to re-enable.
	createOrRecover := func() (remnawave.User, bool, error) {
		for _, candidate := range candidates {
			existing, lookupErr := worker.provisioner.UserByUsername(ctx, candidate)
			if lookupErr == nil {
				updated, updateErr := worker.provisioner.UpdateUser(ctx, existing.ID, provision)
				return updated, false, updateErr
			}
			if !errors.Is(lookupErr, remnawave.ErrNotFound) {
				return remnawave.User{}, false, lookupErr
			}
		}
		created, createErr := worker.provisioner.CreateUser(ctx, provision)
		return created, true, createErr
	}
	if operation == "create" || operation == "reconcile" {
		var (
			remote  remnawave.User
			created bool
		)
		if remoteID == 0 {
			remote, created, err = createOrRecover()
		} else {
			remote, err = worker.provisioner.UpdateUser(ctx, remoteID, provision)
			if errors.Is(err, remnawave.ErrNotFound) {
				remote, created, err = createOrRecover()
			}
		}
		if err != nil || operation != "create" || created {
			return remote, err
		}
		return worker.finishPaidChange(ctx, remote, entitlement, desired)
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

// finishPaidChange completes a `create` against a user that already existed:
// the traffic counter is reset when the operation asks for it, and a user
// Remnawave holds as DISABLED is re-enabled. Both are what a paid renewal of
// an expired-then-disabled or limited user needs to actually restore access;
// without them the PATCH moved the expiry and the tunnel stayed dead.
//
// A paused entitlement is the one case where DISABLED is deliberate and must
// stay. Its clock is stopped by Omniflow, and only a resume may re-enable it.
func (worker *Worker) finishPaidChange(ctx context.Context, remote remnawave.User, entitlement dbgen.Entitlement, desired desiredState) (remnawave.User, error) {
	refresh := false
	if desired.ResetTraffic {
		if err := worker.provisioner.ResetUserTraffic(ctx, remote.ID); err != nil {
			return remnawave.User{}, err
		}
		refresh = true
	}
	if shouldEnable(remote.Status, entitlement.Status) {
		if err := worker.provisioner.EnableUser(ctx, remote.ID); err != nil {
			return remnawave.User{}, err
		}
		refresh = true
	}
	if !refresh {
		return remote, nil
	}
	return worker.provisioner.User(ctx, remote.ID)
}

// shouldEnable reports whether a paid change must switch the remote user back
// on: Remnawave holds it DISABLED and the entitlement is not a pause.
func shouldEnable(remoteStatus, entitlementStatus string) bool {
	return strings.EqualFold(remoteStatus, "DISABLED") && entitlementStatus != "paused"
}

// operatorAlertAttempts is how many failed attempts a fulfillment run makes
// before an operator is told. Below it, ordinary backoff is expected to recover.
const operatorAlertAttempts = 3

// obsoleteOperation reports whether an operation's entitlement has been
// retired, and why, so the operation must be cancelled rather than applied.
func obsoleteOperation(entitlementStatus string) (string, bool) {
	if entitlementStatus == "superseded" {
		return "entitlement_superseded", true
	}
	return "", false
}

// cancel closes an operation that must not be applied. It is not a failure:
// nothing went wrong, the work is simply no longer wanted, so no operator is
// told and the job completes normally.
func (worker *Worker) cancel(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, operation dbgen.FulfillmentOperation, reason string) error {
	if _, err := queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "cancelled", AttemptCount: operation.AttemptCount, NextAttemptAt: pgtype.Timestamptz{Time: worker.clock(), Valid: true}, LastErrorCode: pgtype.Text{String: reason, Valid: true}}); err != nil {
		return err
	}
	if _, err := queries.InsertFulfillmentHistory(ctx, dbgen.InsertFulfillmentHistoryParams{OperationID: operation.ID, Status: "cancelled", CorrelationID: operation.CorrelationID, RequestSummary: []byte(`{}`), ResponseSummary: []byte(`{}`), ErrorCode: pgtype.Text{String: reason, Valid: true}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

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
	value, _ := json.Marshal(map[string]any{"endsAt": desired.EndsAt, "remoteExpireAt": desired.remoteExpireAt, "trafficLimitConfigured": desired.TrafficAllowanceBytes != nil, "deviceLimitConfigured": desired.DeviceLimit != nil, "squadCount": len(desired.SquadIDs)})
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
