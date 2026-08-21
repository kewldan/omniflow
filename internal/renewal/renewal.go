// Package renewal charges subscription renewals without the customer present.
//
// It is the only place in Omniflow where money moves with nobody watching, and
// the design follows from that. The rules — whether a charge is allowed at all,
// when the next attempt falls, when to give up, when to tell the customer —
// live in `internal/recurring`, which has no database in it and is unit-tested
// on its own. This package owns the transaction boundaries, the provider calls,
// and one guarantee: a renewal cycle takes the customer's money at most once.
//
// That guarantee rests on three things rather than on care:
//
//   - `dunning_attempts` is the schedule. It is a durable table with a unique
//     key on (cycle_key, attempt), so a restarted worker resumes rather than
//     restarts, and a duplicated pass inserts nothing.
//   - Every attempt on a cycle shares one order, because the order's
//     idempotency key is derived from the cycle key. A retry finds the order
//     the previous attempt opened instead of billing for a second period.
//   - An outstanding payment blocks a new charge. If the provider has been
//     asked and has not answered, the attempt is deferred, not failed: the
//     honest reading of silence is "unknown", and charging again on unknown is
//     how a customer gets billed twice.
package renewal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
	"github.com/omniflow/omniflow/internal/recurring"
)

// batchSize bounds one pass. A large installation degrades in throughput
// rather than holding a connection for as long as its backlog takes.
const batchSize = 100

// Config tunes the loop. The zero value is the documented default.
type Config struct {
	// Interval is how often the schedule is examined. Attempt times are stored
	// with second precision and the retry delays are measured in hours, so a
	// minute of scheduling granularity costs nothing.
	Interval time.Duration
}

// Charger settles one payment. It is the paymentservice, narrowed to what this
// package uses, so the worker can be exercised without a provider.
type Charger interface {
	CreateIntent(context.Context, paymentservice.CreateIntentInput) (dbgen.PaymentIntent, error)
	Reconcile(ctx context.Context, paymentIntentID string) (dbgen.PaymentIntent, error)
}

// Worker runs the renewal schedule.
type Worker struct {
	pool      *pgxpool.Pool
	orders    *commercepg.Store
	payments  Charger
	providers map[string]payments.Provider
	logger    *slog.Logger
	config    Config
	clock     func() time.Time
}

// New builds the renewal worker.
func New(
	pool *pgxpool.Pool, orders *commercepg.Store, charger Charger,
	providers map[string]payments.Provider, logger *slog.Logger, config Config,
) *Worker {
	if config.Interval <= 0 {
		config.Interval = time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		pool: pool, orders: orders, payments: charger, providers: providers,
		logger: logger, config: config, clock: time.Now,
	}
}

// Run examines the schedule until the context is cancelled.
func (worker *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(worker.config.Interval)
	defer ticker.Stop()
	for {
		worker.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce performs one full pass: schedule what has come due, then charge what
// is scheduled.
//
// The order matters only for latency — a renewal that becomes due in this pass
// is attempted in this pass rather than the next one.
func (worker *Worker) RunOnce(ctx context.Context) {
	worker.schedule(ctx)
	worker.charge(ctx)
}

// ---------------------------------------------------------------------------
// Scheduling
// ---------------------------------------------------------------------------

// schedule opens the first attempt for every renewal that has come due.
//
// Nothing is charged here. Writing the attempt row first is what makes the
// charge resumable: a worker that dies between scheduling and charging leaves a
// scheduled attempt, which the next pass picks up, rather than a renewal that
// silently never happened.
func (worker *Worker) schedule(ctx context.Context) {
	queries := dbgen.New(worker.pool)
	due, err := queries.ListAutoRenewDue(ctx, batchSize)
	if err != nil {
		worker.logger.Error("list due renewals failed", "error", err)
		return
	}
	for _, row := range due {
		settings := recurring.Settings{
			Enabled:         row.Enabled,
			Funding:         row.Funding,
			State:           row.State,
			ConsentAt:       consentTime(row.ConsentAt),
			PaymentMethodID: uuidString(row.PaymentMethodID),
		}
		if !settings.Chargeable() {
			// A row can be written by an import, a migration, or a future code
			// path. The money-moving decision depends on the evidence of
			// agreement, not on a boolean that happens to be true.
			continue
		}
		if _, err := queries.ScheduleDunningAttempt(ctx, dbgen.ScheduleDunningAttemptParams{
			UserID:          row.UserID,
			SubscriptionID:  row.SubscriptionID,
			CycleKey:        row.CycleKey,
			Attempt:         1,
			Funding:         row.Funding,
			PaymentMethodID: row.PaymentMethodID,
			ScheduledFor:    pgtype.Timestamptz{Time: worker.clock().UTC(), Valid: true},
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			// pgx.ErrNoRows is the ON CONFLICT DO NOTHING path: the attempt
			// already exists, which is exactly what should happen on a
			// duplicated pass.
			worker.logger.Error("schedule renewal attempt failed",
				"cycleKey", row.CycleKey, "error", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Charging
// ---------------------------------------------------------------------------

// charge settles every attempt whose time has come.
func (worker *Worker) charge(ctx context.Context) {
	queries := dbgen.New(worker.pool)
	attempts, err := queries.ListDueDunningAttempts(ctx, batchSize)
	if err != nil {
		worker.logger.Error("list due renewal attempts failed", "error", err)
		return
	}
	for _, attempt := range attempts {
		if ctx.Err() != nil {
			return
		}
		if err := worker.attempt(ctx, queries, attempt); err != nil {
			worker.logger.Error("renewal attempt failed",
				"cycleKey", attempt.CycleKey, "attempt", attempt.Attempt, "error", err)
		}
	}
}

// attempt makes or resumes one charge.
func (worker *Worker) attempt(
	ctx context.Context, queries *dbgen.Queries, attempt dbgen.DunningAttempt,
) error {
	settings, err := queries.GetAutoRenewSettings(ctx, dbgen.GetAutoRenewSettingsParams{
		UserID: attempt.UserID, SubscriptionID: attempt.SubscriptionID,
	})
	// The customer may have turned auto-renew off between the attempt being
	// scheduled and it coming due, or the settings row may be gone with the
	// account. Consent is checked at the moment of charging, not at the moment
	// of scheduling, because the second is the one that moves money.
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (!settings.Enabled || settings.State == recurring.StateSuspended) {
		return worker.resolve(ctx, queries, attempt, recurring.OutcomeAbandoned, "consent_withdrawn", pgtype.UUID{}, false)
	}
	if err != nil {
		return err
	}

	order, err := worker.cycleOrder(ctx, queries, attempt, settings)
	if err != nil {
		return worker.fail(ctx, queries, attempt, orderFailureCode(err))
	}
	switch cycleOrderDisposition(order.State) {
	case orderSettled:
		// The wallet covered it, or an earlier attempt's payment settled while
		// this one was waiting.
		return worker.succeed(ctx, queries, attempt, order)
	case orderClosed:
		// The cycle's one order closed without a payment, so nothing can be
		// charged against it. This is a failure of the cycle rather than of
		// the method, and the dunning schedule runs its course.
		return worker.fail(ctx, queries, attempt, "order_"+order.State)
	}

	// An outstanding payment means the provider was asked and has not answered.
	// Poll it; if it still has not settled, wait rather than charge again.
	open, err := queries.ListOpenIntentsForOrder(ctx, order.ID)
	if err != nil {
		return err
	}
	if len(open) > 0 {
		return worker.resume(ctx, queries, attempt, open[0])
	}

	return worker.submit(ctx, queries, attempt, settings, order)
}

// orderDisposition is what the cycle order's state means for an attempt.
type orderDisposition int

const (
	// orderOpen means the order can still be paid, so the attempt charges.
	orderOpen orderDisposition = iota
	// orderSettled means the cycle is already paid for.
	orderSettled
	// orderClosed means the order ended without a payment and cannot take one.
	orderClosed
)

// cycleOrderDisposition classifies the cycle order's state.
func cycleOrderDisposition(state string) orderDisposition {
	switch state {
	case "paid", "fulfilled", "partially_refunded", "refunded":
		return orderSettled
	case "expired", "cancelled":
		return orderClosed
	default:
		return orderOpen
	}
}

// cycleOrder finds or opens the single order this renewal cycle settles.
//
// The idempotency key is the cycle key, so every attempt on a cycle converges
// on one order. That is the difference between a retried renewal and a second
// period the customer did not ask for.
func (worker *Worker) cycleOrder(
	ctx context.Context, queries *dbgen.Queries,
	attempt dbgen.DunningAttempt, settings dbgen.AutoRenewSetting,
) (dbgen.Order, error) {
	key := "renewal:" + attempt.CycleKey
	existing, err := queries.GetCycleOrder(ctx, dbgen.GetCycleOrderParams{
		UserID: attempt.UserID, IdempotencyKey: key,
	})
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Order{}, err
	}
	if !settings.PlanVersionID.Valid || !settings.Currency.Valid {
		return dbgen.Order{}, errPlanUnavailable
	}
	input := commercepg.CreateOrderInput{
		CustomerID:    uuidString(attempt.UserID),
		PlanVersionID: uuidString(settings.PlanVersionID),
		Currency:      settings.Currency.String,
		Operation:     "renewal",
		// A wallet-funded renewal spends the balance; a card-funded one leaves
		// it alone. Silently draining a customer's wallet because their card is
		// on file would be a different agreement from the one they made.
		SkipWallet:     attempt.Funding == recurring.FundingSavedMethod,
		IdempotencyKey: key,
		SubscriptionID: uuidString(attempt.SubscriptionID),
		// The order outlives the whole dunning schedule. Every attempt on the
		// cycle settles this one order, so the fourth attempt must still find
		// it payable.
		ExpiresAt: worker.clock().UTC().Add(recurring.CycleOrderLifetime()),
	}
	if attempt.Funding == recurring.FundingWallet {
		// A wallet renewal is only opened once the balance covers it. Opening
		// it short would reserve what the customer has for the whole dunning
		// window and fix the shortfall into the order, so a top-up made after
		// the first decline could never settle it. Pricing without creating
		// re-reads the balance on every attempt instead.
		quote, err := worker.orders.PreviewOrder(ctx, input)
		if err != nil {
			return dbgen.Order{}, err
		}
		if quote.ExternalMinor > 0 {
			return dbgen.Order{}, errInsufficientWallet
		}
	}
	return worker.orders.CreateOrder(ctx, input)
}

// submit charges the saved method for what the wallet did not cover.
func (worker *Worker) submit(
	ctx context.Context, queries *dbgen.Queries,
	attempt dbgen.DunningAttempt, settings dbgen.AutoRenewSetting, order dbgen.Order,
) error {
	if attempt.Funding != recurring.FundingSavedMethod {
		// A wallet-funded renewal has nothing left to charge: the order was
		// created with the balance applied, and it is still not paid.
		return worker.fail(ctx, queries, attempt, "insufficient_wallet_balance")
	}
	if !attempt.PaymentMethodID.Valid {
		return worker.fail(ctx, queries, attempt, "no_payment_method")
	}
	method, err := queries.GetPaymentMethod(ctx, attempt.PaymentMethodID)
	if err != nil {
		return worker.fail(ctx, queries, attempt, "no_payment_method")
	}
	if method.Status != "active" {
		return worker.fail(ctx, queries, attempt, "payment_method_"+method.Status)
	}
	if err := worker.allowed(ctx, queries, method); err != nil {
		return worker.fail(ctx, queries, attempt, err.Error())
	}

	// The idempotency key names the attempt, not the cycle: a retry of *this*
	// attempt must reach the same provider payment, while a later attempt is a
	// genuinely new charge against an order that is still unpaid.
	intent, err := worker.payments.CreateIntent(ctx, paymentservice.CreateIntentInput{
		OrderID:          uuidString(order.ID),
		Provider:         method.Provider,
		IdempotencyKey:   fmt.Sprintf("renewal:%s:%d", attempt.CycleKey, attempt.Attempt),
		Description:      "Subscription renewal",
		SavedMethodToken: method.ProviderToken,
		ReceiptMetadata:  map[string]any{"cycleKey": attempt.CycleKey},
	})
	if err != nil {
		worker.logger.Warn("recurring charge was refused",
			"provider", method.Provider, "cycleKey", attempt.CycleKey, "error", err)
		if markErr := worker.markMethodFailed(ctx, queries, method, err); markErr != nil {
			worker.logger.Error("payment method status update failed", "error", markErr)
		}
		return worker.fail(ctx, queries, attempt, chargeFailureCode(err))
	}
	if err := queries.TouchPaymentMethodUsed(ctx, method.ID); err != nil {
		worker.logger.Warn("payment method usage timestamp failed", "error", err)
	}
	return worker.settle(ctx, queries, attempt, intent)
}

// resume re-polls a payment that was already submitted.
func (worker *Worker) resume(
	ctx context.Context, queries *dbgen.Queries,
	attempt dbgen.DunningAttempt, intent dbgen.PaymentIntent,
) error {
	polled, err := worker.payments.Reconcile(ctx, uuidString(intent.ID))
	if err != nil {
		if errors.Is(err, payments.ErrUnsupported) {
			// The adapter cannot be polled, so settlement will arrive by
			// webhook. Waiting is the only correct action.
			return worker.defer_(ctx, queries, attempt)
		}
		return err
	}
	return worker.settle(ctx, queries, attempt, polled)
}

// settle turns a payment status into an attempt outcome.
//
// Only two statuses resolve the attempt. Everything else — processing, pending,
// awaiting confirmation — means the provider has not decided, and neither can
// this.
func (worker *Worker) settle(
	ctx context.Context, queries *dbgen.Queries,
	attempt dbgen.DunningAttempt, intent dbgen.PaymentIntent,
) error {
	switch intent.Status {
	case "succeeded":
		order, err := queries.GetOrder(ctx, intent.OrderID)
		if err != nil {
			return err
		}
		return worker.succeed(ctx, queries, attempt, order)
	case "failed", "cancelled", "expired":
		return worker.fail(ctx, queries, attempt, "declined")
	default:
		return worker.defer_(ctx, queries, attempt)
	}
}

// allowed refuses a charge the operator or the adapter has not sanctioned.
//
// Both halves are required. The adapter's declaration says the integration can
// bind a method at all; the stored capability test says this merchant account
// was granted it. Several acquirers grant recurring per merchant rather than
// per integration, so the adapter's word alone would enable charges the
// provider then rejects.
func (worker *Worker) allowed(
	ctx context.Context, queries *dbgen.Queries, method dbgen.PaymentMethod,
) error {
	adapter, configured := worker.providers[method.Provider]
	if !configured {
		return errProviderUnavailable
	}
	stored, err := queries.GetPaymentProviderSettings(ctx, dbgen.GetPaymentProviderSettingsParams{
		Provider: method.Provider, MerchantID: method.MerchantID,
	})
	if err != nil {
		return errRecurringNotEnabled
	}
	capability := recurring.Capability{
		AdapterSupports: adapter.Capabilities().Recurring,
		OperatorEnabled: stored.RecurringEnabled,
		TestStatus:      stored.RecurringTestStatus,
	}
	if !capability.Allows() {
		return errRecurringNotEnabled
	}
	return nil
}

// ---------------------------------------------------------------------------
// Outcomes
// ---------------------------------------------------------------------------

func (worker *Worker) succeed(
	ctx context.Context, queries *dbgen.Queries, attempt dbgen.DunningAttempt, order dbgen.Order,
) error {
	return worker.resolve(ctx, queries, attempt, recurring.OutcomeSucceeded, "", order.ID, false)
}

// fail records a decline and schedules whatever comes next.
func (worker *Worker) fail(
	ctx context.Context, queries *dbgen.Queries, attempt dbgen.DunningAttempt, code string,
) error {
	next := recurring.ScheduleNext(int(attempt.Attempt), worker.clock().UTC())
	outcome := recurring.OutcomeFailed
	if next.Abandon {
		outcome = recurring.OutcomeAbandoned
	}
	if err := worker.resolve(ctx, queries, attempt, outcome, code, pgtype.UUID{}, next.Notify); err != nil {
		return err
	}
	if next.Abandon {
		return nil
	}
	// The next attempt is written now rather than by a later scan, so the
	// schedule survives a worker that never comes back: everything needed to
	// resume is in the table.
	_, err := queries.ScheduleDunningAttempt(ctx, dbgen.ScheduleDunningAttemptParams{
		UserID: attempt.UserID, SubscriptionID: attempt.SubscriptionID,
		CycleKey: attempt.CycleKey, Attempt: int32(next.Attempt),
		Funding: attempt.Funding, PaymentMethodID: attempt.PaymentMethodID,
		ScheduledFor: pgtype.Timestamptz{Time: next.At, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// resolve records an outcome and moves the auto-renew state to match.
func (worker *Worker) resolve(
	ctx context.Context, queries *dbgen.Queries, attempt dbgen.DunningAttempt,
	outcome, code string, orderID pgtype.UUID, notify bool,
) error {
	if _, err := queries.ResolveDunningAttempt(ctx, dbgen.ResolveDunningAttemptParams{
		AttemptID:      attempt.ID,
		Outcome:        outcome,
		FailureCode:    optionalText(code),
		OrderID:        orderID,
		NotifyRequired: notify,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Another pass resolved it first. Its outcome stands.
			return nil
		}
		return err
	}
	// The settings row is keyed on (customer, subscription), and the match is
	// `IS NOT DISTINCT FROM` so a pre-subscription row with a null subscription
	// is still found. Leaving the subscription out matched nothing at all, and
	// the "no rows" that came back aborted the caller before it could schedule
	// the next attempt.
	_, err := queries.SetAutoRenewState(ctx, dbgen.SetAutoRenewStateParams{
		UserID:          attempt.UserID,
		SubscriptionID:  attempt.SubscriptionID,
		State:           recurring.StateAfter(outcome, outcome == recurring.OutcomeAbandoned),
		LastAttemptAt:   pgtype.Timestamptz{Time: worker.clock().UTC(), Valid: true},
		LastFailureCode: optionalText(code),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The settings row is gone. The attempt's outcome is recorded; there
		// is no state left to move.
		return nil
	}
	return err
}

// defer_ pushes an unresolved attempt out without recording an outcome.
func (worker *Worker) defer_(
	ctx context.Context, queries *dbgen.Queries, attempt dbgen.DunningAttempt,
) error {
	next := worker.clock().UTC().Add(recurring.RetryDelay(int(attempt.Attempt) + 1))
	_, err := queries.DeferDunningAttempt(ctx, dbgen.DeferDunningAttemptParams{
		AttemptID: attempt.ID, ScheduledFor: pgtype.Timestamptz{Time: next, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// markMethodFailed retires a method the provider has rejected outright.
//
// A declined charge is not a bad method — a daily limit or a temporary hold
// declines a perfectly good card — so only an explicit rejection of the token
// itself retires it. Retiring on any decline would strand customers whose bank
// simply said "not today".
func (worker *Worker) markMethodFailed(
	ctx context.Context, queries *dbgen.Queries, method dbgen.PaymentMethod, cause error,
) error {
	if !errors.Is(cause, payments.ErrUnsupported) && !isTokenRejection(cause) {
		return nil
	}
	_, err := queries.MarkPaymentMethodStatus(ctx, dbgen.MarkPaymentMethodStatusParams{
		PaymentMethodID: method.ID, Status: "failed",
	})
	return err
}

// ---------------------------------------------------------------------------
// Failure classification
// ---------------------------------------------------------------------------

var (
	errPlanUnavailable     = errors.New("plan_unavailable")
	errProviderUnavailable = errors.New("provider_unavailable")
	errRecurringNotEnabled = errors.New("recurring_not_enabled")
	errInsufficientWallet  = errors.New("insufficient_wallet_balance")
)

// orderFailureCode reduces an order-creation failure to a stable label.
//
// The customer-facing consequence differs: a retired plan version needs an
// operator, a short wallet needs the customer, an unavailable provider usually
// resolves itself, and everything else is worth a look in the log.
func orderFailureCode(err error) string {
	switch {
	case errors.Is(err, errPlanUnavailable):
		return "plan_unavailable"
	case errors.Is(err, commercepg.ErrTrialAlreadyClaimed):
		return "plan_unavailable"
	case errors.Is(err, errInsufficientWallet):
		return "insufficient_wallet_balance"
	default:
		return "order_failed"
	}
}

// chargeFailureCode reduces a provider failure to a stable, non-secret label.
func chargeFailureCode(err error) string {
	switch {
	case errors.Is(err, payments.ErrUnsupported):
		return "recurring_unsupported"
	case errors.Is(err, payments.ErrProviderResponse):
		return "declined"
	case errors.Is(err, context.DeadlineExceeded):
		return "provider_timeout"
	default:
		return "charge_failed"
	}
}

// isTokenRejection reports whether the provider rejected the stored token
// rather than the transaction.
func isTokenRejection(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "payment_method") || strings.Contains(message, "not_found")
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	value, err := id.Value()
	if err != nil || value == nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

// consentTime converts a nullable timestamp into the pointer the domain rule
// expects, so "no consent recorded" stays distinguishable from the zero time.
func consentTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	moment := value.Time.UTC()
	return &moment
}

func optionalText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}
