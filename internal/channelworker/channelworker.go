// Package channelworker verifies channel membership and applies the
// consequences.
//
// It is the only thing in Omniflow that suspends a paying customer for a reason
// unrelated to payment, so it is deliberately cautious. The rules it applies
// live in `internal/channelgate` and are unit-tested there; this package owns
// the Telegram calls, the cache, and the transaction boundaries.
package channelworker

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/channelgate"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// batchSize bounds one pass. getChatMember is rate-limited by Telegram, so a
// large installation is re-verified over many passes rather than in one burst
// that gets the bot throttled.
const batchSize = 100

// Verifier answers whether one Telegram account is a member of one chat.
//
// It is an interface so the worker can be exercised without Telegram, and so
// the implementation stays in the bot process where the token lives.
type Verifier interface {
	// IsMember reports membership. An error means the question could not be
	// answered, which is a different thing from the answer being "no" — the
	// caller records `unknown` rather than absence.
	IsMember(ctx context.Context, chatID, telegramID int64) (bool, error)
}

// Config tunes the loop. The zero value is the documented default.
type Config struct {
	Interval time.Duration
	// Enforcer takes access away and gives it back. Nil records and warns but
	// never suspends, which is the only safe default for a worker that could
	// otherwise disable a paying customer on a misconfiguration.
	Enforcer Enforcer
	// Notifier tells the customer. Nil keeps the decision in the database.
	Notifier Notifier
}

// Worker re-verifies membership and applies the grace rules.
type Worker struct {
	pool     *pgxpool.Pool
	verifier Verifier
	logger   *slog.Logger
	config   Config
	clock    func() time.Time
}

// New builds the worker. A nil verifier disables it, which is what an
// installation with no bot token gets.
func New(
	pool *pgxpool.Pool, verifier Verifier, logger *slog.Logger, config Config,
) *Worker {
	if config.Interval <= 0 {
		config.Interval = 10 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		pool: pool, verifier: verifier, logger: logger, config: config, clock: time.Now,
	}
}

// Run verifies until the context is cancelled.
func (worker *Worker) Run(ctx context.Context) {
	if worker.verifier == nil {
		worker.logger.Info("channel verification disabled", "reason", "no Telegram verifier")
		return
	}
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

// RunOnce re-verifies a batch and applies whatever the results mean.
func (worker *Worker) RunOnce(ctx context.Context) {
	queries := dbgen.New(worker.pool)

	channels, err := queries.ListEnabledChannels(ctx)
	if err != nil {
		worker.logger.Error("required channel lookup failed", "error", err)
		return
	}
	if len(channels) == 0 {
		// Nothing is required, so nothing is enforced. An installation that
		// turns the mechanism off should stop suspending people immediately
		// rather than at the next configuration read.
		return
	}

	settings, err := queries.ChannelGateSettings(ctx)
	grace, recheck := channelgate.DefaultGrace, channelgate.DefaultRecheck
	if err == nil {
		grace = time.Duration(settings.ChannelGraceSeconds) * time.Second
		recheck = time.Duration(settings.ChannelRecheckSeconds) * time.Second
	}

	now := worker.clock().UTC()
	customers, err := queries.ListCustomersForChannelRecheck(ctx,
		dbgen.ListCustomersForChannelRecheckParams{
			StaleBefore: pgtype.Timestamptz{
				Time: channelgate.StaleBefore(now, recheck), Valid: true,
			},
			PageSize: batchSize,
		})
	if err != nil {
		worker.logger.Error("channel recheck lookup failed", "error", err)
		return
	}

	for _, customer := range customers {
		if ctx.Err() != nil {
			return
		}
		if err := worker.verify(ctx, queries, customer, channels, grace, now); err != nil {
			worker.logger.Error("channel verification failed",
				"customerId", uuidString(customer.UserID), "error", err)
		}
	}
}

// verify checks one customer against every enabled channel.
func (worker *Worker) verify(
	ctx context.Context, queries *dbgen.Queries,
	customer dbgen.ListCustomersForChannelRecheckRow,
	channels []dbgen.RequiredChannel, grace time.Duration, now time.Time,
) error {
	memberships := make([]channelgate.Membership, 0, len(channels))
	for _, channel := range channels {
		state := channelgate.StateUnknown
		member, err := worker.verifier.IsMember(ctx, channel.TelegramChatID, customer.TelegramID.Int64)
		switch {
		case err != nil:
			// Telegram could not answer. `unknown` is recorded rather than
			// absence, so an outage never suspends anybody.
			worker.logger.Warn("membership check failed",
				"chatId", channel.TelegramChatID, "error", err)
		case member:
			state = channelgate.StateMember
		default:
			state = channelgate.StateAbsent
		}
		if _, err = queries.RecordMembership(ctx, dbgen.RecordMembershipParams{
			UserID: customer.UserID, ChannelID: channel.ID, State: state,
		}); err != nil {
			return err
		}
		memberships = append(memberships, channelgate.Membership{
			ChannelID: uuidString(channel.ID), State: state,
		})
	}

	exempt, err := queries.IsChannelExempt(ctx, customer.UserID)
	if err != nil {
		return err
	}

	rules := make([]channelgate.Channel, 0, len(channels))
	for _, channel := range channels {
		rules = append(rules, channelgate.Channel{
			ID: uuidString(channel.ID), Enabled: channel.Enabled,
			RequireForPurchase:   channel.RequireForPurchase,
			RequireForActivation: channel.RequireForActivation,
		})
	}
	// Only the activation requirement can suspend. Gating a purchase asks
	// somebody to join before they pay and never takes anything away.
	status := channelgate.Evaluate(rules, memberships, channelgate.PurposeActivation, exempt)

	current := channelgate.Compliant
	var graceUntil *time.Time
	if enforcement, err := queries.GetChannelEnforcement(ctx, customer.UserID); err == nil {
		current = enforcement.State
		if enforcement.GraceUntil.Valid {
			until := enforcement.GraceUntil.Time
			graceUntil = &until
		}
	} else if err != pgx.ErrNoRows {
		return err
	}

	transition := channelgate.Next(status, current, graceUntil, grace, now)
	if !transition.Changed && !transition.Warn {
		return nil
	}

	var until pgtype.Timestamptz
	if transition.GraceUntil != nil {
		until = pgtype.Timestamptz{Time: *transition.GraceUntil, Valid: true}
	}
	if _, err = queries.SetChannelEnforcement(ctx, dbgen.SetChannelEnforcementParams{
		UserID: customer.UserID, State: transition.State,
		Warn: transition.Warn, GraceUntil: until, Restore: transition.Restore,
	}); err != nil {
		return err
	}

	// The consequential half. Suspension and restoration go through the
	// ordinary entitlement path rather than writing Remnawave directly, so a
	// customer suspended for leaving a channel carries the same history as one
	// suspended for anything else. The enforcement row is already written: a
	// consequence that fails is retried on the next pass because the state it
	// records is what the next decision is made against, and it is logged so
	// an operator can see a customer the worker decided about but could not
	// act on.
	if transition.Suspend {
		worker.logger.Info("channel membership lapsed",
			"customerId", uuidString(customer.UserID), "missing", len(status.Missing))
		if worker.config.Enforcer != nil {
			if err := worker.config.Enforcer.Suspend(ctx, customer.UserID, now); err != nil {
				worker.logger.Error("channel suspension failed",
					"customerId", uuidString(customer.UserID), "error", err)
			}
		}
	}
	if transition.Restore {
		worker.logger.Info("channel membership restored",
			"customerId", uuidString(customer.UserID))
		if worker.config.Enforcer != nil {
			if err := worker.config.Enforcer.Restore(ctx, customer.UserID, now); err != nil {
				worker.logger.Error("channel restoration failed",
					"customerId", uuidString(customer.UserID), "error", err)
			}
		}
	}
	worker.notify(ctx, customer, channels, status, transition)
	return nil
}

// notify tells the customer what just happened to them, once per change.
//
// A warning names the channels and the deadline; a suspension says access is
// off and how to get it back; a restoration says it is back. Nothing is sent
// when nothing changed, and a delivery failure is logged rather than returned,
// because the decision has been recorded and the message is not what makes it
// true.
func (worker *Worker) notify(
	ctx context.Context, customer dbgen.ListCustomersForChannelRecheckRow,
	channels []dbgen.RequiredChannel, status channelgate.Status, transition channelgate.Transition,
) {
	if worker.config.Notifier == nil {
		return
	}
	event := ChannelEvent{
		CustomerID: uuidString(customer.UserID), TelegramID: customer.TelegramID.Int64,
		GraceUntil: transition.GraceUntil,
	}
	switch {
	case transition.Suspend:
		event.Kind = EventSuspended
	case transition.Warn:
		event.Kind = EventWarned
	case transition.Restore:
		event.Kind = EventRestored
	default:
		return
	}
	missing := make(map[string]bool, len(status.Missing))
	for _, id := range status.Missing {
		missing[id] = true
	}
	for _, channel := range channels {
		if !missing[uuidString(channel.ID)] {
			continue
		}
		invite := ""
		switch {
		case channel.InviteUrl.Valid && channel.InviteUrl.String != "":
			invite = channel.InviteUrl.String
		case channel.Username.Valid && channel.Username.String != "":
			invite = "https://t.me/" + channel.Username.String
		}
		event.Missing = append(event.Missing, MissingChannel{Title: channel.Title, InviteURL: invite})
	}
	if err := worker.config.Notifier.NotifyChannelEvent(ctx, event); err != nil {
		worker.logger.Warn("channel notice delivery failed",
			"customerId", event.CustomerID, "kind", event.Kind, "error", err)
	}
}

func uuidString(id pgtype.UUID) string {
	value, err := id.Value()
	if err != nil || value == nil {
		return ""
	}
	text, _ := value.(string)
	return text
}
