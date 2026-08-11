package botapp

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/recurring"
)

// SavedMethod is one stored payment method as the customer sees it.
//
// There is no card number, expiry, or verification value here, and there is no
// column that could hold one. `Label` is whatever the provider supplied — the
// last four digits and a scheme name, typically — and `Token` never leaves the
// server: the bot addresses a method by its own row identifier.
type SavedMethod struct {
	ID        string
	Provider  string
	Label     string
	Status    string
	IsDefault bool
	CreatedAt time.Time
}

// SavedMethods lists the methods a customer can renew with.
//
// Revoked methods are excluded rather than shown greyed out. A customer who
// removed a card has said what they want; keeping it visible invites them to
// wonder whether it is really gone.
func (store *PostgresStore) SavedMethods(
	ctx context.Context, customerID string,
) ([]SavedMethod, error) {
	rows, err := store.pool.Query(ctx,
		`SELECT id::text, provider, display_label, status, is_default, created_at
		 FROM payment_methods
		 WHERE user_id = $1::uuid AND status <> 'revoked'
		 ORDER BY is_default DESC, created_at DESC`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	methods := make([]SavedMethod, 0, 4)
	for rows.Next() {
		var method SavedMethod
		if err := rows.Scan(&method.ID, &method.Provider, &method.Label,
			&method.Status, &method.IsDefault, &method.CreatedAt); err != nil {
			return nil, err
		}
		methods = append(methods, method)
	}
	return methods, rows.Err()
}

// SetDefaultMethod makes one saved method the one automatic renewal uses.
//
// Both statements run in one transaction because a partial unique index allows
// exactly one default per customer: clearing and setting separately would leave
// a window with none, and a renewal landing in that window would find nothing
// to charge.
func (store *PostgresStore) SetDefaultMethod(
	ctx context.Context, customerID, methodID string,
) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx,
		`UPDATE payment_methods SET is_default = false, updated_at = now()
		 WHERE user_id = $1::uuid AND is_default`, customerID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE payment_methods SET is_default = true, updated_at = now()
		 WHERE id = $1::uuid AND user_id = $2::uuid AND status = 'active'`, methodID, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// The identifier came from callback data, which the customer controls.
		// Refusing here is what stops one customer naming another's method.
		return errors.New("payment method does not belong to this customer")
	}
	// Auto-renew follows the default, so the two cannot disagree about which
	// method a charge will use.
	if _, err = tx.Exec(ctx,
		`UPDATE auto_renew_settings SET payment_method_id = $1::uuid, updated_at = now()
		 WHERE user_id = $2::uuid AND funding = 'saved_method'`, methodID, customerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RemoveMethod revokes a saved method and stops any renewal that depended on it.
//
// Revoking without touching auto-renew would leave a customer consented to a
// charge that can no longer be made, which surfaces later as a failed renewal
// they did nothing to cause. Instead the settings fall back to the wallet when
// another method remains, and auto-renew is suspended when none does — with the
// consent record intact, so re-adding a card does not require consenting again.
func (store *PostgresStore) RemoveMethod(ctx context.Context, customerID, methodID string) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx,
		`UPDATE payment_methods
		 SET status = 'revoked', revoked_at = now(), is_default = false, updated_at = now()
		 WHERE id = $1::uuid AND user_id = $2::uuid AND status <> 'revoked'`, methodID, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("payment method does not belong to this customer")
	}
	var remaining string
	err = tx.QueryRow(ctx,
		`SELECT id::text FROM payment_methods
		 WHERE user_id = $1::uuid AND status = 'active'
		 ORDER BY is_default DESC, created_at DESC LIMIT 1`, customerID).Scan(&remaining)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if remaining == "" {
		if _, err = tx.Exec(ctx,
			`UPDATE auto_renew_settings
			 SET funding = 'wallet', payment_method_id = NULL, state = 'suspended', updated_at = now()
			 WHERE user_id = $1::uuid AND funding = 'saved_method'`, customerID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx,
		`UPDATE payment_methods SET is_default = true, updated_at = now()
		 WHERE id = $1::uuid AND NOT EXISTS (
			 SELECT 1 FROM payment_methods
			 WHERE user_id = $2::uuid AND is_default AND status = 'active')`,
		remaining, customerID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx,
		`UPDATE auto_renew_settings SET payment_method_id = $1::uuid, updated_at = now()
		 WHERE user_id = $2::uuid AND funding = 'saved_method'`, remaining, customerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetRenewalFunding chooses what an automatic renewal is paid from.
//
// Choosing a saved method requires one to exist, because the table refuses the
// pair otherwise — and rightly: consenting to charge a card that is not there
// is not a state worth representing.
func (store *PostgresStore) SetRenewalFunding(
	ctx context.Context, customerID, funding string,
) error {
	if funding != recurring.FundingWallet && funding != recurring.FundingSavedMethod {
		return errors.New("unsupported renewal funding")
	}
	if funding == recurring.FundingWallet {
		_, err := store.pool.Exec(ctx,
			`UPDATE auto_renew_settings
			 SET funding = 'wallet', payment_method_id = NULL, updated_at = now()
			 WHERE user_id = $1::uuid`, customerID)
		return err
	}
	var methodID string
	err := store.pool.QueryRow(ctx,
		`SELECT id::text FROM payment_methods
		 WHERE user_id = $1::uuid AND status = 'active'
		 ORDER BY is_default DESC, created_at DESC LIMIT 1`, customerID).Scan(&methodID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNoSavedMethod
	}
	if err != nil {
		return err
	}
	_, err = store.pool.Exec(ctx,
		`UPDATE auto_renew_settings
		 SET funding = 'saved_method', payment_method_id = $1::uuid, updated_at = now()
		 WHERE user_id = $2::uuid`, methodID, customerID)
	return err
}

// errNoSavedMethod is returned when a customer asks to renew from a card they
// have not saved. It is a normal outcome with its own message, not a fault.
var errNoSavedMethod = errors.New("no saved payment method")

// SetRenewalLeadTime sets how far ahead of expiry a renewal is attempted.
//
// The value is clamped by the same domain rule the worker reads, so a customer
// cannot configure a renewal a month early — that charges for a period they
// have not reached and may not want.
func (store *PostgresStore) SetRenewalLeadTime(
	ctx context.Context, customerID string, lead time.Duration,
) error {
	seconds := int64(recurring.NormalizeLeadTime(lead).Seconds())
	_, err := store.pool.Exec(ctx,
		`UPDATE auto_renew_settings SET lead_time_seconds = $1, updated_at = now()
		 WHERE user_id = $2::uuid`, seconds, customerID)
	return err
}

// RenewalSettings is the customer's full auto-renew configuration.
type RenewalSettings struct {
	AutoRenew
	Funding     string
	LeadTime    time.Duration
	State       string
	MethodLabel string
	Consented   bool
}

// RenewalSettings reads the auto-renew configuration and the label of whatever
// method it would charge.
func (store *PostgresStore) RenewalSettings(
	ctx context.Context, customerID string,
) (RenewalSettings, error) {
	var (
		settings      RenewalSettings
		planVersionID pgtype.Text
		provider      pgtype.Text
		currency      pgtype.Text
		cancelledAt   pgtype.Timestamptz
		consentAt     pgtype.Timestamptz
		methodLabel   pgtype.Text
		leadSeconds   int64
	)
	err := store.pool.QueryRow(ctx,
		`SELECT a.enabled, a.plan_version_id::text, a.provider, a.currency, a.cancelled_at,
		        a.funding, a.lead_time_seconds, a.state, a.consent_at, m.display_label
		 FROM auto_renew_settings a
		 LEFT JOIN payment_methods m ON m.id = a.payment_method_id
		 WHERE a.user_id = $1::uuid`, customerID).
		Scan(&settings.Enabled, &planVersionID, &provider, &currency, &cancelledAt,
			&settings.Funding, &leadSeconds, &settings.State, &consentAt, &methodLabel)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row is the documented default: auto-renew is off, funded from the
		// wallet, with the standard lead time.
		return RenewalSettings{Funding: recurring.FundingWallet, LeadTime: recurring.DefaultLeadTime,
			State: recurring.StateIdle}, nil
	}
	if err != nil {
		return RenewalSettings{}, err
	}
	settings.PlanVersionID, settings.Provider, settings.Currency = planVersionID.String, provider.String, currency.String
	settings.CancelledAt = cancelledAt.Time
	settings.LeadTime = time.Duration(leadSeconds) * time.Second
	settings.MethodLabel = methodLabel.String
	settings.Consented = consentAt.Valid
	return settings, nil
}
