package panelpg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Merging two customer accounts.
//
// Linking an identity to the account you are already signed in to works.
// Joining two accounts that both exist did not, and that is the case that
// occurs: a customer buys in Telegram, later signs in on the web through a
// provider the first account never carried, and holds an empty second account
// with their subscription on the other one.
//
// Forty-six tables reference `users`, so this is not "update a foreign key". It
// is a decision per category of thing a customer owns, and several of those
// decisions are refusals. The preview shows every one of them before anything
// moves, because a merge cannot be undone and an operator who has to guess what
// will happen will eventually guess wrong.

// MergeSide is what one account holds.
type MergeSide struct {
	ID                  string    `json:"id"`
	Status              string    `json:"status"`
	CreatedAt           time.Time `json:"createdAt"`
	ActiveSubscriptions int64     `json:"activeSubscriptions"`
	Orders              int64     `json:"orders"`
	Tickets             int64     `json:"tickets"`
	Identities          int64     `json:"identities"`
	ReferralsMade       int64     `json:"referralsMade"`
	TrialClaims         int64     `json:"trialClaims"`
	// Wallet is the balance per currency. It is the figure most likely to be
	// what an operator is actually being asked about.
	Wallet []MergeBalance `json:"wallet"`
	// MergedInto is set when this account has already been absorbed, which is
	// what makes a second attempt report the first merge rather than repeat it.
	MergedInto string `json:"mergedInto,omitempty"`
}

// MergeBalance is one currency's balance.
type MergeBalance struct {
	Currency     string `json:"currency"`
	BalanceMinor int64  `json:"balanceMinor"`
}

// MergeBlocker is a reason the merge cannot proceed.
//
// Stable identifiers rather than sentences: the panel renders localised copy and
// the value appears in audit metadata.
type MergeBlocker string

const (
	// BlockerSameAccount is an operator merging a customer with themselves.
	BlockerSameAccount MergeBlocker = "same_account"
	// BlockerAlreadyMerged is a source that has already been absorbed.
	BlockerAlreadyMerged MergeBlocker = "already_merged"
	// BlockerTargetMerged is a target that is itself a merged account, which
	// would leave the customer's things two hops from where anybody looks.
	BlockerTargetMerged MergeBlocker = "target_merged"
	// BlockerDeleted is an account in the retention workflow. Merging one would
	// move records out from under a deletion somebody asked for.
	BlockerDeleted MergeBlocker = "account_deleted"
	// BlockerReferralBetween is one account having referred the other. Merging
	// would make a customer their own referrer, which is a reward that was paid
	// for a signup that turns out to be the same person.
	BlockerReferralBetween MergeBlocker = "referral_between"
)

// MergePreview is what an operator is shown before deciding.
type MergePreview struct {
	Source MergeSide `json:"source"`
	Target MergeSide `json:"target"`
	// Blockers is empty when the merge can proceed. The apply path recomputes
	// them rather than trusting this, so a preview that goes stale between the
	// screen and the button cannot let a refused merge through.
	Blockers []MergeBlocker `json:"blockers"`
	// Notes are things that will happen and are worth knowing rather than
	// reasons to stop.
	Notes []MergeNote `json:"notes"`
}

// MergeNote is a consequence the operator should see coming.
type MergeNote string

const (
	// NoteCartCancelled says a saved cart on the source will be cancelled
	// rather than moved: one cart may be open per customer.
	NoteCartCancelled MergeNote = "cart_cancelled"
	// NoteTrialCarried says the merged customer counts as having claimed a
	// trial, because trial abuse controls count customers rather than accounts.
	NoteTrialCarried MergeNote = "trial_carried"
	// NoteDefaultMethodKept says the target's default payment method survives
	// and the source's methods arrive as non-default.
	NoteDefaultMethodKept MergeNote = "default_method_kept"
	// NoteSubscriptionsRenumbered says moved subscriptions take new slots.
	NoteSubscriptionsRenumbered MergeNote = "subscriptions_renumbered"
	// NoteWalletMoves says the balance moves as ledger entries rather than by
	// rewriting history.
	NoteWalletMoves MergeNote = "wallet_moves"
)

// MergeResult is what a completed merge did.
type MergeResult struct {
	Source string `json:"source"`
	Target string `json:"target"`
	// AlreadyMerged reports that this merge had happened before. The operation
	// is idempotent, so a resubmitted form is not an error and moves nothing.
	AlreadyMerged bool           `json:"alreadyMerged"`
	Moved         MergeSide      `json:"moved"`
	Wallet        []MergeBalance `json:"wallet"`
}

// MergePreview reads both accounts and works out what a merge would do.
func (service *Service) MergePreview(
	ctx context.Context, sourceID, targetID string,
) (MergePreview, error) {
	source, target, err := service.mergeSides(ctx, sourceID, targetID)
	if err != nil {
		return MergePreview{}, err
	}
	blockers, err := service.mergeBlockers(ctx, service.queries(), source, target)
	if err != nil {
		return MergePreview{}, err
	}

	preview := MergePreview{
		Source: source, Target: target, Blockers: blockers, Notes: []MergeNote{},
	}
	if len(source.Wallet) > 0 {
		preview.Notes = append(preview.Notes, NoteWalletMoves)
	}
	if source.ActiveSubscriptions > 0 && target.ActiveSubscriptions > 0 {
		preview.Notes = append(preview.Notes, NoteSubscriptionsRenumbered)
	}
	if source.TrialClaims > 0 || target.TrialClaims > 0 {
		preview.Notes = append(preview.Notes, NoteTrialCarried)
	}
	preview.Notes = append(preview.Notes, NoteCartCancelled, NoteDefaultMethodKept)
	return preview, nil
}

func (service *Service) mergeSides(
	ctx context.Context, sourceID, targetID string,
) (MergeSide, MergeSide, error) {
	source, err := service.mergeSide(ctx, sourceID)
	if err != nil {
		return MergeSide{}, MergeSide{}, err
	}
	target, err := service.mergeSide(ctx, targetID)
	if err != nil {
		return MergeSide{}, MergeSide{}, err
	}
	return source, target, nil
}

func (service *Service) mergeSide(ctx context.Context, customerID string) (MergeSide, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return MergeSide{}, ErrValidaton
	}
	row, err := service.queries().MergeCandidate(ctx, id)
	if err != nil {
		return MergeSide{}, notFound(err)
	}
	balances, err := service.queries().MergeWalletBalances(ctx, id)
	if err != nil {
		return MergeSide{}, err
	}

	side := MergeSide{
		ID: uuidString(row.ID), Status: row.Status, CreatedAt: row.CreatedAt.Time,
		ActiveSubscriptions: row.ActiveSubscriptions, Orders: row.Orders,
		Tickets: row.Tickets, Identities: row.Identities,
		ReferralsMade: row.ReferralsMade, TrialClaims: row.TrialClaims,
		MergedInto: uuidString(row.MergedInto),
		Wallet:     make([]MergeBalance, 0, len(balances)),
	}
	for _, balance := range balances {
		side.Wallet = append(side.Wallet, MergeBalance{
			Currency: balance.Currency, BalanceMinor: balance.BalanceMinor,
		})
	}
	return side, nil
}

// mergeBlockers is the single list of refusals, used by the preview and again by
// the apply path.
//
// Recomputed rather than carried from the preview, because the two are separate
// requests and an account can change between them. A merge authorised by a stale
// screen is exactly the kind of thing that only goes wrong once.
func (service *Service) mergeBlockers(
	ctx context.Context, queries *dbgen.Queries, source, target MergeSide,
) ([]MergeBlocker, error) {
	blockers := make([]MergeBlocker, 0)
	if source.ID == target.ID {
		return append(blockers, BlockerSameAccount), nil
	}
	if source.MergedInto != "" || source.Status == "merged" {
		blockers = append(blockers, BlockerAlreadyMerged)
	}
	if target.MergedInto != "" || target.Status == "merged" {
		blockers = append(blockers, BlockerTargetMerged)
	}
	if source.Status == "deleted" || target.Status == "deleted" {
		blockers = append(blockers, BlockerDeleted)
	}

	sourceID, err := parseUUID(source.ID)
	if err != nil {
		return nil, ErrValidaton
	}
	targetID, err := parseUUID(target.ID)
	if err != nil {
		return nil, ErrValidaton
	}
	between, err := queries.MergeReferralBetween(ctx, dbgen.MergeReferralBetweenParams{
		Source: sourceID, Target: targetID,
	})
	if err != nil {
		return nil, err
	}
	if between > 0 {
		blockers = append(blockers, BlockerReferralBetween)
	}
	return blockers, nil
}

// MergeCustomers moves everything transferable from source to target.
//
// One transaction, and idempotent: the closing update only matches an account
// that has not already been merged, so a resubmitted form reports the earlier
// merge and moves nothing.
func (service *Service) MergeCustomers(
	ctx context.Context, sourceID, targetID string, actor Actor,
) (MergeResult, error) {
	if actor.Reason == "" {
		// A merge is irreversible and combines two people's records. "Why" is
		// the first question anybody asks about one afterwards.
		return MergeResult{}, wrapValidation(fmt.Errorf("a merge needs a reason"))
	}
	source, target, err := service.mergeSides(ctx, sourceID, targetID)
	if err != nil {
		return MergeResult{}, err
	}

	// An already-merged source pointing at this same target is the resubmitted
	// form, and it is a success rather than a refusal.
	if source.MergedInto == target.ID {
		return MergeResult{
			Source: source.ID, Target: target.ID, AlreadyMerged: true,
		}, nil
	}

	blockers, err := service.mergeBlockers(ctx, service.queries(), source, target)
	if err != nil {
		return MergeResult{}, err
	}
	if len(blockers) > 0 {
		return MergeResult{}, wrapValidation(fmt.Errorf(
			"the accounts cannot be merged: %s", blockers[0]))
	}

	sourceUUID, err := parseUUID(source.ID)
	if err != nil {
		return MergeResult{}, ErrValidaton
	}
	targetUUID, err := parseUUID(target.ID)
	if err != nil {
		return MergeResult{}, ErrValidaton
	}

	result := MergeResult{Source: source.ID, Target: target.ID, Moved: source, Wallet: source.Wallet}
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		// Subscriptions first, because they are the only rows that need a new
		// identifier rather than a new owner: the slot is unique per customer.
		subscriptions, err := queries.ListSubscriptionsToMove(ctx, sourceUUID)
		if err != nil {
			return err
		}
		slot, err := queries.NextSubscriptionSlot(ctx, targetUUID)
		if err != nil {
			return err
		}
		for _, subscription := range subscriptions {
			if err := queries.MoveSubscription(ctx, dbgen.MoveSubscriptionParams{
				SubscriptionID: subscription.ID, Target: targetUUID, Slot: slot,
			}); err != nil {
				return err
			}
			slot++
		}

		// Everything else is a plain reassignment. Each generated call takes its
		// own parameter type, so they are listed rather than looped: the compiler
		// then refuses a table that was added to the query file and forgotten here,
		// which a map of closures over a shared type would not.
		move := []func() error{
			func() error {
				return queries.MoveCustomerEntitlements(ctx, dbgen.MoveCustomerEntitlementsParams{
					Source: sourceUUID, Target: targetUUID,
				})
			},
			func() error {
				return queries.MoveCustomerOrders(ctx, dbgen.MoveCustomerOrdersParams{
					Source: sourceUUID, Target: targetUUID,
				})
			},
			func() error {
				return queries.MoveCustomerTickets(ctx, dbgen.MoveCustomerTicketsParams{
					Source: sourceUUID, Target: targetUUID,
				})
			},
			func() error {
				return queries.MoveCustomerIdentities(ctx, dbgen.MoveCustomerIdentitiesParams{
					Source: sourceUUID, Target: targetUUID,
				})
			},
			func() error {
				return queries.MoveCustomerContacts(ctx, dbgen.MoveCustomerContactsParams{
					Source: sourceUUID, Target: targetUUID,
				})
			},
			func() error {
				return queries.MoveCustomerConsents(ctx, dbgen.MoveCustomerConsentsParams{
					Source: sourceUUID, Target: targetUUID,
				})
			},
			func() error {
				return queries.MoveCustomerTopUps(ctx, dbgen.MoveCustomerTopUpsParams{
					Source: sourceUUID, Target: targetUUID,
				})
			},
			func() error {
				return queries.MoveCustomerGoodsOrders(ctx, dbgen.MoveCustomerGoodsOrdersParams{
					Source: sourceUUID, Target: targetUUID,
				})
			},
			func() error {
				return queries.MoveCustomerPaymentMethods(ctx, dbgen.MoveCustomerPaymentMethodsParams{
					Source: sourceUUID, Target: targetUUID,
				})
			},
			func() error {
				return queries.MoveCustomerLifecycleEvents(ctx, dbgen.MoveCustomerLifecycleEventsParams{
					Source: sourceUUID, Target: targetUUID,
				})
			},
			func() error {
				return queries.MoveCustomerNewsReads(ctx, dbgen.MoveCustomerNewsReadsParams{
					Source: sourceUUID, Target: targetUUID,
				})
			},
			func() error {
				return queries.MoveCustomerSecurityEvents(ctx, dbgen.MoveCustomerSecurityEventsParams{
					Source: sourceUUID, Target: targetUUID,
				})
			},
			// The duplicates the read-marker move deliberately left behind.
			func() error { return queries.DropDuplicateNewsReads(ctx, sourceUUID) },
			func() error { return queries.CancelMergedCustomerCart(ctx, sourceUUID) },
		}

		for _, step := range move {
			if err := step(); err != nil {
				return err
			}
		}

		// The wallet moves as entries rather than by reassigning history.
		//
		// `ledger_entries` is append-only, so rewriting `user_id` on a customer's
		// financial record is not available and should not be: the entries are
		// the evidence for every balance this installation has ever reported.
		// A compensating pair — the source debited to zero, the target credited
		// the same — leaves both accounts' histories intact and arrives at the
		// right balance by addition, which is the only way a ledger is allowed
		// to change.
		for _, balance := range source.Wallet {
			if balance.BalanceMinor == 0 {
				continue
			}
			transaction, err := queries.CreateLedgerTransaction(
				ctx, dbgen.CreateLedgerTransactionParams{
					Type: "correction", ReferenceType: "customer_merge",
					ReferenceID: source.ID + ":" + balance.Currency,
					// Derived from the two accounts and the currency, so a
					// resubmitted merge reuses the transaction rather than
					// moving the balance twice.
					IdempotencyKey: "merge:" + source.ID + ":" + target.ID + ":" + balance.Currency,
					Reason:         optionalText(actor.Reason),
					ActorID:        optionalText(actor.AdminID),
				},
			)
			if err != nil {
				return err
			}
			for _, entry := range []struct {
				user   pgtype.UUID
				amount int64
			}{
				{sourceUUID, -balance.BalanceMinor},
				{targetUUID, balance.BalanceMinor},
			} {
				if _, err := queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{
					TransactionID: transaction.ID, AccountType: "customer_wallet",
					UserID: entry.user, Currency: balance.Currency,
					AmountMinor: entry.amount,
				}); err != nil {
					return err
				}
			}
		}

		if _, err := queries.CloseMergedAccount(ctx, dbgen.CloseMergedAccountParams{
			Source: sourceUUID, Target: targetUUID,
		}); err != nil {
			return notFound(err)
		}

		// Both sides get a lifecycle event, so the merge is readable from
		// either account's own history rather than only from the operator trail.
		for _, event := range []struct {
			user   pgtype.UUID
			action string
		}{
			{sourceUUID, "merged_away"},
			{targetUUID, "merged_in"},
		} {
			if _, err := queries.InsertCustomerLifecycleEvent(
				ctx, dbgen.InsertCustomerLifecycleEventParams{
					UserID: event.user, Action: event.action, Reason: actor.Reason,
					ActorType: "operator", ActorID: optionalText(actor.AdminID),
					RequestID: optionalText(actor.RequestID),
				},
			); err != nil {
				return err
			}
		}

		return appendAudit(ctx, queries, actor.audit(
			"customer.merged", "customer", "customer", source.ID,
			map[string]any{
				"into": target.ID, "subscriptions": len(subscriptions),
				"orders": source.Orders, "identities": source.Identities,
			},
		))
	})
	if err != nil {
		return MergeResult{}, err
	}
	return result, nil
}
