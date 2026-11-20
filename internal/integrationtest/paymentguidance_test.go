//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
)

// The operator's transfer instructions reach customers in their language,
// falling back to the other one, and an installation that never saved
// commerce settings has no guidance rather than an error.
func TestPaymentGuidanceIsLocalised(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := accountcheckout.NewStore(harness.pool)
	if _, err := harness.pool.Exec(ctx, `DELETE FROM commerce_settings`); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if guidance, err := store.PaymentGuidance(ctx, "ru"); err != nil || guidance.ManualInstructions != "" || guidance.SupportTopUp {
		t.Fatalf("no settings: %+v %v", guidance, err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO commerce_settings
		(topup_enabled, topup_currency, topup_presets_minor, topup_minimum_minor, topup_maximum_minor,
		 topup_window_seconds, topup_window_limit_minor, multi_subscription_enabled, max_subscriptions_per_customer,
		 manual_instructions_ru, manual_instructions_en, support_topup_enabled)
		VALUES (true, 'RUB', '{}', 100, 1000000, 86400, 0, false, 1, 'Карта 2200 …', '', true)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ru, err := store.PaymentGuidance(ctx, "ru")
	if err != nil || ru.ManualInstructions != "Карта 2200 …" || !ru.SupportTopUp {
		t.Fatalf("ru: %+v %v", ru, err)
	}
	en, err := store.PaymentGuidance(ctx, "en")
	if err != nil || en.ManualInstructions != "Карта 2200 …" {
		t.Fatalf("en falls back to the Russian text: %+v %v", en, err)
	}
}
