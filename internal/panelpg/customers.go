package panelpg

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// CustomerFilter is what the panel's customer search accepts.
//
// There is deliberately no free-text field. Contact values are stored as
// ciphertext plus a fingerprint precisely so they cannot be trawled, and a
// substring search over customer records is the query that turns a support
// account into a bulk-export account.
type CustomerFilter struct {
	Status string
	// Segment is one of "subscribed", "lapsed", "never_purchased", "flagged".
	Segment string
	// Query is matched against the identifiers an operator may safely hold: a
	// customer identifier, a Telegram identifier, or a Remnawave username. Which
	// one it is is decided by its shape, so the operator pastes what they have.
	Query    string
	Cursor   string
	PageSize int32
}

// CustomerSummary is one search result.
type CustomerSummary struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	Locale      string     `json:"locale"`
	Timezone    string     `json:"timezone"`
	CreatedAt   time.Time  `json:"createdAt"`
	TelegramID  *int64     `json:"telegramId,omitempty"`
	SuspendedAt *time.Time `json:"suspendedAt,omitempty"`
	DeletedAt   *time.Time `json:"deletedAt,omitempty"`
}

// CustomerPage is one page of search results.
type CustomerPage struct {
	Items      []CustomerSummary `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

// SearchCustomers finds customers by safe identifier and segment.
func (service *Service) SearchCustomers(ctx context.Context, filter CustomerFilter) (CustomerPage, error) {
	size := pageSize(filter.PageSize)
	cursor := DecodeCursor(filter.Cursor)

	params := dbgen.SearchCustomersParams{
		CursorCreatedAt: cursor.timestamp(),
		CursorID:        cursor.uuid(),
		Status:          optionalText(filter.Status),
		Segment:         optionalText(filter.Segment),
		// One more than the page, so "is there a next page" is answered by the
		// result set rather than by a second count query.
		PageSize: size + 1,
	}
	applyCustomerQuery(&params, filter.Query)

	rows, err := service.queries().SearchCustomers(ctx, params)
	if err != nil {
		return CustomerPage{}, err
	}

	page := CustomerPage{Items: make([]CustomerSummary, 0, min(len(rows), int(size)))}
	for index, row := range rows {
		if index == int(size) {
			last := rows[index-1]
			page.NextCursor = EncodeCursor(timeValue(last.User.CreatedAt), uuidString(last.User.ID))
			break
		}
		page.Items = append(page.Items, CustomerSummary{
			ID:          uuidString(row.User.ID),
			Status:      row.User.Status,
			Locale:      row.User.Locale,
			Timezone:    row.User.Timezone,
			CreatedAt:   timeValue(row.User.CreatedAt),
			TelegramID:  int8Pointer(row.TelegramID),
			SuspendedAt: timePointer(row.User.SuspendedAt),
			DeletedAt:   timePointer(row.User.DeletedAt),
		})
	}
	return page, nil
}

// applyCustomerQuery decides which identifier the operator pasted.
//
// The shapes are unambiguous — a UUID, a run of digits, or a username — so
// guessing is safe and saves the operator having to say which field they are
// searching. A value matching none of them sets a username filter that will
// simply find nothing, which is the honest answer to a search for something
// that cannot be an identifier.
func applyCustomerQuery(params *dbgen.SearchCustomersParams, query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}
	if id, err := parseUUID(query); err == nil && id.Valid {
		params.CustomerID = id
		return
	}
	if telegramID, err := strconv.ParseInt(query, 10, 64); err == nil && telegramID > 0 {
		params.TelegramID = pgtype.Int8{Int64: telegramID, Valid: true}
		return
	}
	params.Username = optionalText(strings.TrimPrefix(query, "@"))
}

// CustomerProfile is the profile header: who the customer is, plus the counts
// the page shows before any tab is opened.
type CustomerProfile struct {
	CustomerSummary
	AnonymizedAt        *time.Time `json:"anonymizedAt,omitempty"`
	RetentionUntil      *time.Time `json:"retentionUntil,omitempty"`
	ActiveSubscriptions int64      `json:"activeSubscriptions"`
	OrderCount          int64      `json:"orderCount"`
	OpenTickets         int64      `json:"openTickets"`
	ReferralCount       int64      `json:"referralCount"`
	// OpenFlags is how many blocklist matches are awaiting an operator decision.
	// It is a count of things to review, never a verdict about the customer.
	OpenFlags int64 `json:"openFlags"`
	// Allowlisted records that an operator has already decided this customer is
	// fine, which is what stops the next list refresh re-raising the same match.
	Allowlisted bool `json:"allowlisted"`
}

// CustomerProfile reads the header for one customer.
func (service *Service) CustomerProfile(ctx context.Context, customerID string) (CustomerProfile, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return CustomerProfile{}, err
	}
	row, err := service.queries().GetCustomerOverview(ctx, id)
	if err != nil {
		return CustomerProfile{}, notFound(err)
	}
	return CustomerProfile{
		CustomerSummary: CustomerSummary{
			ID:          uuidString(row.User.ID),
			Status:      row.User.Status,
			Locale:      row.User.Locale,
			Timezone:    row.User.Timezone,
			CreatedAt:   timeValue(row.User.CreatedAt),
			TelegramID:  int8Pointer(row.TelegramID),
			SuspendedAt: timePointer(row.User.SuspendedAt),
			DeletedAt:   timePointer(row.User.DeletedAt),
		},
		AnonymizedAt:        timePointer(row.User.AnonymizedAt),
		RetentionUntil:      timePointer(row.User.RetentionUntil),
		ActiveSubscriptions: row.ActiveSubscriptions,
		OrderCount:          row.OrderCount,
		OpenTickets:         row.OpenTickets,
		ReferralCount:       row.ReferralCount,
		OpenFlags:           row.OpenFlags,
		Allowlisted:         row.Allowlisted,
	}, nil
}

// SubscriptionDetail is one of a customer's concurrent subscriptions together
// with the entitlement currently governing it.
//
// Every field that describes the entitlement is optional, because a
// subscription slot can exist before it has ever been provisioned — that is
// what a purchase whose fulfillment has not run yet looks like.
type SubscriptionDetail struct {
	ID     string `json:"id"`
	Slot   int32  `json:"slot"`
	Label  string `json:"label"`
	Status string `json:"status"`
	// RemnawaveUserID is the mapping this subscription owns. It is shown so an
	// operator can correlate with the Remnawave panel; the username is shown
	// beside it for the same reason.
	RemnawaveUserID   *int64     `json:"remnawaveUserId,omitempty"`
	RemnawaveUsername string     `json:"remnawaveUsername,omitempty"`
	ReconciledAt      *time.Time `json:"reconciledAt,omitempty"`

	EntitlementID     string     `json:"entitlementId,omitempty"`
	EntitlementStatus string     `json:"entitlementStatus,omitempty"`
	StartsAt          *time.Time `json:"startsAt,omitempty"`
	EndsAt            *time.Time `json:"endsAt,omitempty"`
	TrafficAllowance  *int64     `json:"trafficAllowanceBytes,omitempty"`
	DeviceLimit       *int32     `json:"deviceLimit,omitempty"`
	SquadIDs          []string   `json:"squadIds,omitempty"`
	PlanCode          string     `json:"planCode,omitempty"`
	PlanVersion       *int32     `json:"planVersion,omitempty"`
}

// CustomerSubscriptions lists every subscription a customer holds.
func (service *Service) CustomerSubscriptions(
	ctx context.Context, customerID string,
) ([]SubscriptionDetail, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListCustomerSubscriptionsDetailed(ctx, id)
	if err != nil {
		return nil, err
	}

	details := make([]SubscriptionDetail, 0, len(rows))
	for _, row := range rows {
		detail := SubscriptionDetail{
			ID:                uuidString(row.Subscription.ID),
			Slot:              row.Subscription.Slot,
			Label:             row.Subscription.Label,
			Status:            row.Subscription.Status,
			RemnawaveUserID:   int8Pointer(row.Subscription.RemnawaveUserID),
			RemnawaveUsername: textValue(row.Subscription.RemnawaveUsername),
			ReconciledAt:      timePointer(row.Subscription.ReconciledAt),
			EntitlementID:     uuidString(row.EntitlementID),
			EntitlementStatus: row.EntitlementStatus,
			StartsAt:          timePointer(row.EntitlementStartsAt),
			EndsAt:            timePointer(row.EntitlementEndsAt),
			TrafficAllowance:  int8Pointer(row.TrafficAllowanceBytes),
			DeviceLimit:       int4Pointer(row.DeviceLimit),
			SquadIDs:          uuidStrings(row.RemnawaveSquadIds),
			PlanCode:          textValue(row.PlanCode),
			PlanVersion:       int4Pointer(row.PlanVersion),
		}
		details = append(details, detail)
	}
	return details, nil
}

// OrderSummary is one order as the panel lists it.
type OrderSummary struct {
	ID             string    `json:"id"`
	State          string    `json:"state"`
	Operation      string    `json:"operation"`
	Currency       string    `json:"currency"`
	SubtotalMinor  int64     `json:"subtotalMinor"`
	DiscountMinor  int64     `json:"discountMinor"`
	WalletMinor    int64     `json:"walletMinor"`
	ExternalMinor  int64     `json:"externalMinor"`
	PaidMinor      int64     `json:"paidMinor"`
	RefundedMinor  int64     `json:"refundedMinor"`
	CustomerID     string    `json:"customerId"`
	SubscriptionID string    `json:"subscriptionId,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// CustomerOrders lists a customer's order history, newest first.
func (service *Service) CustomerOrders(
	ctx context.Context, customerID string, limit int32,
) ([]OrderSummary, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListCustomerOrders(ctx, dbgen.ListCustomerOrdersParams{
		UserID: id, PageSize: pageSize(limit),
	})
	if err != nil {
		return nil, err
	}
	orders := make([]OrderSummary, 0, len(rows))
	for _, row := range rows {
		orders = append(orders, orderSummaryFrom(row))
	}
	return orders, nil
}

func orderSummaryFrom(row dbgen.Order) OrderSummary {
	return OrderSummary{
		ID:             uuidString(row.ID),
		State:          row.State,
		Operation:      row.Operation,
		Currency:       row.Currency,
		SubtotalMinor:  row.SubtotalMinor,
		DiscountMinor:  row.DiscountMinor,
		WalletMinor:    row.WalletMinor,
		ExternalMinor:  row.ExternalMinor,
		PaidMinor:      row.PaidMinor,
		RefundedMinor:  row.RefundedMinor,
		CustomerID:     uuidString(row.UserID),
		SubscriptionID: uuidString(row.SubscriptionID),
		CreatedAt:      timeValue(row.CreatedAt),
		UpdatedAt:      timeValue(row.UpdatedAt),
	}
}

// LedgerLine is one wallet movement with the transaction that caused it.
type LedgerLine struct {
	ID            string     `json:"id"`
	TransactionID string     `json:"transactionId"`
	Type          string     `json:"type"`
	ReferenceType string     `json:"referenceType"`
	ReferenceID   string     `json:"referenceId"`
	Reason        string     `json:"reason,omitempty"`
	Currency      string     `json:"currency"`
	AmountMinor   int64      `json:"amountMinor"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

// CustomerWallet lists a customer's ledger, newest first.
//
// The ledger is append-only: there is no update or delete path anywhere in the
// panel. A correction is a compensating entry, which is why this method has no
// counterpart that edits a line.
func (service *Service) CustomerWallet(
	ctx context.Context, customerID string, limit int32,
) ([]LedgerLine, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListCustomerLedgerEntries(ctx, dbgen.ListCustomerLedgerEntriesParams{
		UserID: id, PageSize: pageSize(limit),
	})
	if err != nil {
		return nil, err
	}
	lines := make([]LedgerLine, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, LedgerLine{
			ID:            uuidString(row.LedgerEntry.ID),
			TransactionID: uuidString(row.LedgerEntry.TransactionID),
			Type:          row.Type,
			ReferenceType: row.ReferenceType,
			ReferenceID:   row.ReferenceID,
			Reason:        textValue(row.Reason),
			Currency:      row.LedgerEntry.Currency,
			AmountMinor:   row.LedgerEntry.AmountMinor,
			ExpiresAt:     timePointer(row.LedgerEntry.ExpiresAt),
			CreatedAt:     timeValue(row.LedgerEntry.CreatedAt),
		})
	}
	return lines, nil
}

// TicketSummary is one support ticket in the customer timeline.
type TicketSummary struct {
	ID            string    `json:"id"`
	Status        string    `json:"status"`
	Subject       string    `json:"subject"`
	Priority      string    `json:"priority"`
	LastMessageAt time.Time `json:"lastMessageAt"`
	CreatedAt     time.Time `json:"createdAt"`
}

// CustomerTickets lists a customer's support history.
func (service *Service) CustomerTickets(
	ctx context.Context, customerID string, limit int32,
) ([]TicketSummary, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListCustomerSupportTickets(ctx, dbgen.ListCustomerSupportTicketsParams{
		UserID: id, PageSize: pageSize(limit),
	})
	if err != nil {
		return nil, err
	}
	tickets := make([]TicketSummary, 0, len(rows))
	for _, row := range rows {
		tickets = append(tickets, TicketSummary{
			ID:            uuidString(row.ID),
			Status:        row.Status,
			Subject:       row.Subject,
			Priority:      row.Priority,
			LastMessageAt: timeValue(row.LastMessageAt),
			CreatedAt:     timeValue(row.CreatedAt),
		})
	}
	return tickets, nil
}

// ConsentRecord is the latest recorded decision for one purpose.
type ConsentRecord struct {
	Purpose       string    `json:"purpose"`
	Granted       bool      `json:"granted"`
	PolicyVersion string    `json:"policyVersion"`
	Source        string    `json:"source"`
	OccurredAt    time.Time `json:"occurredAt"`
}

// CustomerConsents lists the current consent state per purpose.
func (service *Service) CustomerConsents(ctx context.Context, customerID string) ([]ConsentRecord, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListCustomerConsents(ctx, id)
	if err != nil {
		return nil, err
	}
	consents := make([]ConsentRecord, 0, len(rows))
	for _, row := range rows {
		consents = append(consents, ConsentRecord{
			Purpose:       row.Purpose,
			Granted:       row.Granted,
			PolicyVersion: row.PolicyVersion,
			Source:        row.Source,
			OccurredAt:    timeValue(row.OccurredAt),
		})
	}
	return consents, nil
}

// SetCustomerStatus suspends or reactivates a customer.
//
// Suspension is not deletion and does not touch Remnawave here: the entitlement
// lifecycle is driven by the fulfillment pipeline, which observes the customer
// state rather than being bypassed by a panel click. Deletion and anonymisation
// go through the retention workflow that already owns them, for the same
// reason.
func (service *Service) SetCustomerStatus(
	ctx context.Context, customerID, status string, actor Actor,
) (CustomerProfile, error) {
	if status != "active" && status != "suspended" {
		return CustomerProfile{}, ErrValidaton
	}
	if strings.TrimSpace(actor.Reason) == "" {
		// A status change with no reason is unreviewable after the fact, and
		// this is exactly the kind of change a review asks about.
		return CustomerProfile{}, ErrValidaton
	}
	id, err := parseUUID(customerID)
	if err != nil {
		return CustomerProfile{}, err
	}

	action := "restored"
	if status == "suspended" {
		action = "suspended"
	}

	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.ApplyCustomerLifecycle(ctx, dbgen.ApplyCustomerLifecycleParams{
			UserID: id, Status: status,
		}); txErr != nil {
			return notFound(txErr)
		}
		if _, txErr := queries.InsertCustomerLifecycleEvent(ctx, dbgen.InsertCustomerLifecycleEventParams{
			UserID: id, Action: action, Reason: actor.Reason,
			ActorType: "operator", ActorID: optionalText(actor.AdminID),
			RequestID: optionalText(actor.RequestID),
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.customer."+action, "customer", "customer", customerID,
			map[string]any{"status": status},
		))
	})
	if err != nil {
		return CustomerProfile{}, err
	}
	return service.CustomerProfile(ctx, customerID)
}

// RenameSubscription changes the customer-visible label of one subscription.
//
// It is the only subscription attribute the panel edits directly. Everything
// else — expiry, traffic, device limit, squads, enabled state — belongs to
// Remnawave and is changed through the fulfillment pipeline, so that a panel
// edit and a reconciliation sweep cannot disagree about what the desired state
// is.
func (service *Service) RenameSubscription(
	ctx context.Context, customerID, subscriptionID, label string, actor Actor,
) error {
	label = strings.TrimSpace(label)
	if label == "" || len([]rune(label)) > 40 {
		return ErrValidaton
	}
	customer, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	subscription, err := parseUUID(subscriptionID)
	if err != nil {
		return err
	}

	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.RenameSubscription(ctx, dbgen.RenameSubscriptionParams{
			SubscriptionID: subscription, UserID: customer, Label: label,
		}); txErr != nil {
			return notFound(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.subscription.renamed", "customer", "subscription", subscriptionID,
			map[string]any{"customerId": customerID},
		))
	})
}

// ReferralSummary is what one customer's referral activity looks like.
//
// The invitees are named only by identifier and status. A referrer is entitled
// to know how many of their invitations converted; they are not entitled to a
// list of other people's account details, and neither is an operator looking at
// this screen for a support question about the referrer.
type ReferralSummary struct {
	Code       string          `json:"code,omitempty"`
	CodeIssued *time.Time      `json:"codeIssuedAt,omitempty"`
	InvitedBy  string          `json:"invitedBy,omitempty"`
	InvitedVia string          `json:"invitedVia,omitempty"`
	InvitedAt  *time.Time      `json:"invitedAt,omitempty"`
	Invitees   []ReferralInvit `json:"invitees"`
}

// ReferralInvit is one invitation this customer sent.
type ReferralInvit struct {
	CustomerID string    `json:"customerId"`
	Status     string    `json:"status"`
	Converted  bool      `json:"converted"`
	InvitedAt  time.Time `json:"invitedAt"`
}

// CustomerReferrals assembles the referral view for one customer.
//
// Every part is optional: a customer who never opened the referral screen has
// no code, one who arrived on their own has no referrer, and one whose
// invitations went nowhere has an empty list. None of those is an error, so
// each missing piece is simply absent rather than failing the whole read.
func (service *Service) CustomerReferrals(
	ctx context.Context, customerID string, limit int32,
) (ReferralSummary, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return ReferralSummary{}, err
	}
	queries := service.queries()
	summary := ReferralSummary{Invitees: []ReferralInvit{}}

	if code, codeErr := queries.GetCustomerReferralCode(ctx, id); codeErr == nil {
		issued := timeValue(code.CreatedAt)
		summary.Code, summary.CodeIssued = code.Code, &issued
	} else if !errors.Is(codeErr, pgx.ErrNoRows) {
		return ReferralSummary{}, codeErr
	}

	if referrer, refErr := queries.GetCustomerReferrer(ctx, id); refErr == nil {
		invited := timeValue(referrer.CreatedAt)
		summary.InvitedBy = uuidString(referrer.ReferrerUserID)
		summary.InvitedVia, summary.InvitedAt = referrer.Code, &invited
	} else if !errors.Is(refErr, pgx.ErrNoRows) {
		return ReferralSummary{}, refErr
	}

	rows, err := queries.ListCustomerReferrals(ctx, dbgen.ListCustomerReferralsParams{
		ReferrerUserID: id, PageSize: pageSize(limit),
	})
	if err != nil {
		return ReferralSummary{}, err
	}
	for _, row := range rows {
		summary.Invitees = append(summary.Invitees, ReferralInvit{
			CustomerID: uuidString(row.ReferredUserID),
			Status:     row.ReferredStatus,
			Converted:  row.Converted,
			InvitedAt:  timeValue(row.CreatedAt),
		})
	}
	return summary, nil
}
