package panelpg

import (
	"context"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/gifts"
)

// ---------------------------------------------------------------------------
// Gifts
// ---------------------------------------------------------------------------

// Gift is one gift as the operator register shows it.
//
// The claim code is absent and unreachable: only its digest is stored. `CodeHint`
// is the last four characters, which lets a support operator confirm they are
// looking at the gift a sender is asking about without being able to redeem it.
type Gift struct {
	ID            string     `json:"id"`
	OrderID       string     `json:"orderId"`
	SenderID      string     `json:"senderId"`
	RecipientID   string     `json:"recipientId,omitempty"`
	Kind          string     `json:"kind"`
	Currency      string     `json:"currency"`
	CreditMinor   *int64     `json:"creditMinor,omitempty"`
	CodeHint      string     `json:"codeHint"`
	Status        string     `json:"status"`
	ClaimAttempts int32      `json:"claimAttempts"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	ClaimedAt     *time.Time `json:"claimedAt,omitempty"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
	RevokeReason  string     `json:"revokeReason,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

// GiftFilter is what the gift register accepts.
type GiftFilter struct {
	Status   string
	Kind     string
	SenderID string
	Cursor   string
	PageSize int32
}

// GiftPage is one page of the gift register.
type GiftPage struct {
	Items      []Gift `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// SearchGifts reads the gift register.
func (service *Service) SearchGifts(ctx context.Context, filter GiftFilter) (GiftPage, error) {
	size := pageSize(filter.PageSize)
	cursor := DecodeCursor(filter.Cursor)

	rows, err := service.queries().SearchGifts(ctx, dbgen.SearchGiftsParams{
		CursorCreatedAt: cursor.timestamp(),
		CursorID:        cursor.uuid(),
		Status:          optionalText(filter.Status),
		Kind:            optionalText(filter.Kind),
		SenderUserID:    optionalUUID(filter.SenderID),
		PageSize:        size + 1,
	})
	if err != nil {
		return GiftPage{}, err
	}

	page := GiftPage{Items: make([]Gift, 0, min(len(rows), int(size)))}
	for index, row := range rows {
		if index == int(size) {
			last := rows[index-1]
			page.NextCursor = EncodeCursor(timeValue(last.CreatedAt), uuidString(last.ID))
			break
		}
		page.Items = append(page.Items, giftFrom(row))
	}
	return page, nil
}

// GiftTotals counts gifts by status for the register header.
func (service *Service) GiftTotals(ctx context.Context) (map[string]int64, error) {
	rows, err := service.queries().CountGiftsByStatus(ctx)
	if err != nil {
		return nil, err
	}
	totals := make(map[string]int64, len(rows))
	for _, row := range rows {
		totals[row.Status] = row.Total
	}
	return totals, nil
}

// RevokeGift reclaims a gift that has not been redeemed.
//
// A claimed gift is deliberately not revocable. The recipient already holds
// what it bought, and taking that back is a refund decision made against the
// entitlement or the ledger, with its own permission and its own record —
// which is why this returns a refusal rather than quietly doing nothing.
func (service *Service) RevokeGift(ctx context.Context, giftID string, actor Actor) (Gift, error) {
	if strings.TrimSpace(actor.Reason) == "" || actor.AdminID == "" {
		return Gift{}, ErrValidaton
	}
	id, err := parseUUID(giftID)
	if err != nil {
		return Gift{}, err
	}
	admin, err := parseUUID(actor.AdminID)
	if err != nil {
		return Gift{}, err
	}

	var revoked Gift
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		// The row lock serialises this against a claim arriving at the same
		// moment: whichever commits first wins, and the other sees a state its
		// guarded update refuses.
		current, txErr := queries.LockGift(ctx, id)
		if txErr != nil {
			return notFound(txErr)
		}
		if !gifts.CanRevoke(current.Status) {
			return ErrRejected
		}

		row, txErr := queries.RevokeGift(ctx, dbgen.RevokeGiftParams{
			GiftID: id, RevokedBy: admin, RevokeReason: optionalText(actor.Reason),
		})
		if txErr != nil {
			return rejected(txErr)
		}
		revoked = giftFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.gift.revoked", "financial", "gift", giftID,
			map[string]any{"kind": row.Kind, "orderId": uuidString(row.OrderID)},
		))
	})
	return revoked, err
}

// MarkGiftRefunded records that a revoked or expired gift has been refunded to
// its sender.
//
// The refund itself goes through the payment service against the sender's
// original order, exactly like any other. Only a gift that was never redeemed
// qualifies, which is the rule that stops a sender getting their money back
// while the recipient keeps the entitlement.
func (service *Service) MarkGiftRefunded(ctx context.Context, giftID string, actor Actor) error {
	id, err := parseUUID(giftID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		current, txErr := queries.LockGift(ctx, id)
		if txErr != nil {
			return notFound(txErr)
		}
		if !gifts.RefundEligible(current.Status) {
			return ErrRejected
		}
		row, txErr := queries.MarkGiftRefunded(ctx, id)
		if txErr != nil {
			return rejected(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.gift.refunded", "financial", "gift", giftID,
			map[string]any{"orderId": uuidString(row.OrderID)},
		))
	})
}

func giftFrom(row dbgen.Gift) Gift {
	return Gift{
		ID:            uuidString(row.ID),
		OrderID:       uuidString(row.OrderID),
		SenderID:      uuidString(row.SenderUserID),
		RecipientID:   uuidString(row.RecipientUserID),
		Kind:          row.Kind,
		Currency:      row.Currency,
		CreditMinor:   int8Pointer(row.CreditMinor),
		CodeHint:      row.CodeHint,
		Status:        row.Status,
		ClaimAttempts: row.ClaimAttempts,
		ExpiresAt:     timeValue(row.ExpiresAt),
		ClaimedAt:     timePointer(row.ClaimedAt),
		RevokedAt:     timePointer(row.RevokedAt),
		RevokeReason:  textValue(row.RevokeReason),
		CreatedAt:     timeValue(row.CreatedAt),
	}
}

// ---------------------------------------------------------------------------
// Personal offers
// ---------------------------------------------------------------------------

// PersonalOffer is one offer aimed at a single customer.
type PersonalOffer struct {
	ID          string     `json:"id"`
	CustomerID  string     `json:"customerId"`
	PromotionID string     `json:"promotionId"`
	PlanID      string     `json:"planId,omitempty"`
	TitleRU     string     `json:"titleRu"`
	TitleEN     string     `json:"titleEn"`
	TermsRU     string     `json:"termsRu,omitempty"`
	TermsEN     string     `json:"termsEn,omitempty"`
	Status      string     `json:"status"`
	StartsAt    time.Time  `json:"startsAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	OrderID     string     `json:"orderId,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`
}

// OfferInput creates a targeted offer.
type OfferInput struct {
	CustomerID  string
	PromotionID string
	PlanID      string
	TitleRU     string
	TitleEN     string
	TermsRU     string
	TermsEN     string
	StartsAt    time.Time
	ExpiresAt   time.Time
}

// CreatePersonalOffer targets one customer with an existing promotion.
//
// Both locales are required. An offer with copy in only one language is an
// offer that renders as a blank card for half the customer base, and the bot
// has no sensible fallback to invent.
func (service *Service) CreatePersonalOffer(
	ctx context.Context, input OfferInput, actor Actor,
) (PersonalOffer, error) {
	if strings.TrimSpace(input.TitleRU) == "" || strings.TrimSpace(input.TitleEN) == "" {
		return PersonalOffer{}, ErrValidaton
	}
	if !input.ExpiresAt.After(input.StartsAt) {
		return PersonalOffer{}, ErrValidaton
	}
	customer, err := parseUUID(input.CustomerID)
	if err != nil {
		return PersonalOffer{}, err
	}
	promotion, err := parseUUID(input.PromotionID)
	if err != nil {
		return PersonalOffer{}, err
	}

	var offer PersonalOffer
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.CreatePersonalOffer(ctx, dbgen.CreatePersonalOfferParams{
			UserID:      customer,
			PromotionID: promotion,
			PlanID:      optionalUUID(input.PlanID),
			TitleRu:     input.TitleRU,
			TitleEn:     input.TitleEN,
			TermsRu:     input.TermsRU,
			TermsEn:     input.TermsEN,
			StartsAt:    timestamp(input.StartsAt),
			ExpiresAt:   timestamp(input.ExpiresAt),
			CreatedBy:   optionalUUID(actor.AdminID),
		})
		if txErr != nil {
			// The partial unique index on active offers is what makes a re-run
			// of a targeting job idempotent, so a conflict is "already offered"
			// rather than a failure.
			return conflicted(txErr)
		}
		offer = personalOfferFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.offer.created", "marketing", "customer", input.CustomerID,
			map[string]any{"offerId": offer.ID, "promotionId": input.PromotionID},
		))
	})
	return offer, err
}

// RevokePersonalOffer withdraws an offer that has not been redeemed.
func (service *Service) RevokePersonalOffer(ctx context.Context, offerID string, actor Actor) error {
	id, err := parseUUID(offerID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.RevokePersonalOffer(ctx, id)
		if txErr != nil {
			return rejected(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.offer.revoked", "marketing", "customer", uuidString(row.UserID),
			map[string]any{"offerId": offerID},
		))
	})
}

// OfferFilter is what the offer list accepts.
type OfferFilter struct {
	Status     string
	CustomerID string
	Cursor     string
	PageSize   int32
}

// OfferPage is one page of offers.
type OfferPage struct {
	Items      []PersonalOffer `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

// SearchPersonalOffers reads targeted offers.
func (service *Service) SearchPersonalOffers(
	ctx context.Context, filter OfferFilter,
) (OfferPage, error) {
	size := pageSize(filter.PageSize)
	cursor := DecodeCursor(filter.Cursor)

	rows, err := service.queries().SearchPersonalOffers(ctx, dbgen.SearchPersonalOffersParams{
		CursorCreatedAt: cursor.timestamp(),
		CursorID:        cursor.uuid(),
		Status:          optionalText(filter.Status),
		UserID:          optionalUUID(filter.CustomerID),
		PageSize:        size + 1,
	})
	if err != nil {
		return OfferPage{}, err
	}

	page := OfferPage{Items: make([]PersonalOffer, 0, min(len(rows), int(size)))}
	for index, row := range rows {
		if index == int(size) {
			last := rows[index-1]
			page.NextCursor = EncodeCursor(timeValue(last.CreatedAt), uuidString(last.ID))
			break
		}
		page.Items = append(page.Items, personalOfferFrom(row))
	}
	return page, nil
}

func personalOfferFrom(row dbgen.PersonalOffer) PersonalOffer {
	return PersonalOffer{
		ID:          uuidString(row.ID),
		CustomerID:  uuidString(row.UserID),
		PromotionID: uuidString(row.PromotionID),
		PlanID:      uuidString(row.PlanID),
		TitleRU:     row.TitleRu,
		TitleEN:     row.TitleEn,
		TermsRU:     row.TermsRu,
		TermsEN:     row.TermsEn,
		Status:      row.Status,
		StartsAt:    timeValue(row.StartsAt),
		ExpiresAt:   timeValue(row.ExpiresAt),
		OrderID:     uuidString(row.OrderID),
		CreatedAt:   timeValue(row.CreatedAt),
		ResolvedAt:  timePointer(row.ResolvedAt),
	}
}

// ---------------------------------------------------------------------------
// Recurring payments
// ---------------------------------------------------------------------------

// SavedMethod is a customer's stored payment method as the panel shows it.
//
// The provider token is absent. It is the credential that can move money, and
// nothing outside the payment adapter ever needs it; the masked label the
// provider supplied is what a support conversation is actually about.
type SavedMethod struct {
	ID           string     `json:"id"`
	Provider     string     `json:"provider"`
	MerchantID   string     `json:"merchantId,omitempty"`
	DisplayLabel string     `json:"displayLabel,omitempty"`
	Status       string     `json:"status"`
	IsDefault    bool       `json:"isDefault"`
	ConsentAt    time.Time  `json:"consentAt"`
	LastUsedAt   *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}

// CustomerPaymentMethods lists a customer's saved methods.
func (service *Service) CustomerPaymentMethods(
	ctx context.Context, customerID string,
) ([]SavedMethod, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListPaymentMethods(ctx, id)
	if err != nil {
		return nil, err
	}
	methods := make([]SavedMethod, 0, len(rows))
	for _, row := range rows {
		methods = append(methods, SavedMethod{
			ID:           uuidString(row.ID),
			Provider:     row.Provider,
			MerchantID:   row.MerchantID,
			DisplayLabel: row.DisplayLabel,
			Status:       row.Status,
			IsDefault:    row.IsDefault,
			ConsentAt:    timeValue(row.ConsentAt),
			LastUsedAt:   timePointer(row.LastUsedAt),
			CreatedAt:    timeValue(row.CreatedAt),
		})
	}
	return methods, nil
}

// RevokePaymentMethod removes a saved method on a customer's behalf.
//
// The row is kept, marked revoked, so a historical charge still resolves to the
// method that made it. Revoking does not cancel auto-renew: the settings row
// keeps its consent record and simply stops being chargeable, which the renewal
// worker reports as a failure the customer is told about.
func (service *Service) RevokePaymentMethod(
	ctx context.Context, customerID, methodID string, actor Actor,
) error {
	customer, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	method, err := parseUUID(methodID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.RevokePaymentMethod(ctx, dbgen.RevokePaymentMethodParams{
			PaymentMethodID: method, UserID: customer,
		})
		if txErr != nil {
			return rejected(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.payment_method.revoked", "financial", "customer", customerID,
			map[string]any{"methodId": methodID, "provider": row.Provider},
		))
	})
}

// DunningAttempt is one automatic charge attempt.
type DunningAttempt struct {
	ID             string     `json:"id"`
	CustomerID     string     `json:"customerId"`
	SubscriptionID string     `json:"subscriptionId,omitempty"`
	CycleKey       string     `json:"cycleKey"`
	Attempt        int32      `json:"attempt"`
	Funding        string     `json:"funding"`
	Outcome        string     `json:"outcome"`
	FailureCode    string     `json:"failureCode,omitempty"`
	OrderID        string     `json:"orderId,omitempty"`
	ScheduledFor   time.Time  `json:"scheduledFor"`
	OccurredAt     *time.Time `json:"occurredAt,omitempty"`
	NotifiedAt     *time.Time `json:"notifiedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	CustomerStatus string     `json:"customerStatus,omitempty"`
}

// DunningPage is one page of the failed-charge review queue.
type DunningPage struct {
	Items      []DunningAttempt `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// FailedCharges reads the recurring-payment review queue.
//
// It is a review surface, not a control surface: a failed charge is retried on
// its own schedule and then abandoned, and nothing here re-triggers one. An
// operator who wants a customer charged again asks them to renew, which is what
// abandonment already tells the customer to do.
func (service *Service) FailedCharges(
	ctx context.Context, cursor string, limit int32,
) (DunningPage, error) {
	size := pageSize(limit)
	position := DecodeCursor(cursor)

	rows, err := service.queries().ListRecentDunningFailures(ctx, dbgen.ListRecentDunningFailuresParams{
		CursorCreatedAt: position.timestamp(),
		CursorID:        position.uuid(),
		PageSize:        size + 1,
	})
	if err != nil {
		return DunningPage{}, err
	}

	page := DunningPage{Items: make([]DunningAttempt, 0, min(len(rows), int(size)))}
	for index, row := range rows {
		if index == int(size) {
			last := rows[index-1]
			page.NextCursor = EncodeCursor(
				timeValue(last.DunningAttempt.CreatedAt), uuidString(last.DunningAttempt.ID),
			)
			break
		}
		attempt := dunningAttemptFrom(row.DunningAttempt)
		attempt.CustomerStatus = row.CustomerStatus
		page.Items = append(page.Items, attempt)
	}
	return page, nil
}

// CustomerCharges lists one customer's automatic charge history.
func (service *Service) CustomerCharges(
	ctx context.Context, customerID string, limit int32,
) ([]DunningAttempt, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListDunningAttemptsForCustomer(
		ctx, dbgen.ListDunningAttemptsForCustomerParams{UserID: id, PageSize: pageSize(limit)},
	)
	if err != nil {
		return nil, err
	}
	attempts := make([]DunningAttempt, 0, len(rows))
	for _, row := range rows {
		attempts = append(attempts, dunningAttemptFrom(row))
	}
	return attempts, nil
}

func dunningAttemptFrom(row dbgen.DunningAttempt) DunningAttempt {
	return DunningAttempt{
		ID:             uuidString(row.ID),
		CustomerID:     uuidString(row.UserID),
		SubscriptionID: uuidString(row.SubscriptionID),
		CycleKey:       row.CycleKey,
		Attempt:        row.Attempt,
		Funding:        row.Funding,
		Outcome:        row.Outcome,
		FailureCode:    textValue(row.FailureCode),
		OrderID:        uuidString(row.OrderID),
		ScheduledFor:   timeValue(row.ScheduledFor),
		OccurredAt:     timePointer(row.OccurredAt),
		NotifiedAt:     timePointer(row.NotifiedAt),
		CreatedAt:      timeValue(row.CreatedAt),
	}
}
