package panelpg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/loyalty"
)

// ReferralReview is one inviter/invitee pair as the review queue shows it.
type ReferralReview struct {
	ReferredID    string     `json:"referredCustomerId"`
	ReferrerID    string     `json:"referrerCustomerId"`
	Code          string     `json:"code"`
	State         string     `json:"reviewState"`
	Note          string     `json:"reviewNote,omitempty"`
	ReviewerName  string     `json:"reviewerName,omitempty"`
	Signals       []string   `json:"signals"`
	LiveRewards   int64      `json:"liveRewards"`
	RewardedMinor int64      `json:"rewardedMinor"`
	CreatedAt     time.Time  `json:"createdAt"`
	QualifiedAt   *time.Time `json:"qualifiedAt,omitempty"`
}

// SearchReferralReviews reads the review queue.
func (service *Service) SearchReferralReviews(
	ctx context.Context, state string, signalledOnly bool, limit int32,
) ([]ReferralReview, error) {
	rows, err := service.queries().SearchReferralAttributions(
		ctx, dbgen.SearchReferralAttributionsParams{
			ReviewState: optionalText(state), SignalledOnly: signalledOnly,
			PageSize: pageSize(limit),
		},
	)
	if err != nil {
		return nil, err
	}
	reviews := make([]ReferralReview, 0, len(rows))
	for _, row := range rows {
		review := ReferralReview{
			ReferredID: uuidString(row.ReferredUserID), ReferrerID: uuidString(row.ReferrerUserID),
			Code: row.Code, State: row.ReviewState, Note: textValue(row.ReviewNote),
			ReviewerName: row.ReviewerName, Signals: row.SignalCodes,
			LiveRewards: row.LiveRewards, RewardedMinor: row.RewardedMinor,
			CreatedAt: timeValue(row.CreatedAt),
		}
		if row.QualifiedAt.Valid {
			qualified := timeValue(row.QualifiedAt)
			review.QualifiedAt = &qualified
		}
		reviews = append(reviews, review)
	}
	return reviews, nil
}

// ReviewReferral records a person's decision about a referral pair.
//
// Rejecting reverses every live reward through a compensating ledger
// transaction rather than deleting or editing the original. The ledger is
// append-only, and "this was granted and then taken back, by this operator, for
// this reason" is a different fact from "this was never granted" — the customer
// saw the balance either way.
func (service *Service) ReviewReferral(
	ctx context.Context, referredID, state, note string, actor Actor,
) (ReferralReview, error) {
	if !validReferralReview[state] {
		return ReferralReview{}, ErrValidaton
	}
	if state == "rejected" && strings.TrimSpace(note) == "" {
		// A rejection takes money back from a customer. It requires a reason
		// somebody can quote to them.
		return ReferralReview{}, ErrValidaton
	}
	id, err := parseUUID(referredID)
	if err != nil {
		return ReferralReview{}, err
	}

	var review ReferralReview
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.SetReferralReviewState(ctx, dbgen.SetReferralReviewStateParams{
			ReferredUserID: id, ReviewState: state,
			ReviewedBy: optionalUUID(actor.AdminID), ReviewNote: optionalText(note),
		})
		if txErr != nil {
			return notFound(txErr)
		}
		review = ReferralReview{
			ReferredID: uuidString(row.ReferredUserID), ReferrerID: uuidString(row.ReferrerUserID),
			Code: row.Code, State: row.ReviewState, Note: textValue(row.ReviewNote),
			Signals: row.SignalCodes, CreatedAt: timeValue(row.CreatedAt),
		}

		if state == "rejected" {
			if txErr = service.reverseRewards(ctx, queries, id, note, actor); txErr != nil {
				return txErr
			}
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.referral.reviewed", "risk", "referral", referredID,
			map[string]any{"state": state, "note": note},
		))
	})
	return review, err
}

// reverseRewards writes the compensating entries for every live reward on a
// pair.
//
// The customer's balance can go negative as a result, and that is deliberate:
// refusing to reverse because the money has been spent would mean an abusive
// referral is profitable as long as the reward is spent quickly. A negative
// balance is visible, explicable from the ledger, and recoverable.
func (service *Service) reverseRewards(
	ctx context.Context, queries *dbgen.Queries, referredID pgtype.UUID, reason string, actor Actor,
) error {
	rewards, err := queries.ListReferralRewardsForPair(ctx, referredID)
	if err != nil {
		return err
	}
	for _, reward := range rewards {
		if reward.ReversedAt.Valid {
			continue
		}
		entries := []commerce.LedgerEntry{
			{
				AccountType: "customer_wallet", CustomerID: uuidString(reward.BeneficiaryUserID),
				Currency: reward.Currency, AmountMinor: -reward.AmountMinor,
			},
			{
				AccountType: "platform_clearing", Currency: reward.Currency,
				AmountMinor: reward.AmountMinor,
			},
		}
		if err = commerce.ValidateLedger(entries); err != nil {
			return err
		}
		transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{
			Type: "correction", ReferenceType: "referral_reward",
			ReferenceID:    uuidString(reward.ID),
			IdempotencyKey: "referral-reversal:" + uuidString(reward.ID),
			Reason:         pgtype.Text{String: "referral reward reversed: " + reason, Valid: true},
		})
		if err != nil {
			return err
		}
		if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{
			TransactionID: transaction.ID, AccountType: "customer_wallet",
			UserID: reward.BeneficiaryUserID, Currency: reward.Currency,
			AmountMinor: -reward.AmountMinor,
		}); err != nil {
			return err
		}
		if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{
			TransactionID: transaction.ID, AccountType: "platform_clearing",
			Currency: reward.Currency, AmountMinor: reward.AmountMinor,
		}); err != nil {
			return err
		}
		if _, err = queries.ReverseReferralReward(ctx, dbgen.ReverseReferralRewardParams{
			RewardID: reward.ID, ReversedBy: optionalUUID(actor.AdminID),
			ReversalReason:      pgtype.Text{String: reason, Valid: true},
			LedgerTransactionID: transaction.ID,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return nil
}

// RecordReferralSignal notes something worth a person's attention.
//
// It holds the pair for review and never rejects it. An automatic system that
// punishes customers silently is one nobody can explain to the customer it
// punished, so the heuristic's only power is to ask for a human.
func (service *Service) RecordReferralSignal(
	ctx context.Context, referredID, code string, evidence map[string]any,
) error {
	id, err := parseUUID(referredID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.RecordReferralSignal(ctx, dbgen.RecordReferralSignalParams{
			ReferredUserID: id, Code: code, Evidence: payload,
		}); txErr != nil {
			return txErr
		}
		return queries.AttachReferralSignal(ctx, dbgen.AttachReferralSignalParams{
			ReferredUserID: id, Code: code,
		})
	})
}

// LoyaltyProgram is one versioned definition.
type LoyaltyProgram struct {
	ID          string        `json:"id"`
	Version     int32         `json:"version"`
	Enabled     bool          `json:"enabled"`
	Metric      string        `json:"metric"`
	Currency    string        `json:"currency"`
	WindowDays  int32         `json:"windowDays"`
	GraceDays   int32         `json:"graceDays"`
	Tiers       []LoyaltyTier `json:"tiers"`
	PublishedAt *time.Time    `json:"publishedAt,omitempty"`
	CreatedAt   time.Time     `json:"createdAt"`
}

// LoyaltyTier is one rung of a definition.
type LoyaltyTier struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	NameEN      string `json:"nameEn"`
	NameRU      string `json:"nameRu"`
	Threshold   int64  `json:"threshold"`
	DiscountBPS int32  `json:"discountBps"`
}

// LoyaltyPrograms lists the definitions, newest version first.
func (service *Service) LoyaltyPrograms(
	ctx context.Context, limit int32,
) ([]LoyaltyProgram, error) {
	queries := service.queries()
	rows, err := queries.ListLoyaltyPrograms(ctx, pageSize(limit))
	if err != nil {
		return nil, err
	}
	programs := make([]LoyaltyProgram, 0, len(rows))
	for _, row := range rows {
		program := loyaltyProgramFrom(row)
		tiers, tierErr := queries.ListLoyaltyTiers(ctx, row.ID)
		if tierErr != nil {
			return nil, tierErr
		}
		for _, tier := range tiers {
			program.Tiers = append(program.Tiers, loyaltyTierFrom(tier))
		}
		programs = append(programs, program)
	}
	return programs, nil
}

// PublishLoyaltyProgram writes a new version and, optionally, makes it the one
// in force.
//
// There is no edit. A customer who reached gold under one set of thresholds
// should not silently fall out of it because somebody changed the numbers, so
// editing means publishing the next version — the old one keeps explaining the
// assignments made under it.
func (service *Service) PublishLoyaltyProgram(
	ctx context.Context, program LoyaltyProgram, enable bool, actor Actor,
) (LoyaltyProgram, error) {
	domain := loyalty.Program{
		Metric: program.Metric,
		Window: time.Duration(program.WindowDays) * 24 * time.Hour,
		Grace:  time.Duration(program.GraceDays) * 24 * time.Hour,
	}
	for _, tier := range program.Tiers {
		domain.Tiers = append(domain.Tiers, loyalty.Tier{
			Code: tier.Code, Threshold: tier.Threshold, DiscountBPS: int(tier.DiscountBPS),
		})
	}
	// The domain refuses what a CHECK constraint cannot express: a definition
	// with no floor tier, and a higher tier worth less than a lower one.
	if err := loyalty.Validate(domain); err != nil {
		return LoyaltyProgram{}, ErrValidaton
	}

	var saved LoyaltyProgram
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		version, txErr := queries.NextLoyaltyVersion(ctx)
		if txErr != nil {
			return txErr
		}
		row, txErr := queries.CreateLoyaltyProgram(ctx, dbgen.CreateLoyaltyProgramParams{
			Version: version, Metric: program.Metric, Currency: program.Currency,
			WindowDays: program.WindowDays, GraceDays: program.GraceDays,
			CreatedBy: optionalUUID(actor.AdminID),
		})
		if txErr != nil {
			return txErr
		}
		saved = loyaltyProgramFrom(row)
		for index, tier := range program.Tiers {
			created, tierErr := queries.CreateLoyaltyTier(ctx, dbgen.CreateLoyaltyTierParams{
				ProgramID: row.ID, Code: strings.ToLower(strings.TrimSpace(tier.Code)),
				NameEn: tier.NameEN, NameRu: tier.NameRU,
				Threshold: tier.Threshold, DiscountBps: tier.DiscountBPS,
				SortOrder: int32(index),
			})
			if tierErr != nil {
				return tierErr
			}
			saved.Tiers = append(saved.Tiers, loyaltyTierFrom(created))
		}
		if enable {
			if _, txErr = queries.PublishLoyaltyProgram(ctx, row.ID); txErr != nil {
				return txErr
			}
			saved.Enabled = true
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.loyalty_program.published", "configuration", "loyalty_program", saved.ID,
			map[string]any{
				"version": version, "metric": program.Metric,
				"tiers": len(program.Tiers), "enabled": enable,
			},
		))
	})
	return saved, err
}

// LoyaltyStanding is where one customer stands.
type LoyaltyStanding struct {
	TierCode    string     `json:"tierCode"`
	NameEN      string     `json:"nameEn"`
	NameRU      string     `json:"nameRu"`
	DiscountBPS int32      `json:"discountBps"`
	Metric      int64      `json:"evaluatedMetric"`
	EvaluatedAt time.Time  `json:"evaluatedAt"`
	GraceUntil  *time.Time `json:"graceUntil,omitempty"`
}

// EvaluateLoyalty places one customer under the definition in force.
//
// It is idempotent: re-running it writes history only when the standing
// actually changed, so a sweep that runs every hour does not produce a history
// entry every hour.
func (service *Service) EvaluateLoyalty(
	ctx context.Context, customerID string, now time.Time, actor Actor,
) (LoyaltyStanding, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return LoyaltyStanding{}, err
	}

	var standing LoyaltyStanding
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		program, txErr := queries.GetEnabledLoyaltyProgram(ctx)
		if errors.Is(txErr, pgx.ErrNoRows) {
			// No programme in force is a normal state, not an error: loyalty is
			// off until an operator publishes one.
			return nil
		}
		if txErr != nil {
			return txErr
		}
		tiers, txErr := queries.ListLoyaltyTiers(ctx, program.ID)
		if txErr != nil {
			return txErr
		}
		domain := loyalty.Program{
			ID: uuidString(program.ID), Version: int(program.Version), Metric: program.Metric,
			Window: time.Duration(program.WindowDays) * 24 * time.Hour,
			Grace:  time.Duration(program.GraceDays) * 24 * time.Hour,
		}
		byID := make(map[string]dbgen.LoyaltyTier, len(tiers))
		for _, tier := range tiers {
			domain.Tiers = append(domain.Tiers, loyalty.Tier{
				ID: uuidString(tier.ID), Code: tier.Code,
				Threshold: tier.Threshold, DiscountBPS: int(tier.DiscountBps),
			})
			byID[uuidString(tier.ID)] = tier
		}

		metric, txErr := service.loyaltyMetric(ctx, queries, id, program)
		if txErr != nil {
			return txErr
		}

		current := loyalty.Standing{}
		existing, existingErr := queries.GetLoyaltyStanding(ctx, id)
		if existingErr == nil {
			current.TierID = uuidString(existing.LoyaltyStanding.TierID)
			if existing.LoyaltyStanding.GraceUntil.Valid {
				grace := timeValue(existing.LoyaltyStanding.GraceUntil)
				current.GraceUntil = &grace
			}
		} else if !errors.Is(existingErr, pgx.ErrNoRows) {
			return existingErr
		}

		next, txErr := loyalty.Evaluate(domain, current, metric, now)
		if txErr != nil {
			return txErr
		}
		tierID, parseErr := parseUUID(next.TierID)
		if parseErr != nil {
			return parseErr
		}
		var graceUntil pgtype.Timestamptz
		if next.GraceUntil != nil {
			graceUntil = pgtype.Timestamptz{Time: *next.GraceUntil, Valid: true}
		}
		if _, txErr = queries.UpsertLoyaltyStanding(ctx, dbgen.UpsertLoyaltyStandingParams{
			UserID: id, ProgramID: program.ID, TierID: tierID,
			EvaluatedMetric: metric, GraceUntil: graceUntil,
		}); txErr != nil {
			return txErr
		}
		if next.Changed {
			if _, txErr = queries.RecordLoyaltyChange(ctx, dbgen.RecordLoyaltyChangeParams{
				UserID: id, FromTierID: optionalUUID(current.TierID), ToTierID: tierID,
				EvaluatedMetric: metric, Reason: "evaluation",
				ActorID: optionalUUID(actor.AdminID),
			}); txErr != nil {
				return txErr
			}
		}
		tier := byID[next.TierID]
		standing = LoyaltyStanding{
			TierCode: tier.Code, NameEN: tier.NameEn, NameRU: tier.NameRu,
			DiscountBPS: tier.DiscountBps, Metric: metric, EvaluatedAt: now,
			GraceUntil: next.GraceUntil,
		}
		return nil
	})
	return standing, err
}

// loyaltyMetric reads the number the standing is decided on.
func (service *Service) loyaltyMetric(
	ctx context.Context, queries *dbgen.Queries, id pgtype.UUID, program dbgen.LoyaltyProgram,
) (int64, error) {
	row, err := queries.CustomerLoyaltyMetric(ctx, dbgen.CustomerLoyaltyMetricParams{
		UserID: id, Currency: program.Currency, WindowDays: program.WindowDays,
	})
	if err != nil {
		return 0, err
	}
	switch program.Metric {
	case loyalty.MetricOrders:
		return row.OrderCount, nil
	case loyalty.MetricTenure:
		return row.TenureDays, nil
	default:
		return row.SpendMinor, nil
	}
}

// CustomerLoyalty reads one customer's standing and how it changed.
func (service *Service) CustomerLoyalty(
	ctx context.Context, customerID string, limit int32,
) (LoyaltyStanding, []LoyaltyChange, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return LoyaltyStanding{}, nil, err
	}
	queries := service.queries()

	var standing LoyaltyStanding
	row, err := queries.GetLoyaltyStanding(ctx, id)
	if err == nil {
		standing = LoyaltyStanding{
			TierCode: row.TierCode, NameEN: row.NameEn, NameRU: row.NameRu,
			DiscountBPS: row.DiscountBps,
			Metric:      row.LoyaltyStanding.EvaluatedMetric,
			EvaluatedAt: timeValue(row.LoyaltyStanding.EvaluatedAt),
		}
		if row.LoyaltyStanding.GraceUntil.Valid {
			grace := timeValue(row.LoyaltyStanding.GraceUntil)
			standing.GraceUntil = &grace
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return LoyaltyStanding{}, nil, err
	}

	rows, err := queries.ListLoyaltyHistory(ctx, dbgen.ListLoyaltyHistoryParams{
		UserID: id, PageSize: pageSize(limit),
	})
	if err != nil {
		return LoyaltyStanding{}, nil, err
	}
	history := make([]LoyaltyChange, 0, len(rows))
	for _, change := range rows {
		history = append(history, LoyaltyChange{
			From: change.FromCode, To: change.ToCode,
			Metric: change.EvaluatedMetric, Reason: change.Reason,
			OccurredAt: timeValue(change.OccurredAt),
		})
	}
	return standing, history, nil
}

// LoyaltyChange is one recorded movement between tiers.
type LoyaltyChange struct {
	From       string    `json:"fromTier,omitempty"`
	To         string    `json:"toTier"`
	Metric     int64     `json:"evaluatedMetric"`
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurredAt"`
}

var validReferralReview = map[string]bool{"clear": true, "held": true, "rejected": true}

func loyaltyProgramFrom(row dbgen.LoyaltyProgram) LoyaltyProgram {
	program := LoyaltyProgram{
		ID: uuidString(row.ID), Version: row.Version, Enabled: row.Enabled,
		Metric: row.Metric, Currency: row.Currency,
		WindowDays: row.WindowDays, GraceDays: row.GraceDays,
		Tiers: []LoyaltyTier{}, CreatedAt: timeValue(row.CreatedAt),
	}
	if row.PublishedAt.Valid {
		published := timeValue(row.PublishedAt)
		program.PublishedAt = &published
	}
	return program
}

func loyaltyTierFrom(row dbgen.LoyaltyTier) LoyaltyTier {
	return LoyaltyTier{
		ID: uuidString(row.ID), Code: row.Code, NameEN: row.NameEn, NameRU: row.NameRu,
		Threshold: row.Threshold, DiscountBPS: row.DiscountBps,
	}
}
