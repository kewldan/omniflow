package panelpg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/omniflow/omniflow/internal/adtracking"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Advertising measurement, from the operator's side.
//
// The roadmap entry named three absences and they have one cause between them.
// Payment happens on the backend — often a day after the click, sometimes
// through a transfer somebody confirms by hand — so the browser session that
// carried the advertising platform's click identifier is gone by the time there
// is money to attribute. A counter script watching the storefront sees the
// visit and never sees the sale, which is why no channel could be attributed
// even on an installation that had pasted a counter in.
//
// Three deliberate limits, each of which gives something up:
//
//   - A counter is a provider this repository knows and an identifier validated
//     against its shape, never a snippet. The flexibility given up is exactly
//     the ability to run arbitrary code in a customer's browser, and a
//     customer's browser holds subscription links.
//   - Nothing is uploaded anywhere. The export is a file the operator downloads
//     and hands to the platform themselves. Sending a customer's purchase to
//     Google because a settings toggle was on is not a thing anybody should
//     have to discover from behaviour.
//   - Attribution hangs off an order, not a customer. An order is a conversion
//     with an amount and a date, which is what an upload takes; a click
//     identifier held against a person for the life of their account is a
//     profile, which nothing here needs.

// AnalyticsSettings is what an operator configured, with the version guard the
// settings screens use.
type AnalyticsSettings struct {
	adtracking.Settings
	Version   int32     `json:"version"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
	// Providers is the closed set of counters this build can render, so the
	// screen offers what exists rather than a text field that fails on save.
	Providers []string `json:"providers"`
	// VerificationNames is the same for webmaster tags.
	VerificationNames []string `json:"verificationNames"`
}

// Analytics reads the configuration.
func (service *Service) Analytics(ctx context.Context) (AnalyticsSettings, error) {
	section, err := service.SettingSection(ctx, "analytics")
	if err != nil {
		return AnalyticsSettings{}, err
	}
	var stored adtracking.Settings
	if len(section.Document) > 0 {
		// A document that will not parse is treated as no configuration rather
		// than as an error. Measurement is the least important thing on the
		// installation, and a settings screen that cannot open because an
		// analytics document went bad would be worse than one counter missing.
		_ = json.Unmarshal(section.Document, &stored)
	}
	return AnalyticsSettings{
		Settings:          adtracking.Normalise(stored),
		Version:           section.Version,
		UpdatedAt:         section.UpdatedAt,
		UpdatedBy:         section.UpdatedBy,
		Providers:         adtracking.ProviderNames(),
		VerificationNames: adtracking.VerificationNames(),
	}, nil
}

// SaveAnalytics stores it, refusing anything that could not be rendered safely.
func (service *Service) SaveAnalytics(
	ctx context.Context, settings adtracking.Settings, expectedVersion int32, actor Actor,
) (AnalyticsSettings, error) {
	normalised := adtracking.Normalise(settings)
	if err := adtracking.CheckSettings(normalised); err != nil {
		return AnalyticsSettings{}, wrapValidation(err)
	}
	document, err := json.Marshal(normalised)
	if err != nil {
		return AnalyticsSettings{}, err
	}
	if _, err := service.SaveSettingSection(
		ctx, "analytics", document, expectedVersion, nil, actor,
	); err != nil {
		return AnalyticsSettings{}, err
	}
	return service.Analytics(ctx)
}

// PublicAnalytics is what an anonymous visitor's browser is told.
//
// Verification tags are present whether or not measurement is enabled: they set
// no cookie, load no script, and observe nobody, and a verification that only
// appeared for consenting visitors would verify nothing — the fetcher is a
// webmaster tool, not a person who can consent.
//
// Counters are present only when measurement is enabled, and the browser is
// told to render them only after consent. The endpoint publishes what would run
// rather than deciding: the decision belongs where the consent is.
type PublicAnalytics struct {
	// Measurable is false when nothing would run even with consent, which is
	// what suppresses the consent request entirely. Asking somebody to agree to
	// nothing is worse than not asking.
	Measurable    bool                      `json:"measurable"`
	Counters      map[string]string         `json:"counters,omitempty"`
	Verifications []adtracking.Verification `json:"verifications,omitempty"`
}

// PublicAnalytics reads the configuration in the shape the storefront needs.
func (service *Service) PublicAnalytics(ctx context.Context) (PublicAnalytics, error) {
	settings, err := service.Analytics(ctx)
	if err != nil {
		return PublicAnalytics{}, err
	}
	public := PublicAnalytics{
		Measurable:    adtracking.Measurable(settings.Settings),
		Verifications: settings.Verifications,
	}
	if public.Measurable {
		public.Counters = map[string]string{}
		for provider, identifier := range settings.Counters {
			public.Counters[string(provider)] = identifier
		}
	}
	return public, nil
}

// Conversion is one settled order with the advertisement it came from.
type Conversion struct {
	OrderID       string    `json:"orderId"`
	Operation     string    `json:"operation"`
	State         string    `json:"state"`
	Currency      string    `json:"currency"`
	PaidMinor     int64     `json:"paidMinor"`
	RefundedMinor int64     `json:"refundedMinor"`
	PaidAt        time.Time `json:"paidAt"`

	ClickID     string `json:"clickId,omitempty"`
	ClickSource string `json:"clickSource,omitempty"`
	Source      string `json:"source,omitempty"`
	Medium      string `json:"medium,omitempty"`
	Campaign    string `json:"campaign,omitempty"`
	Content     string `json:"content,omitempty"`
	Term        string `json:"term,omitempty"`
}

// ChannelResult is one channel's contribution over the period.
type ChannelResult struct {
	Channel          string `json:"channel"`
	Medium           string `json:"medium,omitempty"`
	Orders           int64  `json:"orders"`
	AttributedClicks int64  `json:"attributedClicks"`
	Currency         string `json:"currency"`
	PaidMinor        int64  `json:"paidMinor"`
	RefundedMinor    int64  `json:"refundedMinor"`
}

// conversionLimit bounds one export. It is a file somebody uploads by hand, and
// a platform's own import limits are smaller than this.
const conversionLimit = 50000

// Conversions lists the settled orders an advertisement produced.
//
// Refunded orders are present with their refunded amount rather than dropped. A
// platform that optimised bidding on a sale that was later returned is being
// taught the wrong thing, and an operator can only correct for that if the
// export says it happened.
func (service *Service) Conversions(
	ctx context.Context, from, to time.Time, source string,
) ([]Conversion, error) {
	rows, err := service.queries().ConversionsInPeriod(ctx, dbgen.ConversionsInPeriodParams{
		FromTime: timestamp(from), ToTime: timestamp(to),
		ClickSource: optionalText(source), PageSize: conversionLimit,
	})
	if err != nil {
		return nil, err
	}
	conversions := make([]Conversion, 0, len(rows))
	for _, row := range rows {
		conversions = append(conversions, Conversion{
			OrderID: uuidString(row.OrderID), Operation: row.Operation, State: row.State,
			Currency: row.Currency, PaidMinor: row.PaidMinor,
			RefundedMinor: row.RefundedMinor, PaidAt: instant(row.PaidAt),
			ClickID: row.ClickID, ClickSource: row.ClickSource,
			Source: row.UtmSource, Medium: row.UtmMedium, Campaign: row.UtmCampaign,
			Content: row.UtmContent, Term: row.UtmTerm,
		})
	}
	return conversions, nil
}

// Channels summarises where the period's revenue came from.
func (service *Service) Channels(
	ctx context.Context, from, to time.Time,
) ([]ChannelResult, error) {
	rows, err := service.queries().AttributionSummary(ctx, dbgen.AttributionSummaryParams{
		FromTime: timestamp(from), ToTime: timestamp(to),
	})
	if err != nil {
		return nil, err
	}
	channels := make([]ChannelResult, 0, len(rows))
	for _, row := range rows {
		channels = append(channels, ChannelResult{
			Channel: row.Channel, Medium: row.Medium, Orders: row.Orders,
			AttributedClicks: row.AttributedClicks, Currency: row.Currency,
			PaidMinor: row.PaidMinor, RefundedMinor: row.RefundedMinor,
		})
	}
	return channels, nil
}
