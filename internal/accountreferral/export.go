package accountreferral

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
	if err = service.recordAudit(ctx, service.pool, customerID, "account.data.exported", request.RequestID,
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
	var (
		profile        ExportProfile
		createdAt      pgtype.Timestamptz
		updatedAt      pgtype.Timestamptz
		suspendedAt    pgtype.Timestamptz
		deletedAt      pgtype.Timestamptz
		anonymizedAt   pgtype.Timestamptz
		retentionUntil pgtype.Timestamptz
	)
	err := service.pool.QueryRow(ctx, `SELECT status, locale, timezone, created_at, updated_at,
			suspended_at, deleted_at, anonymized_at, retention_until
		FROM users WHERE id = $1`, userID).
		Scan(
			&profile.Status, &profile.Locale, &profile.Timezone, &createdAt, &updatedAt,
			&suspendedAt, &deletedAt, &anonymizedAt, &retentionUntil,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExportProfile{}, ErrNotFound
	}
	if err != nil {
		return ExportProfile{}, err
	}
	profile.ID = customerID
	profile.CreatedAt = createdAt.Time.UTC()
	profile.UpdatedAt = updatedAt.Time.UTC()
	profile.SuspendedAt = timePointer(suspendedAt)
	profile.DeletedAt = timePointer(deletedAt)
	profile.AnonymizedAt = timePointer(anonymizedAt)
	profile.RetentionUntil = timePointer(retentionUntil)
	return profile, nil
}

func (service *Service) exportIdentities(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	rows, err := service.pool.Query(ctx, `SELECT provider, provider_subject, status, verified_at, created_at
		FROM identities WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			identity   ExportIdentity
			verifiedAt pgtype.Timestamptz
			createdAt  pgtype.Timestamptz
		)
		if err = rows.Scan(
			&identity.Provider, &identity.Subject, &identity.Status, &verifiedAt, &createdAt,
		); err != nil {
			return false, err
		}
		identity.Verified = verifiedAt.Valid
		identity.CreatedAt = createdAt.Time.UTC()
		document.Identities = append(document.Identities, identity)
	}
	return false, rows.Err()
}

func (service *Service) exportContacts(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	rows, err := service.pool.Query(ctx, `SELECT kind, value_ciphertext, verified_at,
			transactional_enabled, marketing_enabled, created_at, revoked_at
		FROM contact_channels WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			contact    ExportContact
			ciphertext []byte
			verifiedAt pgtype.Timestamptz
			createdAt  pgtype.Timestamptz
			revokedAt  pgtype.Timestamptz
		)
		if err = rows.Scan(
			&contact.Kind, &ciphertext, &verifiedAt,
			&contact.Transactional, &contact.Marketing, &createdAt, &revokedAt,
		); err != nil {
			return false, err
		}
		contact.Verified = verifiedAt.Valid
		contact.CreatedAt = createdAt.Time.UTC()
		contact.RevokedAt = timePointer(revokedAt)
		// Without a key the address cannot be opened. The row is still reported,
		// because "we hold a contact channel of this kind" is itself a fact the
		// customer is entitled to, and an empty value is honest about the rest.
		contact.Value = service.open(contact.Kind, ciphertext)
		document.Contacts = append(document.Contacts, contact)
	}
	return false, rows.Err()
}

func (service *Service) exportSubscriptions(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	rows, err := service.pool.Query(ctx, `SELECT id, slot, label, status, created_at, closed_at
		FROM subscriptions WHERE user_id = $1 ORDER BY slot`, userID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id           pgtype.UUID
			subscription ExportSubscription
			slot         int32
			createdAt    pgtype.Timestamptz
			closedAt     pgtype.Timestamptz
		)
		if err = rows.Scan(
			&id, &slot, &subscription.Label, &subscription.Status, &createdAt, &closedAt,
		); err != nil {
			return false, err
		}
		subscription.ID = uuidString(id)
		subscription.Slot = int(slot)
		subscription.CreatedAt = createdAt.Time.UTC()
		subscription.ClosedAt = timePointer(closedAt)
		document.Subscriptions = append(document.Subscriptions, subscription)
	}
	return false, rows.Err()
}

func (service *Service) exportEntitlements(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	rows, err := service.pool.Query(ctx, `SELECT e.id, p.code, e.status, e.starts_at, e.ends_at,
			e.traffic_allowance_bytes, e.device_limit, e.created_at
		FROM entitlements e
		JOIN plan_versions v ON v.id = e.plan_version_id
		JOIN plans p ON p.id = v.plan_id
		WHERE e.user_id = $1
		ORDER BY e.starts_at DESC
		LIMIT $2`, userID, exportSectionLimit)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id          pgtype.UUID
			entitlement ExportEntitlement
			startsAt    pgtype.Timestamptz
			endsAt      pgtype.Timestamptz
			createdAt   pgtype.Timestamptz
			allowance   pgtype.Int8
			deviceLimit pgtype.Int4
		)
		if err = rows.Scan(
			&id, &entitlement.PlanCode, &entitlement.Status,
			&startsAt, &endsAt, &allowance, &deviceLimit, &createdAt,
		); err != nil {
			return false, err
		}
		entitlement.ID = uuidString(id)
		entitlement.StartsAt = startsAt.Time.UTC()
		entitlement.EndsAt = endsAt.Time.UTC()
		entitlement.CreatedAt = createdAt.Time.UTC()
		if allowance.Valid {
			bytes := allowance.Int64
			entitlement.TrafficAllowanceBytes = &bytes
		}
		if deviceLimit.Valid {
			limit := int(deviceLimit.Int32)
			entitlement.DeviceLimit = &limit
		}
		document.Entitlements = append(document.Entitlements, entitlement)
	}
	return len(document.Entitlements) == exportSectionLimit, rows.Err()
}

func (service *Service) exportOrders(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	rows, err := service.pool.Query(ctx, `SELECT id, state, operation, currency,
			subtotal_minor, discount_minor, wallet_minor, external_minor,
			paid_minor, refunded_minor, created_at, updated_at
		FROM orders WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`,
		userID, exportSectionLimit)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id        pgtype.UUID
			order     ExportOrder
			createdAt pgtype.Timestamptz
			updatedAt pgtype.Timestamptz
		)
		if err = rows.Scan(
			&id, &order.State, &order.Operation, &order.Currency,
			&order.SubtotalMinor, &order.DiscountMinor, &order.WalletMinor,
			&order.ExternalMinor, &order.PaidMinor, &order.RefundedMinor,
			&createdAt, &updatedAt,
		); err != nil {
			return false, err
		}
		order.ID = uuidString(id)
		order.CreatedAt = createdAt.Time.UTC()
		order.UpdatedAt = updatedAt.Time.UTC()
		document.Orders = append(document.Orders, order)
	}
	return len(document.Orders) == exportSectionLimit, rows.Err()
}

func (service *Service) exportPayments(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	// The join to `orders` is what scopes this to the calling customer:
	// `payment_intents` has no customer column of its own, so reading it without
	// the join would read every installation's payments.
	rows, err := service.pool.Query(ctx, `SELECT i.order_id, i.provider, i.status,
			i.amount_minor, i.currency, i.provider_reference, i.checkout_url,
			i.created_at, i.updated_at
		FROM payment_intents i
		JOIN orders o ON o.id = i.order_id
		WHERE o.user_id = $1
		ORDER BY i.created_at DESC
		LIMIT $2`, userID, exportSectionLimit)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			orderID   pgtype.UUID
			row       paymentRow
			reference pgtype.Text
			checkout  pgtype.Text
			createdAt pgtype.Timestamptz
			updatedAt pgtype.Timestamptz
		)
		if err = rows.Scan(
			&orderID, &row.Provider, &row.Status, &row.AmountMinor, &row.Currency,
			&reference, &checkout, &createdAt, &updatedAt,
		); err != nil {
			return false, err
		}
		row.OrderID = uuidString(orderID)
		row.ProviderReference = reference.String
		row.CheckoutURL = checkout.String
		row.CreatedAt = createdAt.Time
		row.UpdatedAt = updatedAt.Time
		document.Payments = append(document.Payments, projectPayment(row))
	}
	return len(document.Payments) == exportSectionLimit, rows.Err()
}

func (service *Service) exportWallet(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	rows, err := service.pool.Query(ctx, `SELECT t.type, t.reference_type, t.reference_id,
			COALESCE(t.reason, ''), e.currency, e.amount_minor, e.expires_at, e.created_at
		FROM ledger_entries e
		JOIN ledger_transactions t ON t.id = e.transaction_id
		WHERE e.account_type = 'customer_wallet' AND e.user_id = $1
		ORDER BY e.created_at DESC
		LIMIT $2`, userID, exportSectionLimit)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			row       walletRow
			expiresAt pgtype.Timestamptz
			createdAt pgtype.Timestamptz
		)
		if err = rows.Scan(
			&row.Type, &row.ReferenceType, &row.ReferenceID, &row.Reason,
			&row.Currency, &row.AmountMinor, &expiresAt, &createdAt,
		); err != nil {
			return false, err
		}
		row.ExpiresAt = timePointer(expiresAt)
		row.CreatedAt = createdAt.Time
		document.Wallet = append(document.Wallet, projectWalletEntry(row))
	}
	return len(document.Wallet) == exportSectionLimit, rows.Err()
}

func (service *Service) exportSupport(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	// Tickets and messages arrive in one pass, ordered so a ticket's messages
	// are contiguous. Two round trips per conversation would turn a customer
	// with a long support history into a query storm.
	rows, err := service.pool.Query(ctx, `SELECT t.id, t.status, t.created_at, t.updated_at,
			m.sender, m.body, m.created_at
		FROM support_tickets t
		LEFT JOIN support_messages m ON m.ticket_id = t.id
		WHERE t.user_id = $1
		ORDER BY t.created_at DESC, m.created_at
		LIMIT $2`, userID, exportSectionLimit)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	scanned := 0
	byID := make(map[string]int)
	for rows.Next() {
		var (
			id             pgtype.UUID
			status         string
			ticketCreated  pgtype.Timestamptz
			ticketUpdated  pgtype.Timestamptz
			sender         pgtype.Text
			body           pgtype.Text
			messageCreated pgtype.Timestamptz
		)
		if err = rows.Scan(
			&id, &status, &ticketCreated, &ticketUpdated, &sender, &body, &messageCreated,
		); err != nil {
			return false, err
		}
		scanned++
		ticketID := uuidString(id)
		index, seen := byID[ticketID]
		if !seen {
			document.Support = append(document.Support, ExportTicket{
				ID: ticketID, Status: status,
				CreatedAt: ticketCreated.Time.UTC(), UpdatedAt: ticketUpdated.Time.UTC(),
			})
			index = len(document.Support) - 1
			byID[ticketID] = index
		}
		if sender.Valid {
			document.Support[index].Messages = append(
				document.Support[index].Messages,
				ExportMessage{
					Sender: sender.String, Body: body.String,
					CreatedAt: messageCreated.Time.UTC(),
				},
			)
		}
	}
	return scanned == exportSectionLimit, rows.Err()
}

func (service *Service) exportReferral(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	// The code is read rather than created. An export must not have a side
	// effect: a customer downloading their data has not asked to join a
	// programme.
	var code pgtype.Text
	if err := service.pool.QueryRow(ctx,
		`SELECT code FROM referral_codes WHERE user_id = $1`, userID).Scan(&code); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	document.Referral.Code = code.String

	if err := service.pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM referral_attributions WHERE referrer_user_id = $1),
			(SELECT count(*) FROM referral_attributions
				WHERE referrer_user_id = $1 AND qualified_at IS NOT NULL)`, userID).
		Scan(&document.Referral.Invited, &document.Referral.Qualified); err != nil {
		return false, err
	}

	var (
		row          attributionRow
		referrerID   pgtype.UUID
		attributedAt pgtype.Timestamptz
		qualifiedAt  pgtype.Timestamptz
		inviteCode   string
	)
	err := service.pool.QueryRow(ctx, `SELECT referrer_user_id, code, created_at, qualified_at
		FROM referral_attributions WHERE referred_user_id = $1`, userID).
		Scan(&referrerID, &inviteCode, &attributedAt, &qualifiedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return false, err
	default:
		row.ReferrerID = uuidString(referrerID)
		row.Code = inviteCode
		row.AttributedAt = attributedAt.Time
		row.QualifiedAt = timePointer(qualifiedAt)
		invited := projectInvitedBy(row)
		document.Referral.InvitedBy = &invited
	}

	rows, err := service.pool.Query(ctx, `SELECT w.role, w.amount_minor, w.currency,
			w.granted_at, w.reversed_at, COALESCE(a.review_state, 'clear')
		FROM referral_rewards w
		LEFT JOIN referral_attributions a ON a.referred_user_id = w.referred_user_id
		WHERE w.beneficiary_user_id = $1
		ORDER BY w.granted_at DESC
		LIMIT $2`, userID, exportSectionLimit)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			reward      ExportReward
			grantedAt   pgtype.Timestamptz
			reversedAt  pgtype.Timestamptz
			reviewState string
		)
		if err = rows.Scan(
			&reward.Role, &reward.AmountMinor, &reward.Currency,
			&grantedAt, &reversedAt, &reviewState,
		); err != nil {
			return false, err
		}
		reward.GrantedAt = grantedAt.Time.UTC()
		reward.ReversedAt = timePointer(reversedAt)
		reward.State = RewardState(reversedAt.Valid, reviewState)
		document.Referral.Rewards = append(document.Referral.Rewards, reward)
	}
	return len(document.Referral.Rewards) == exportSectionLimit, rows.Err()
}

func (service *Service) exportLoyalty(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	var (
		tierCode    string
		metric      int64
		evaluatedAt pgtype.Timestamptz
		graceUntil  pgtype.Timestamptz
	)
	err := service.pool.QueryRow(ctx, `SELECT t.code, s.evaluated_metric, s.evaluated_at, s.grace_until
		FROM loyalty_standings s
		JOIN loyalty_tiers t ON t.id = s.tier_id
		WHERE s.user_id = $1`, userID).
		Scan(&tierCode, &metric, &evaluatedAt, &graceUntil)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return false, err
	default:
		document.Loyalty.Enabled = true
		document.Loyalty.TierCode = tierCode
		document.Loyalty.Metric = metric
		document.Loyalty.EvaluatedAt = timePointer(evaluatedAt)
		document.Loyalty.GraceUntil = timePointer(graceUntil)
	}

	rows, err := service.pool.Query(ctx, `SELECT COALESCE(f.code, ''), n.code,
			h.evaluated_metric, h.reason, h.occurred_at
		FROM loyalty_standing_history h
		LEFT JOIN loyalty_tiers f ON f.id = h.from_tier_id
		JOIN loyalty_tiers n ON n.id = h.to_tier_id
		WHERE h.user_id = $1
		ORDER BY h.occurred_at DESC
		LIMIT $2`, userID, exportSectionLimit)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			change     ExportLoyaltyChange
			occurredAt pgtype.Timestamptz
		)
		if err = rows.Scan(
			&change.FromTier, &change.ToTier, &change.Metric, &change.Reason, &occurredAt,
		); err != nil {
			return false, err
		}
		change.OccurredAt = occurredAt.Time.UTC()
		document.Loyalty.History = append(document.Loyalty.History, change)
	}
	return len(document.Loyalty.History) == exportSectionLimit, rows.Err()
}

func (service *Service) exportConsents(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	rows, err := service.pool.Query(ctx, `SELECT purpose, granted, policy_version, source, occurred_at
		FROM consent_records WHERE user_id = $1 ORDER BY occurred_at DESC LIMIT $2`,
		userID, exportSectionLimit)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			consent    ExportConsent
			occurredAt pgtype.Timestamptz
		)
		if err = rows.Scan(
			&consent.Purpose, &consent.Granted, &consent.PolicyVersion, &consent.Source, &occurredAt,
		); err != nil {
			return false, err
		}
		consent.OccurredAt = occurredAt.Time.UTC()
		document.Consents = append(document.Consents, consent)
	}
	return len(document.Consents) == exportSectionLimit, rows.Err()
}

func (service *Service) exportLifecycle(
	ctx context.Context, userID pgtype.UUID, document *ExportDocument,
) (bool, error) {
	// The acting operator's identifier is not selected. An operator is not part
	// of the customer's personal data, and naming the individual who suspended an
	// account in a document the account holder receives is a safety question
	// rather than a transparency one.
	rows, err := service.pool.Query(ctx, `SELECT action, reason, actor_type, occurred_at
		FROM customer_lifecycle_events WHERE user_id = $1 ORDER BY occurred_at DESC LIMIT $2`,
		userID, exportSectionLimit)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			event      ExportLifecycleEvent
			occurredAt pgtype.Timestamptz
		)
		if err = rows.Scan(&event.Action, &event.Reason, &event.ActorType, &occurredAt); err != nil {
			return false, err
		}
		event.OccurredAt = occurredAt.Time.UTC()
		document.Lifecycle = append(document.Lifecycle, event)
	}
	return len(document.Lifecycle) == exportSectionLimit, rows.Err()
}
