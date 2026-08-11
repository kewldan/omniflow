package panelpg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// TopUpSettings is the operator's wallet top-up policy.
type TopUpSettings struct {
	Enabled bool `json:"enabled"`
	// Currency is the single currency top-ups are denominated in. A wallet is
	// per-currency, and offering several top-up currencies would let a customer
	// fund a balance they cannot spend on the plan they want.
	Currency string `json:"currency"`
	// Presets are the buttons offered. Free entry stays available whatever this
	// list contains, so an empty list means "free entry only" rather than "no
	// top-up".
	PresetsMinor     []int64 `json:"presetsMinor"`
	MinimumMinor     int64   `json:"minimumMinor"`
	MaximumMinor     int64   `json:"maximumMinor"`
	WindowSeconds    int64   `json:"windowSeconds"`
	WindowLimitMinor int64   `json:"windowLimitMinor"`
}

// SubscriptionSettings governs concurrent subscriptions installation-wide.
type SubscriptionSettings struct {
	MultiEnabled bool `json:"multiEnabled"`
	// MaxPerCustomer is the installation ceiling. A plan may narrow it further
	// through `plans.max_concurrent_per_customer`; neither can widen the other.
	MaxPerCustomer int32 `json:"maxPerCustomer"`
}

// CommerceSettings is the pair, with the provenance an operator needs to know
// whether they are looking at their own configuration or a seeded default.
type CommerceSettings struct {
	TopUp         TopUpSettings        `json:"topUp"`
	Subscriptions SubscriptionSettings `json:"subscriptions"`
	UpdatedAt     time.Time            `json:"updatedAt"`
	UpdatedBy     string               `json:"updatedBy,omitempty"`
}

// EnsureCommerceSettings seeds the settings row from the environment the
// installation already had, and returns whatever is stored.
//
// This runs once, on the first read after upgrading. An installation coming
// from v0.5 configured these limits through environment variables, and silently
// replacing them with schema defaults would change how much a customer may
// top up without anybody having asked for that. After the seed the row is
// authoritative and only the panel writes it; the environment variables stay
// readable so restoring into a fresh database reproduces the same starting
// point.
func (service *Service) EnsureCommerceSettings(
	ctx context.Context, defaults CommerceSettings,
) (CommerceSettings, error) {
	queries := service.queries()

	stored, err := queries.GetCommerceSettings(ctx)
	if err == nil {
		return commerceSettingsFrom(stored), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CommerceSettings{}, err
	}

	seeded, err := queries.SeedCommerceSettings(ctx, dbgen.SeedCommerceSettingsParams{
		TopupEnabled:                defaults.TopUp.Enabled,
		TopupCurrency:               defaults.TopUp.Currency,
		TopupPresetsMinor:           defaults.TopUp.PresetsMinor,
		TopupMinimumMinor:           defaults.TopUp.MinimumMinor,
		TopupMaximumMinor:           defaults.TopUp.MaximumMinor,
		TopupWindowSeconds:          defaults.TopUp.WindowSeconds,
		TopupWindowLimitMinor:       defaults.TopUp.WindowLimitMinor,
		MultiSubscriptionEnabled:    defaults.Subscriptions.MultiEnabled,
		MaxSubscriptionsPerCustomer: defaults.Subscriptions.MaxPerCustomer,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Another process seeded it between the read and the insert. Its values
		// are as valid as ours, so read them back rather than racing again.
		stored, readErr := queries.GetCommerceSettings(ctx)
		if readErr != nil {
			return CommerceSettings{}, readErr
		}
		return commerceSettingsFrom(stored), nil
	}
	if err != nil {
		return CommerceSettings{}, err
	}
	return commerceSettingsFrom(seeded), nil
}

// CommerceSettings reads the stored configuration.
func (service *Service) CommerceSettings(ctx context.Context) (CommerceSettings, error) {
	stored, err := service.queries().GetCommerceSettings(ctx)
	if err != nil {
		return CommerceSettings{}, notFound(err)
	}
	return commerceSettingsFrom(stored), nil
}

// SaveTopUpSettings validates and stores the top-up policy.
//
// The range check is here as well as on the table because the table's version
// answers with a constraint violation, and an operator who typed a maximum
// below the minimum deserves to be told which field to fix.
func (service *Service) SaveTopUpSettings(
	ctx context.Context, settings TopUpSettings, actor Actor,
) (CommerceSettings, error) {
	if settings.MinimumMinor <= 0 || settings.MaximumMinor <= 0 {
		return CommerceSettings{}, ErrValidaton
	}
	if settings.MinimumMinor > settings.MaximumMinor {
		return CommerceSettings{}, ErrValidaton
	}
	if settings.WindowSeconds <= 0 {
		return CommerceSettings{}, ErrValidaton
	}
	for _, preset := range settings.PresetsMinor {
		// A preset outside the limits is a button that always fails.
		if preset < settings.MinimumMinor || preset > settings.MaximumMinor {
			return CommerceSettings{}, ErrValidaton
		}
	}

	var saved CommerceSettings
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.UpdateTopUpSettings(ctx, dbgen.UpdateTopUpSettingsParams{
			TopupEnabled:          settings.Enabled,
			TopupCurrency:         settings.Currency,
			TopupPresetsMinor:     settings.PresetsMinor,
			TopupMinimumMinor:     settings.MinimumMinor,
			TopupMaximumMinor:     settings.MaximumMinor,
			TopupWindowSeconds:    settings.WindowSeconds,
			TopupWindowLimitMinor: settings.WindowLimitMinor,
			UpdatedBy:             optionalUUID(actor.AdminID),
		})
		if err != nil {
			return notFound(err)
		}
		saved = commerceSettingsFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.settings.topup.updated", "configuration", "settings", "commerce",
			map[string]any{
				"enabled": settings.Enabled, "currency": settings.Currency,
				"minimumMinor": settings.MinimumMinor, "maximumMinor": settings.MaximumMinor,
				"windowSeconds": settings.WindowSeconds, "presets": len(settings.PresetsMinor),
			},
		))
	})
	return saved, err
}

// SaveSubscriptionSettings stores the concurrency policy.
//
// Turning multiple subscriptions off does not close anything. Customers who
// already hold several keep them, and the ceiling applies to new purchases; a
// setting that retroactively orphaned an entitlement would orphan a Remnawave
// user with it.
func (service *Service) SaveSubscriptionSettings(
	ctx context.Context, settings SubscriptionSettings, actor Actor,
) (CommerceSettings, error) {
	if settings.MaxPerCustomer < 1 {
		return CommerceSettings{}, ErrValidaton
	}

	var saved CommerceSettings
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.UpdateSubscriptionSettings(ctx, dbgen.UpdateSubscriptionSettingsParams{
			MultiSubscriptionEnabled:    settings.MultiEnabled,
			MaxSubscriptionsPerCustomer: settings.MaxPerCustomer,
			UpdatedBy:                   optionalUUID(actor.AdminID),
		})
		if err != nil {
			return notFound(err)
		}
		saved = commerceSettingsFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.settings.subscriptions.updated", "configuration", "settings", "commerce",
			map[string]any{
				"multiEnabled": settings.MultiEnabled, "maxPerCustomer": settings.MaxPerCustomer,
			},
		))
	})
	return saved, err
}

func commerceSettingsFrom(row dbgen.CommerceSetting) CommerceSettings {
	return CommerceSettings{
		TopUp: TopUpSettings{
			Enabled:          row.TopupEnabled,
			Currency:         row.TopupCurrency,
			PresetsMinor:     row.TopupPresetsMinor,
			MinimumMinor:     row.TopupMinimumMinor,
			MaximumMinor:     row.TopupMaximumMinor,
			WindowSeconds:    row.TopupWindowSeconds,
			WindowLimitMinor: row.TopupWindowLimitMinor,
		},
		Subscriptions: SubscriptionSettings{
			MultiEnabled:   row.MultiSubscriptionEnabled,
			MaxPerCustomer: row.MaxSubscriptionsPerCustomer,
		},
		UpdatedAt: timeValue(row.UpdatedAt),
		UpdatedBy: uuidString(row.UpdatedBy),
	}
}

// ---------------------------------------------------------------------------
// Payment provider configuration
// ---------------------------------------------------------------------------

// ProviderSettings is one provider-and-merchant configuration as the panel sees
// it.
//
// No secret is present. `CredentialsSet` and `WebhookSecretSet` report whether
// one is stored, which is everything the form needs to render "configured" or
// "not configured" without the value ever leaving the database.
type ProviderSettings struct {
	Provider         string `json:"provider"`
	MerchantID       string `json:"merchantId"`
	Enabled          bool   `json:"enabled"`
	DisplayOrder     int32  `json:"displayOrder"`
	CredentialsSet   bool   `json:"credentialsSet"`
	WebhookSecretSet bool   `json:"webhookSecretSet"`

	// AdapterRecurring is what the compiled-in adapter declares. It is filled
	// in by transport from `payments.Capabilities`, not read from the database,
	// so a row cannot claim a capability the integration does not have.
	AdapterRecurring bool `json:"adapterRecurring"`

	RecurringEnabled    bool       `json:"recurringEnabled"`
	RecurringTestStatus string     `json:"recurringTestStatus"`
	RecurringTestedAt   *time.Time `json:"recurringTestedAt,omitempty"`

	ConnectionStatus    string     `json:"connectionStatus"`
	ConnectionCheckedAt *time.Time `json:"connectionCheckedAt,omitempty"`
	ConnectionErrorCode string     `json:"connectionErrorCode,omitempty"`

	WebhookStatus        string     `json:"webhookStatus"`
	WebhookLastEventAt   *time.Time `json:"webhookLastEventAt,omitempty"`
	WebhookLastErrorCode string     `json:"webhookLastErrorCode,omitempty"`

	UpdatedAt time.Time `json:"updatedAt"`
}

// ProviderSettingsInput is a panel save.
//
// The two secrets are pointers so that "not supplied" and "set to empty" are
// distinguishable: a nil leaves the stored secret alone, which is what lets the
// form be re-saved without ever echoing a credential back to the browser.
type ProviderSettingsInput struct {
	Provider      string
	MerchantID    string
	Enabled       bool
	DisplayOrder  int32
	Credentials   *string
	WebhookSecret *string
}

// ListProviderSettings reads every configured provider.
func (service *Service) ListProviderSettings(ctx context.Context) ([]ProviderSettings, error) {
	rows, err := service.queries().ListPaymentProviderSettings(ctx)
	if err != nil {
		return nil, err
	}
	settings := make([]ProviderSettings, 0, len(rows))
	for _, row := range rows {
		settings = append(settings, providerSettingsFrom(row))
	}
	return settings, nil
}

// ProviderCredential returns the decrypted credential for an adapter to use.
//
// It is separate from ListProviderSettings on purpose: the listing is what the
// panel renders and must never carry a secret, and this is what a provider call
// needs and is never serialised to a response.
func (service *Service) ProviderCredential(
	ctx context.Context, provider, merchantID string,
) (string, error) {
	row, err := service.queries().GetPaymentProviderSettings(ctx, dbgen.GetPaymentProviderSettingsParams{
		Provider: provider, MerchantID: merchantID,
	})
	if err != nil {
		return "", notFound(err)
	}
	return service.OpenSecret(row.CredentialsCiphertext, SecretPaymentProvider)
}

// SaveProviderSettings stores a provider configuration.
//
// Recurring is deliberately not writable here. It moves only through
// RecordRecurringTest, so a settings save can never enable automatic charging
// as a side effect of changing a display order.
func (service *Service) SaveProviderSettings(
	ctx context.Context, input ProviderSettingsInput, actor Actor,
) (ProviderSettings, error) {
	var credentials, webhookSecret []byte
	if input.Credentials != nil {
		sealed, err := service.sealSecret(*input.Credentials, SecretPaymentProvider)
		if err != nil {
			return ProviderSettings{}, err
		}
		credentials = sealed
	}
	if input.WebhookSecret != nil {
		sealed, err := service.sealSecret(*input.WebhookSecret, SecretPaymentWebhook)
		if err != nil {
			return ProviderSettings{}, err
		}
		webhookSecret = sealed
	}

	var saved ProviderSettings
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.UpsertPaymentProviderSettings(ctx, dbgen.UpsertPaymentProviderSettingsParams{
			Provider:                input.Provider,
			MerchantID:              input.MerchantID,
			Enabled:                 input.Enabled,
			DisplayOrder:            input.DisplayOrder,
			CredentialsCiphertext:   credentials,
			WebhookSecretCiphertext: webhookSecret,
			UpdatedBy:               optionalUUID(actor.AdminID),
		})
		if err != nil {
			return err
		}
		saved = providerSettingsFrom(row)
		// The metadata records that a secret was rotated, never the secret.
		return appendAudit(ctx, queries, actor.audit(
			"panel.provider.updated", "configuration", "payment_provider",
			input.Provider+":"+input.MerchantID,
			map[string]any{
				"enabled": input.Enabled, "displayOrder": input.DisplayOrder,
				"credentialsRotated": input.Credentials != nil,
				"webhookRotated":     input.WebhookSecret != nil,
			},
		))
	})
	return saved, err
}

// RecordConnectionCheck stores the outcome of a provider connection test.
func (service *Service) RecordConnectionCheck(
	ctx context.Context, provider, merchantID, status, errorCode string, actor Actor,
) (ProviderSettings, error) {
	var saved ProviderSettings
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.RecordProviderConnectionCheck(ctx, dbgen.RecordProviderConnectionCheckParams{
			Provider: provider, MerchantID: merchantID,
			ConnectionStatus:    status,
			ConnectionErrorCode: optionalText(errorCode),
		})
		if err != nil {
			return notFound(err)
		}
		saved = providerSettingsFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.provider.connection_tested", "configuration", "payment_provider",
			provider+":"+merchantID,
			map[string]any{"status": status, "errorCode": errorCode},
		))
	})
	return saved, err
}

// RecordWebhookHealth stores what the webhook endpoint last observed for a
// provider, so the panel can distinguish "configured" from "actually
// receiving".
func (service *Service) RecordWebhookHealth(
	ctx context.Context, provider, merchantID, status, errorCode string,
) error {
	_, err := service.queries().RecordProviderWebhookHealth(ctx, dbgen.RecordProviderWebhookHealthParams{
		Provider: provider, MerchantID: merchantID,
		WebhookStatus:        status,
		WebhookLastErrorCode: optionalText(errorCode),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// A provider with no settings row is one an operator configured through
		// the environment. Recording health for it is a nicety, not a
		// requirement, so its absence must not fail webhook intake.
		return nil
	}
	return err
}

// RecordRecurringTest stores the result of a capability test and, only on a
// pass, may enable automatic charging.
//
// The two are written together because the table constraint refuses any pair
// where recurring is enabled without a passing test. Enabling is still an
// explicit operator decision — a passing test alone does not turn it on.
func (service *Service) RecordRecurringTest(
	ctx context.Context, provider, merchantID string, passed, enable bool, actor Actor,
) (ProviderSettings, error) {
	status := "failed"
	if passed {
		status = "passed"
	}
	// A failed test can only ever result in recurring being off, whatever the
	// caller asked for.
	enabled := enable && passed

	var saved ProviderSettings
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.SetProviderRecurring(ctx, dbgen.SetProviderRecurringParams{
			Provider: provider, MerchantID: merchantID,
			RecurringTestStatus: status,
			RecurringEnabled:    enabled,
			UpdatedBy:           optionalUUID(actor.AdminID),
		})
		if err != nil {
			return notFound(err)
		}
		saved = providerSettingsFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.provider.recurring_configured", "configuration", "payment_provider",
			provider+":"+merchantID,
			map[string]any{"testStatus": status, "recurringEnabled": enabled},
		))
	})
	return saved, err
}

func providerSettingsFrom(row dbgen.PaymentProviderSetting) ProviderSettings {
	return ProviderSettings{
		Provider:             row.Provider,
		MerchantID:           row.MerchantID,
		Enabled:              row.Enabled,
		DisplayOrder:         row.DisplayOrder,
		CredentialsSet:       len(row.CredentialsCiphertext) > 0,
		WebhookSecretSet:     len(row.WebhookSecretCiphertext) > 0,
		RecurringEnabled:     row.RecurringEnabled,
		RecurringTestStatus:  row.RecurringTestStatus,
		RecurringTestedAt:    timePointer(row.RecurringTestedAt),
		ConnectionStatus:     row.ConnectionStatus,
		ConnectionCheckedAt:  timePointer(row.ConnectionCheckedAt),
		ConnectionErrorCode:  textValue(row.ConnectionErrorCode),
		WebhookStatus:        row.WebhookStatus,
		WebhookLastEventAt:   timePointer(row.WebhookLastEventAt),
		WebhookLastErrorCode: textValue(row.WebhookLastErrorCode),
		UpdatedAt:            timeValue(row.UpdatedAt),
	}
}
