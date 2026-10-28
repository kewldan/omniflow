package accountreferral

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ExportVersion identifies the document shape.
//
// It is carried in the document itself so a customer who kept an export from a
// year ago, and the support operator reading it with them, can tell which
// version's field meanings apply.
const ExportVersion = "omniflow.account.export/1"

// exportSectionLimit bounds each list.
//
// The export is synchronous, so an account with a decade of orders must not be
// able to turn one request into an unbounded response. A truncated section is
// named in Truncated rather than silently short, because a customer told their
// data is complete when it is not has been misinformed about the one thing the
// document exists to answer.
const exportSectionLimit = 1000

// ExportSections names what an export contains, in the order the document
// carries them. The privacy screen shows this list before the customer asks, so
// the request is made with knowledge of what comes back.
func ExportSections() []string {
	return []string{
		"profile", "identities", "contacts", "subscriptions", "entitlements",
		"orders", "payments", "wallet", "support", "referral", "loyalty",
		"consents", "lifecycle",
	}
}

// ExportRedactions names what is deliberately left out, and why.
//
// It ships inside the document. An export that quietly omits things reads as
// complete, and a customer cannot ask about an absence they cannot see; naming
// each exclusion turns a silent gap into a statement somebody can challenge.
func ExportRedactions() []string {
	return []string{
		// Another customer's identifier. An inviter is entitled to know an invite
		// paid out, not to a roster of who took it.
		"other_customer_identifiers",
		// Payment credentials: provider references, checkout URLs, and receipt
		// metadata are the payment provider's handles on a transaction, and a
		// document a customer may forward is the wrong place for them.
		"payment_credentials",
		// Provider secrets and the raw claim payloads an identity provider
		// returned. The link itself is exported; the provider's own material is
		// not this installation's to redistribute.
		"provider_secrets",
		// Subscription links are credentials that grant access. They live behind
		// the connection screen, which is rate-limited and never cached.
		"subscription_links",
		// Device and network identifiers observed upstream, including any
		// belonging to somebody else sharing a household or a connection.
		"device_and_network_identifiers",
	}
}

// ExportDocument is everything this installation holds about one customer, as
// one response.
type ExportDocument struct {
	Version     string
	GeneratedAt time.Time

	Profile       ExportProfile
	Identities    []ExportIdentity
	Contacts      []ExportContact
	Subscriptions []ExportSubscription
	Entitlements  []ExportEntitlement
	Orders        []ExportOrder
	Payments      []ExportPayment
	Wallet        []ExportWalletEntry
	Support       []ExportTicket
	Referral      ExportReferral
	Loyalty       ExportLoyalty
	Consents      []ExportConsent
	Lifecycle     []ExportLifecycleEvent

	// Redactions is the standing list of exclusions; Truncated names the
	// sections that hit the row limit for this particular customer.
	Redactions []string
	Truncated  []string
}

// ExportProfile is the account itself.
type ExportProfile struct {
	ID             string
	Status         string
	Locale         string
	Timezone       string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	SuspendedAt    *time.Time
	DeletedAt      *time.Time
	AnonymizedAt   *time.Time
	RetentionUntil *time.Time
}

// ExportIdentity is one way the customer signs in.
//
// The provider's own claim payload is not carried. Subject is the customer's
// identifier at that provider, which is theirs; the metadata around it is the
// provider's, shaped by the provider, and unbounded.
type ExportIdentity struct {
	Provider  string
	Subject   string
	Status    string
	Verified  bool
	CreatedAt time.Time
}

// ExportContact is one channel, with the address in the clear because the
// customer is its owner.
type ExportContact struct {
	Kind          string
	Value         string
	Verified      bool
	Transactional bool
	Marketing     bool
	CreatedAt     time.Time
	RevokedAt     *time.Time
}

// ExportSubscription is one subscription slot. The upstream user identifier and
// the access link are not part of it.
type ExportSubscription struct {
	ID        string
	Slot      int
	Label     string
	Status    string
	CreatedAt time.Time
	ClosedAt  *time.Time
}

// ExportEntitlement is one purchased period.
type ExportEntitlement struct {
	ID                    string
	PlanCode              string
	Status                string
	StartsAt              time.Time
	EndsAt                time.Time
	TrafficAllowanceBytes *int64
	DeviceLimit           *int
	CreatedAt             time.Time
}

// ExportOrder is one order with its money broken out. Every amount is in minor
// units of the stated currency.
type ExportOrder struct {
	ID            string
	State         string
	Operation     string
	Currency      string
	SubtotalMinor int64
	DiscountMinor int64
	WalletMinor   int64
	ExternalMinor int64
	PaidMinor     int64
	RefundedMinor int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ExportPayment is one payment attempt, with the provider's handles removed.
type ExportPayment struct {
	OrderID     string
	Provider    string
	Status      string
	AmountMinor int64
	Currency    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ExportWalletEntry is one movement of the customer's balance.
type ExportWalletEntry struct {
	// Type is the ledger transaction's kind — credit, payment, referral_reward,
	// correction, and so on — which is what makes a balance readable.
	Type          string
	ReferenceType string
	Currency      string
	AmountMinor   int64
	ExpiresAt     *time.Time
	CreatedAt     time.Time
}

// ExportTicket is one support conversation with its messages.
type ExportTicket struct {
	ID        string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
	Messages  []ExportMessage
}

// ExportMessage is one message. Operator replies are included because they were
// written to this customer and this customer has already read them; internal
// operator notes live in another table this query never touches.
type ExportMessage struct {
	Sender    string
	Body      string
	CreatedAt time.Time
}

// ExportReferral is the customer's own side of the referral programme.
type ExportReferral struct {
	Code      string
	Invited   int64
	Qualified int64
	Rewards   []ExportReward
	// InvitedBy records that this customer arrived through somebody's invite,
	// without saying whose. The inviter's code identifies another customer, and
	// an export is exactly the document that gets forwarded.
	InvitedBy *ExportInvitedBy
}

// ExportReward is one granted referral reward.
type ExportReward struct {
	Role        string
	State       string
	AmountMinor int64
	Currency    string
	GrantedAt   time.Time
	ReversedAt  *time.Time
}

// ExportInvitedBy is the attribution seen from the invitee's side.
type ExportInvitedBy struct {
	AttributedAt time.Time
	Qualified    bool
	QualifiedAt  *time.Time
}

// ExportLoyalty is the standing and how it moved.
type ExportLoyalty struct {
	Enabled     bool
	TierCode    string
	Metric      int64
	EvaluatedAt *time.Time
	GraceUntil  *time.Time
	History     []ExportLoyaltyChange
}

// ExportLoyaltyChange is one recorded movement between tiers. The operator who
// made an override is not named: they are an operator record, not a customer's.
type ExportLoyaltyChange struct {
	FromTier   string
	ToTier     string
	Metric     int64
	Reason     string
	OccurredAt time.Time
}

// ExportConsent is one dated decision.
type ExportConsent struct {
	Purpose       string
	Granted       bool
	PolicyVersion string
	Source        string
	OccurredAt    time.Time
}

// ExportLifecycleEvent is one recorded change of account state.
type ExportLifecycleEvent struct {
	Action     string
	Reason     string
	ActorType  string
	OccurredAt time.Time
}

// ---------------------------------------------------------------------------
// Projection
// ---------------------------------------------------------------------------

// paymentRow is the payment intent as stored, credentials and all.
//
// It exists so the redaction is a function that can be tested rather than an
// omission in a SELECT list that nobody notices when somebody later adds a
// column with `SELECT *`.
type paymentRow struct {
	OrderID           string
	Provider          string
	Status            string
	AmountMinor       int64
	Currency          string
	ProviderReference string
	CheckoutURL       string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// projectPayment drops the provider's handles on the transaction.
//
// A provider reference and a checkout URL are credentials: the first identifies
// the charge to the provider's support desk, and the second is frequently a
// bearer link that can still be opened. Neither belongs in a document a customer
// may forward to somebody who asked for "proof of payment".
func projectPayment(row paymentRow) ExportPayment {
	return ExportPayment{
		OrderID: row.OrderID, Provider: row.Provider, Status: row.Status,
		AmountMinor: row.AmountMinor, Currency: row.Currency,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
}

// walletRow is the ledger entry joined to its transaction.
type walletRow struct {
	Type          string
	ReferenceType string
	ReferenceID   string
	// Reason is operator free text. A referral reversal quotes the reviewer's
	// note, which can describe a pattern involving other accounts.
	Reason      string
	Currency    string
	AmountMinor int64
	ExpiresAt   *time.Time
	CreatedAt   time.Time
}

// projectWalletEntry keeps the movement and drops the prose.
//
// The type and the reference type say what moved the balance, which is what a
// customer checking their wallet needs. The operator's reason and the reference
// identifier behind it are omitted: the reason is free text written for an
// operator audience, and a reference identifier can name a record belonging to
// the pair rather than to this customer alone.
func projectWalletEntry(row walletRow) ExportWalletEntry {
	return ExportWalletEntry{
		Type: row.Type, ReferenceType: row.ReferenceType,
		Currency: row.Currency, AmountMinor: row.AmountMinor,
		ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt.UTC(),
	}
}

// attributionRow is the invite this customer arrived through.
type attributionRow struct {
	ReferrerID   string
	Code         string
	AttributedAt time.Time
	QualifiedAt  *time.Time
}

// projectInvitedBy reports the fact of the invite and nothing about the inviter.
//
// Both the referrer's identifier and the code they shared point at another
// customer — a code is a stable pseudonym for the person who owns it, and
// anybody holding both an export and a public invite link could join the two.
func projectInvitedBy(row attributionRow) ExportInvitedBy {
	return ExportInvitedBy{
		AttributedAt: row.AttributedAt.UTC(),
		Qualified:    row.QualifiedAt != nil,
		QualifiedAt:  row.QualifiedAt,
	}
}

// ---------------------------------------------------------------------------
// Assembly
// ---------------------------------------------------------------------------

// Export builds the customer's personal-data document.
//
// Every query is scoped to the calling customer's identifier. That is the whole
// authorization model here and it is deliberately not delegated: there is no
// filter applied afterwards that a future refactor could drop, because a row
// belonging to somebody else is never read in the first place.
//
// The document is produced synchronously and returned to the caller. Nothing is
// written to disk, nothing is queued, and nothing is emailed — an export that
// travels acquires a delivery channel, a retention window, and a link that
// authenticates on its own, and each of those is a disclosure decision that has
// to be made deliberately rather than inherited from a convenience.
func (service *Service) Export(
	ctx context.Context, customerID string, request RequestContext,
) (ExportDocument, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return ExportDocument{}, err
	}

	document := ExportDocument{
		Version: ExportVersion, GeneratedAt: service.now(),
		Redactions: ExportRedactions(),
	}
	if document.Profile, err = service.exportProfile(ctx, userID, customerID); err != nil {
		return ExportDocument{}, err
	}
	for _, section := range []struct {
		name string
		read func(context.Context, pgtype.UUID, *ExportDocument) (bool, error)
	}{
		{"identities", service.exportIdentities},
		{"contacts", service.exportContacts},
		{"subscriptions", service.exportSubscriptions},
		{"entitlements", service.exportEntitlements},
		{"orders", service.exportOrders},
		{"payments", service.exportPayments},
		{"wallet", service.exportWallet},
		{"support", service.exportSupport},
		{"referral", service.exportReferral},
		{"loyalty", service.exportLoyalty},
		{"consents", service.exportConsents},
		{"lifecycle", service.exportLifecycle},
	} {
		truncated, readErr := section.read(ctx, userID, &document)
		if readErr != nil {
			return ExportDocument{}, readErr
		}
		if truncated {
			document.Truncated = append(document.Truncated, section.name)
		}
	}

	// The trail is written after the document is assembled and before it is
	// handed over, so a recorded export is always one that actually succeeded.
	// The metadata carries counts only: an audit row describing what was
	// disclosed must not become a second copy of it.
	if err = service.recordAudit(ctx, service.queries, customerID, "account.data.exported", request.RequestID,
		map[string]any{
			"version": ExportVersion, "orders": len(document.Orders),
			"payments": len(document.Payments), "walletEntries": len(document.Wallet),
			"tickets": len(document.Support), "truncated": document.Truncated,
		},
	); err != nil {
		return ExportDocument{}, err
	}
	return document, nil
}

func (service *Service) exportProfile(
	ctx context.Context, userID pgtype.UUID, customerID string,
) (ExportProfile, error) {
	user, err := service.queries.GetCustomer(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExportProfile{}, ErrNotFound
	}
	if err != nil {
		return ExportProfile{}, err
	}
	return ExportProfile{
		ID:             customerID,
		Status:         user.Status,
		Locale:         user.Locale,
		Timezone:       user.Timezone,
		CreatedAt:      user.CreatedAt.Time.UTC(),
		UpdatedAt:      user.UpdatedAt.Time.UTC(),
		SuspendedAt:    timePointer(user.SuspendedAt),
		DeletedAt:      timePointer(user.DeletedAt),
		AnonymizedAt:   timePointer(user.AnonymizedAt),
		RetentionUntil: timePointer(user.RetentionUntil),
	}, nil
}

func (service *Service) exportIdentities(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	// The shared read selects the provider's claim payload too. It is dropped
	// here, in the projection, rather than carried into the document.
	rows, err := service.queries.ListCustomerIdentities(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		document.Identities = append(document.Identities, ExportIdentity{
			Provider: row.Provider, Subject: row.ProviderSubject, Status: row.Status,
			Verified: row.VerifiedAt.Valid, CreatedAt: row.CreatedAt.Time.UTC(),
		})
	}
	return false, nil
}

func (service *Service) exportContacts(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	rows, err := service.queries.ListAccountExportContacts(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		document.Contacts = append(document.Contacts, ExportContact{
			Kind: row.Kind,
			// Without a key the address cannot be opened. The row is still
			// reported, because "we hold a contact channel of this kind" is itself
			// a fact the customer is entitled to, and an empty value is honest
			// about the rest.
			Value:         service.open(row.Kind, row.ValueCiphertext),
			Verified:      row.VerifiedAt.Valid,
			Transactional: row.TransactionalEnabled,
			Marketing:     row.MarketingEnabled,
			CreatedAt:     row.CreatedAt.Time.UTC(),
			RevokedAt:     timePointer(row.RevokedAt),
		})
	}
	return false, nil
}

func (service *Service) exportSubscriptions(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	rows, err := service.queries.ListAccountExportSubscriptions(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		document.Subscriptions = append(document.Subscriptions, ExportSubscription{
			ID: uuidString(row.ID), Slot: int(row.Slot), Label: row.Label, Status: row.Status,
			CreatedAt: row.CreatedAt.Time.UTC(), ClosedAt: timePointer(row.ClosedAt),
		})
	}
	return false, nil
}

func (service *Service) exportEntitlements(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	rows, err := service.queries.ListAccountExportEntitlements(ctx, dbgen.ListAccountExportEntitlementsParams{
		UserID: userID, PageSize: exportSectionLimit,
	})
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		entitlement := ExportEntitlement{
			ID: uuidString(row.ID), PlanCode: row.PlanCode, Status: row.Status,
			StartsAt: row.StartsAt.Time.UTC(), EndsAt: row.EndsAt.Time.UTC(),
			CreatedAt: row.CreatedAt.Time.UTC(),
		}
		if row.TrafficAllowanceBytes.Valid {
			bytes := row.TrafficAllowanceBytes.Int64
			entitlement.TrafficAllowanceBytes = &bytes
		}
		if row.DeviceLimit.Valid {
			limit := int(row.DeviceLimit.Int32)
			entitlement.DeviceLimit = &limit
		}
		document.Entitlements = append(document.Entitlements, entitlement)
	}
	return len(document.Entitlements) == exportSectionLimit, nil
}

func (service *Service) exportOrders(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	rows, err := service.queries.ListCustomerOrders(ctx, dbgen.ListCustomerOrdersParams{
		UserID: userID, PageSize: exportSectionLimit,
	})
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		document.Orders = append(document.Orders, ExportOrder{
			ID: uuidString(row.ID), State: row.State, Operation: row.Operation,
			Currency:      row.Currency,
			SubtotalMinor: row.SubtotalMinor, DiscountMinor: row.DiscountMinor,
			WalletMinor: row.WalletMinor, ExternalMinor: row.ExternalMinor,
			PaidMinor: row.PaidMinor, RefundedMinor: row.RefundedMinor,
			CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
		})
	}
	return len(document.Orders) == exportSectionLimit, nil
}

func (service *Service) exportPayments(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	// The join to `orders` inside the query is what scopes this to the calling
	// customer: `payment_intents` has no customer column of its own.
	rows, err := service.queries.ListAccountExportPayments(ctx, dbgen.ListAccountExportPaymentsParams{
		UserID: userID, PageSize: exportSectionLimit,
	})
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		document.Payments = append(document.Payments, projectPayment(paymentRow{
			OrderID: uuidString(row.OrderID), Provider: row.Provider, Status: row.Status,
			AmountMinor: row.AmountMinor, Currency: row.Currency,
			ProviderReference: row.ProviderReference.String,
			CheckoutURL:       row.CheckoutUrl.String,
			CreatedAt:         row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
		}))
	}
	return len(document.Payments) == exportSectionLimit, nil
}

func (service *Service) exportWallet(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	rows, err := service.queries.ListCustomerLedgerEntries(ctx, dbgen.ListCustomerLedgerEntriesParams{
		UserID: userID, PageSize: exportSectionLimit,
	})
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		document.Wallet = append(document.Wallet, projectWalletEntry(walletRow{
			Type: row.Type, ReferenceType: row.ReferenceType, ReferenceID: row.ReferenceID,
			Reason:   row.Reason.String,
			Currency: row.LedgerEntry.Currency, AmountMinor: row.LedgerEntry.AmountMinor,
			ExpiresAt: timePointer(row.LedgerEntry.ExpiresAt),
			CreatedAt: row.LedgerEntry.CreatedAt.Time,
		}))
	}
	return len(document.Wallet) == exportSectionLimit, nil
}

func (service *Service) exportSupport(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	// Tickets and messages arrive in one pass, ordered so a ticket's messages
	// are contiguous. Two round trips per conversation would turn a customer
	// with a long support history into a query storm.
	rows, err := service.queries.ListAccountExportSupport(ctx, dbgen.ListAccountExportSupportParams{
		UserID: userID, PageSize: exportSectionLimit,
	})
	if err != nil {
		return false, err
	}

	byID := make(map[string]int)
	for _, row := range rows {
		ticketID := uuidString(row.ID)
		index, seen := byID[ticketID]
		if !seen {
			document.Support = append(document.Support, ExportTicket{
				ID: ticketID, Status: row.Status,
				CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
			})
			index = len(document.Support) - 1
			byID[ticketID] = index
		}
		if row.Sender.Valid {
			document.Support[index].Messages = append(
				document.Support[index].Messages,
				ExportMessage{
					Sender: row.Sender.String, Body: row.Body.String,
					CreatedAt: row.MessageCreatedAt.Time.UTC(),
				},
			)
		}
	}
	return len(rows) == exportSectionLimit, nil
}

func (service *Service) exportReferral(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	// The code is read rather than created. An export must not have a side
	// effect: a customer downloading their data has not asked to join a
	// programme.
	code, err := service.queries.GetCustomerReferralCode(ctx, userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	document.Referral.Code = code.Code

	counts, err := service.queries.CountAccountInvitations(ctx, userID)
	if err != nil {
		return false, err
	}
	document.Referral.Invited = counts.Invited
	document.Referral.Qualified = counts.Qualified

	invite, err := service.queries.GetAccountInvitedBy(ctx, userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return false, err
	default:
		invited := projectInvitedBy(attributionRow{
			ReferrerID:   uuidString(invite.ReferrerUserID),
			Code:         invite.Code,
			AttributedAt: invite.CreatedAt.Time,
			QualifiedAt:  timePointer(invite.QualifiedAt),
		})
		document.Referral.InvitedBy = &invited
	}

	rows, err := service.queries.ListAccountExportReferralRewards(ctx,
		dbgen.ListAccountExportReferralRewardsParams{UserID: userID, PageSize: exportSectionLimit},
	)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		document.Referral.Rewards = append(document.Referral.Rewards, ExportReward{
			Role: row.Role, State: RewardState(row.ReversedAt.Valid, row.ReviewState),
			AmountMinor: row.AmountMinor, Currency: row.Currency,
			GrantedAt: row.GrantedAt.Time.UTC(), ReversedAt: timePointer(row.ReversedAt),
		})
	}
	return len(document.Referral.Rewards) == exportSectionLimit, nil
}

func (service *Service) exportLoyalty(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	standing, err := service.queries.GetLoyaltyStanding(ctx, userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return false, err
	default:
		document.Loyalty.Enabled = true
		document.Loyalty.TierCode = standing.TierCode
		document.Loyalty.Metric = standing.LoyaltyStanding.EvaluatedMetric
		document.Loyalty.EvaluatedAt = timePointer(standing.LoyaltyStanding.EvaluatedAt)
		document.Loyalty.GraceUntil = timePointer(standing.LoyaltyStanding.GraceUntil)
	}

	// The shared history read also selects the acting operator. It is dropped
	// here: an operator who made an override is an operator record, not the
	// customer's.
	rows, err := service.queries.ListLoyaltyHistory(ctx, dbgen.ListLoyaltyHistoryParams{
		UserID: userID, PageSize: exportSectionLimit,
	})
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		document.Loyalty.History = append(document.Loyalty.History, ExportLoyaltyChange{
			FromTier: row.FromCode, ToTier: row.ToCode, Metric: row.EvaluatedMetric,
			Reason: row.Reason, OccurredAt: row.OccurredAt.Time.UTC(),
		})
	}
	return len(document.Loyalty.History) == exportSectionLimit, nil
}

func (service *Service) exportConsents(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	history, err := service.consentHistory(ctx, userID, exportSectionLimit)
	if err != nil {
		return false, err
	}
	document.Consents = append(document.Consents, history...)
	return len(document.Consents) == exportSectionLimit, nil
}

func (service *Service) exportLifecycle(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	// The acting operator's identifier is not selected. An operator is not part
	// of the customer's personal data, and naming the individual who suspended an
	// account in a document the account holder receives is a safety question
	// rather than a transparency one.
	rows, err := service.queries.ListAccountExportLifecycle(ctx, dbgen.ListAccountExportLifecycleParams{
		UserID: userID, PageSize: exportSectionLimit,
	})
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		document.Lifecycle = append(document.Lifecycle, ExportLifecycleEvent{
			Action: row.Action, Reason: row.Reason, ActorType: row.ActorType,
			OccurredAt: row.OccurredAt.Time.UTC(),
		})
	}
	return len(document.Lifecycle) == exportSectionLimit, nil
}
