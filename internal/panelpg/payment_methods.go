package panelpg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/payments"
)

// PaymentMethodSettings is how one customer-facing payment button is shown
// and to whom.
type PaymentMethodSettings struct {
	Provider          string    `json:"provider"`
	Method            string    `json:"method"`
	Key               string    `json:"key"`
	Configured        bool      `json:"configured"`
	Enabled           bool      `json:"enabled"`
	DisplayOrder      int32     `json:"displayOrder"`
	NameRU            string    `json:"nameRu"`
	NameEN            string    `json:"nameEn"`
	DescriptionRU     string    `json:"descriptionRu"`
	DescriptionEN     string    `json:"descriptionEn"`
	MinMinor          *int64    `json:"minMinor,omitempty"`
	MaxMinor          *int64    `json:"maxMinor,omitempty"`
	QuickAmountsMinor []int64   `json:"quickAmountsMinor"`
	LoginKinds        []string  `json:"loginKinds"`
	TopUpAudience     string    `json:"topUpAudience"`
	LoyaltyTierCodes  []string  `json:"loyaltyTierCodes"`
	OpenDirectly      bool      `json:"openDirectly"`
	UpdatedAt         time.Time `json:"updatedAt,omitempty"`
}

// ErrInvalidPaymentMethod is returned for a payment button configuration the
// panel refuses before it reaches the database.
var ErrInvalidPaymentMethod = fmt.Errorf("%w: payment method settings", ErrValidaton)

// ListPaymentMethodSettings returns every button an operator can configure:
// one per method each configured adapter offers, with its stored settings
// where there are any, plus stored rows for adapters this deployment no longer
// loads (so they stay visible rather than silently orphaned).
func (service *Service) ListPaymentMethodSettings(
	ctx context.Context, adapters map[string][]string,
) ([]PaymentMethodSettings, error) {
	rows, err := service.queries().ListPaymentMethodSettings(ctx)
	if err != nil {
		return nil, err
	}
	stored := make(map[string]dbgen.PaymentMethodSetting, len(rows))
	for _, row := range rows {
		stored[payments.OptionKey(row.Provider, row.Method)] = row
	}
	items := make([]PaymentMethodSettings, 0, len(rows)+len(adapters))
	seen := make(map[string]bool, len(rows))
	for provider, methods := range adapters {
		for index, method := range methods {
			key := payments.OptionKey(provider, method)
			seen[key] = true
			if row, ok := stored[key]; ok {
				item := paymentMethodFrom(row)
				item.Configured = true
				items = append(items, item)
				continue
			}
			items = append(items, PaymentMethodSettings{
				Provider: provider, Method: method, Key: key, Configured: true, Enabled: true,
				DisplayOrder: int32(index), TopUpAudience: "all",
				QuickAmountsMinor: []int64{}, LoginKinds: []string{}, LoyaltyTierCodes: []string{},
			})
		}
	}
	for key, row := range stored {
		if !seen[key] {
			items = append(items, paymentMethodFrom(row))
		}
	}
	return items, nil
}

// SavePaymentMethodSettings stores one button's configuration, audited.
func (service *Service) SavePaymentMethodSettings(
	ctx context.Context, input PaymentMethodSettings, actor Actor,
) (PaymentMethodSettings, error) {
	if err := validatePaymentMethod(&input); err != nil {
		return PaymentMethodSettings{}, err
	}
	var saved PaymentMethodSettings
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.UpsertPaymentMethodSettings(ctx, dbgen.UpsertPaymentMethodSettingsParams{
			Provider: input.Provider, Method: input.Method, Enabled: input.Enabled,
			DisplayOrder: input.DisplayOrder, NameRu: input.NameRU, NameEn: input.NameEN,
			DescriptionRu: input.DescriptionRU, DescriptionEn: input.DescriptionEN,
			MinMinor: optionalInt8(input.MinMinor), MaxMinor: optionalInt8(input.MaxMinor),
			QuickAmountsMinor: input.QuickAmountsMinor, LoginKinds: input.LoginKinds,
			TopupAudience: input.TopUpAudience, LoyaltyTierCodes: input.LoyaltyTierCodes,
			OpenDirectly: input.OpenDirectly, UpdatedBy: optionalUUID(actor.AdminID),
		})
		if err != nil {
			return err
		}
		saved = paymentMethodFrom(row)
		saved.Configured = true
		return appendAudit(ctx, queries, actor.audit(
			"panel.payment_method.updated", "configuration", "payment_method", saved.Key,
			map[string]any{"enabled": input.Enabled, "displayOrder": input.DisplayOrder, "openDirectly": input.OpenDirectly},
		))
	})
	return saved, err
}

func validatePaymentMethod(input *PaymentMethodSettings) error {
	input.Provider = strings.TrimSpace(input.Provider)
	input.Method = strings.ToLower(strings.TrimSpace(input.Method))
	if input.Provider == "" || !payments.ValidMethodKey(input.Method) {
		return fmt.Errorf("%w: provider and method", ErrInvalidPaymentMethod)
	}
	for _, text := range []*string{&input.NameRU, &input.NameEN} {
		*text = strings.TrimSpace(*text)
		if len([]rune(*text)) > 64 {
			return fmt.Errorf("%w: a name is longer than 64 characters", ErrInvalidPaymentMethod)
		}
	}
	for _, text := range []*string{&input.DescriptionRU, &input.DescriptionEN} {
		*text = strings.TrimSpace(*text)
		if len([]rune(*text)) > 240 {
			return fmt.Errorf("%w: a description is longer than 240 characters", ErrInvalidPaymentMethod)
		}
	}
	if (input.MinMinor != nil && *input.MinMinor <= 0) || (input.MaxMinor != nil && *input.MaxMinor <= 0) ||
		(input.MinMinor != nil && input.MaxMinor != nil && *input.MinMinor > *input.MaxMinor) {
		return fmt.Errorf("%w: amount bounds", ErrInvalidPaymentMethod)
	}
	if input.QuickAmountsMinor == nil {
		input.QuickAmountsMinor = []int64{}
	}
	if len(input.QuickAmountsMinor) > 8 {
		return fmt.Errorf("%w: at most eight quick amounts", ErrInvalidPaymentMethod)
	}
	for _, amount := range input.QuickAmountsMinor {
		if amount <= 0 {
			return fmt.Errorf("%w: quick amounts must be positive", ErrInvalidPaymentMethod)
		}
	}
	if input.LoginKinds == nil {
		input.LoginKinds = []string{}
	}
	for _, kind := range input.LoginKinds {
		switch kind {
		case "telegram", "email", "vk", "oidc":
		default:
			return fmt.Errorf("%w: unknown sign-in kind %q", ErrInvalidPaymentMethod, kind)
		}
	}
	switch input.TopUpAudience {
	case "":
		input.TopUpAudience = "all"
	case "all", "first_topup", "repeat_topup":
	default:
		return fmt.Errorf("%w: top-up audience", ErrInvalidPaymentMethod)
	}
	if input.LoyaltyTierCodes == nil {
		input.LoyaltyTierCodes = []string{}
	}
	return nil
}

func paymentMethodFrom(row dbgen.PaymentMethodSetting) PaymentMethodSettings {
	item := PaymentMethodSettings{
		Provider: row.Provider, Method: row.Method, Key: payments.OptionKey(row.Provider, row.Method),
		Enabled: row.Enabled, DisplayOrder: row.DisplayOrder,
		NameRU: row.NameRu, NameEN: row.NameEn, DescriptionRU: row.DescriptionRu, DescriptionEN: row.DescriptionEn,
		QuickAmountsMinor: row.QuickAmountsMinor, LoginKinds: row.LoginKinds,
		TopUpAudience: row.TopupAudience, LoyaltyTierCodes: row.LoyaltyTierCodes,
		OpenDirectly: row.OpenDirectly, UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
	if row.MinMinor.Valid {
		value := row.MinMinor.Int64
		item.MinMinor = &value
	}
	if row.MaxMinor.Valid {
		value := row.MaxMinor.Int64
		item.MaxMinor = &value
	}
	return item
}
