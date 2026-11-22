package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Economics is what acquisition cost and returned in one currency. CAC and
// ROI exist only where spend and revenue share the currency: converting one
// into the other would be a guess at a rate, and the report never guesses.
type Economics struct {
	Currency     string   `json:"currency"`
	SpendMinor   int64    `json:"spendMinor"`
	RevenueMinor int64    `json:"revenueMinor"`
	CacMinor     *int64   `json:"cacMinor,omitempty"`
	Roi          *float64 `json:"roi,omitempty"`
}

// CampaignLine is one /start campaign.
type CampaignLine struct {
	CampaignID     string      `json:"campaignId"`
	Code           string      `json:"code"`
	Name           string      `json:"name"`
	Signups        int64       `json:"signups"`
	Payers         int64       `json:"payers"`
	ConversionRate *float64    `json:"conversionRate,omitempty"`
	Economics      []Economics `json:"economics"`
}

// SourceLine is one `utm_source` in one currency.
type SourceLine struct {
	UtmSource    string   `json:"utmSource"`
	Currency     string   `json:"currency"`
	Orders       int64    `json:"orders"`
	Payers       int64    `json:"payers"`
	NewPayers    int64    `json:"newPayers"`
	RevenueMinor int64    `json:"revenueMinor"`
	SpendMinor   int64    `json:"spendMinor"`
	CacMinor     *int64   `json:"cacMinor,omitempty"`
	Roi          *float64 `json:"roi,omitempty"`
}

// PromoLine is one promo code in one currency.
type PromoLine struct {
	Code          string `json:"code"`
	PromotionKind string `json:"promotionKind"`
	Currency      string `json:"currency"`
	Redemptions   int64  `json:"redemptions"`
	Customers     int64  `json:"customers"`
	NewCustomers  int64  `json:"newCustomers"`
	DiscountMinor int64  `json:"discountMinor"`
	RevenueMinor  int64  `json:"revenueMinor"`
}

// BonusLine is wallet credit given away from one source.
type BonusLine struct {
	Source      string `json:"source"`
	Currency    string `json:"currency"`
	Grants      int64  `json:"grants"`
	AmountMinor int64  `json:"amountMinor"`
}

// FreeAccessLine is access given away of one kind.
type FreeAccessLine struct {
	Source string `json:"source"`
	Grants int64  `json:"grants"`
	Days   int64  `json:"days"`
}

// FunnelChannel is one recorded step on one channel.
type FunnelChannel struct {
	Step      string `json:"step"`
	Channel   string `json:"channel"`
	Customers int64  `json:"customers"`
}

// Funnel is how far the customers who signed up in the window got.
type Funnel struct {
	Signups         int64           `json:"signups"`
	PlansViewed     int64           `json:"plansViewed"`
	CheckoutStarted int64           `json:"checkoutStarted"`
	TrialClaimed    int64           `json:"trialClaimed"`
	Paid            int64           `json:"paid"`
	ByChannel       []FunnelChannel `json:"byChannel"`
}

// ChannelValueLine is what the customers one channel brought for the first
// time in the window have been worth since, in one currency, set against
// what acquiring them cost.
type ChannelValueLine struct {
	// Channel is "campaign:<code>", "referral", "utm:<source>", or "organic".
	Channel      string `json:"channel"`
	Currency     string `json:"currency"`
	Payers       int64  `json:"payers"`
	RevenueMinor int64  `json:"revenueMinor"`
	// LtvMinor is revenue per payer so far: lifetime value to date, not a
	// forecast.
	LtvMinor *int64 `json:"ltvMinor,omitempty"`
	// MonthlyRevenueMinor is revenue per payer per thirty days since their
	// first payment.
	MonthlyRevenueMinor *int64 `json:"monthlyRevenueMinor,omitempty"`
	SpendMinor          int64  `json:"spendMinor"`
	CacMinor            *int64 `json:"cacMinor,omitempty"`
	// LtvToCac is lifetime value so far over acquisition cost.
	LtvToCac *float64 `json:"ltvToCac,omitempty"`
	// PaybackMonths is how many months of revenue repay the cost of
	// acquiring one payer.
	PaybackMonths *float64 `json:"paybackMonths,omitempty"`
}

// ReferralRewardLine is what the referral programme gave one role in one
// currency.
type ReferralRewardLine struct {
	Role        string `json:"role"`
	Currency    string `json:"currency"`
	Rewards     int64  `json:"rewards"`
	AmountMinor int64  `json:"amountMinor"`
	Days        int64  `json:"days"`
}

// ReferralProgramme is the referral programme as an acquisition channel.
type ReferralProgramme struct {
	Invited        int64                `json:"invited"`
	Qualified      int64                `json:"qualified"`
	Rejected       int64                `json:"rejected"`
	Payers         int64                `json:"payers"`
	ConversionRate *float64             `json:"conversionRate,omitempty"`
	Rewards        []ReferralRewardLine `json:"rewards"`
	// Economics sets the money rewards granted in the window against what
	// the customers invited in the window have paid since.
	Economics []Economics `json:"economics"`
}

// LocaleFunnel is the funnel's two ends for one interface language.
type LocaleFunnel struct {
	Locale       string   `json:"locale"`
	Signups      int64    `json:"signups"`
	TrialClaimed int64    `json:"trialClaimed"`
	Paid         int64    `json:"paid"`
	TrialRate    *float64 `json:"trialRate,omitempty"`
	PaidRate     *float64 `json:"paidRate,omitempty"`
}

// AcquisitionReport is the acquisition tab in one read.
type AcquisitionReport struct {
	Since       time.Time        `json:"since"`
	Until       time.Time        `json:"until"`
	Timezone    string           `json:"timezone"`
	GeneratedAt time.Time        `json:"generatedAt"`
	Campaigns   []CampaignLine   `json:"campaigns"`
	Sources     []SourceLine     `json:"sources"`
	PromoCodes  []PromoLine      `json:"promoCodes"`
	BonusCredit []BonusLine      `json:"bonusCredit"`
	FreeAccess  []FreeAccessLine `json:"freeAccess"`
	Funnel      Funnel           `json:"funnel"`
	// ChannelValue follows the customers who paid for the first time in the
	// window, by the channel that brought them.
	ChannelValue []ChannelValueLine `json:"channelValue"`
	Referrals    ReferralProgramme  `json:"referrals"`
	ByLocale     []LocaleFunnel     `json:"byLocale"`
}

// AcquisitionReport reads what acquiring customers cost and brought.
func (service *Service) AcquisitionReport(ctx context.Context, period Period) (AcquisitionReport, error) {
	queries := service.queries()
	report := AcquisitionReport{
		Since: period.Since, Until: period.Until, Timezone: period.Timezone,
		GeneratedAt: service.now(),
		Campaigns:   []CampaignLine{}, Sources: []SourceLine{}, PromoCodes: []PromoLine{},
		BonusCredit: []BonusLine{}, FreeAccess: []FreeAccessLine{},
		Funnel:       Funnel{ByChannel: []FunnelChannel{}},
		ChannelValue: []ChannelValueLine{},
		Referrals:    ReferralProgramme{Rewards: []ReferralRewardLine{}, Economics: []Economics{}},
		ByLocale:     []LocaleFunnel{},
	}
	window := func() (pgtype.Timestamptz, pgtype.Timestamptz) {
		return timestamp(period.Since), timestamp(period.Until)
	}
	firstDay, lastDay := period.days()
	spendRows, err := queries.AcquisitionSpendTotals(ctx, dbgen.AcquisitionSpendTotalsParams{
		FromDay: date(firstDay), ToDay: date(lastDay),
	})
	if err != nil {
		return report, err
	}
	campaignSpend := map[string]map[string]int64{}
	sourceSpend := map[string]map[string]int64{}
	for _, row := range spendRows {
		if row.CampaignID.Valid {
			add(campaignSpend, uuidString(row.CampaignID), row.Currency, row.AmountMinor)
		} else if row.UtmSource.Valid {
			add(sourceSpend, row.UtmSource.String, row.Currency, row.AmountMinor)
		}
	}

	since, until := window()
	outcomes, err := queries.CampaignAcquisitionOutcomes(ctx, dbgen.CampaignAcquisitionOutcomesParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	revenueRows, err := queries.CampaignAcquisitionRevenue(ctx, dbgen.CampaignAcquisitionRevenueParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	campaignRevenue := map[string]map[string]int64{}
	for _, row := range revenueRows {
		add(campaignRevenue, uuidString(row.CampaignID), row.Currency, row.RevenueMinor)
	}
	for _, row := range outcomes {
		id := uuidString(row.CampaignID)
		spend, revenue := campaignSpend[id], campaignRevenue[id]
		// A campaign with no sign-ups and no spend in the window is noise.
		if row.Signups == 0 && len(spend) == 0 {
			continue
		}
		report.Campaigns = append(report.Campaigns, CampaignLine{
			CampaignID: id, Code: row.Code, Name: row.Name,
			Signups: row.Signups, Payers: row.Payers,
			ConversionRate: rate(row.Payers, row.Signups),
			Economics:      economics(spend, revenue, row.Payers),
		})
	}

	sources, err := queries.SourceAcquisition(ctx, dbgen.SourceAcquisitionParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	seen := map[[2]string]bool{}
	for _, row := range sources {
		spend := sourceSpend[row.UtmSource][row.Currency]
		seen[[2]string{row.UtmSource, row.Currency}] = true
		report.Sources = append(report.Sources, sourceLine(row.UtmSource, row.Currency,
			row.Orders, row.Payers, row.NewPayers, row.RevenueMinor, spend))
	}
	// Money spent on a source that brought nothing is the line most worth
	// seeing, so it is listed rather than dropped for having no orders.
	for source, byCurrency := range sourceSpend {
		for currency, spend := range byCurrency {
			if !seen[[2]string{source, currency}] {
				report.Sources = append(report.Sources, sourceLine(source, currency, 0, 0, 0, 0, spend))
			}
		}
	}
	sort.SliceStable(report.Sources, func(i, j int) bool {
		return report.Sources[i].RevenueMinor+report.Sources[i].SpendMinor >
			report.Sources[j].RevenueMinor+report.Sources[j].SpendMinor
	})

	promos, err := queries.PromoCodePerformance(ctx, dbgen.PromoCodePerformanceParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	for _, row := range promos {
		report.PromoCodes = append(report.PromoCodes, PromoLine{
			Code: row.Code, PromotionKind: row.PromotionKind, Currency: row.Currency,
			Redemptions: row.Redemptions, Customers: row.Customers, NewCustomers: row.NewCustomers,
			DiscountMinor: row.DiscountMinor, RevenueMinor: row.RevenueMinor,
		})
	}
	bonuses, err := queries.BonusWalletCredit(ctx, dbgen.BonusWalletCreditParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	for _, row := range bonuses {
		report.BonusCredit = append(report.BonusCredit, BonusLine{
			Source: row.Source, Currency: row.Currency, Grants: row.Grants, AmountMinor: row.AmountMinor,
		})
	}
	free, err := queries.FreeAccessGranted(ctx, dbgen.FreeAccessGrantedParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	for _, row := range free {
		report.FreeAccess = append(report.FreeAccess, FreeAccessLine{Source: row.Source, Grants: row.Grants, Days: row.Days})
	}

	funnel, err := queries.FunnelSummary(ctx, dbgen.FunnelSummaryParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	report.Funnel.Signups = funnel.Signups
	report.Funnel.PlansViewed = funnel.PlansViewed
	report.Funnel.CheckoutStarted = funnel.CheckoutStarted
	report.Funnel.TrialClaimed = funnel.TrialClaimed
	report.Funnel.Paid = funnel.Paid
	channels, err := queries.FunnelByChannel(ctx, dbgen.FunnelByChannelParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	for _, row := range channels {
		report.Funnel.ByChannel = append(report.Funnel.ByChannel,
			FunnelChannel{Step: row.Step, Channel: row.Channel, Customers: row.Customers})
	}
	locales, err := queries.FunnelByLocale(ctx, dbgen.FunnelByLocaleParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	for _, row := range locales {
		report.ByLocale = append(report.ByLocale, LocaleFunnel{
			Locale: row.Locale, Signups: row.Signups, TrialClaimed: row.TrialClaimed, Paid: row.Paid,
			TrialRate: rate(row.TrialClaimed, row.Signups), PaidRate: rate(row.Paid, row.Signups),
		})
	}

	referralSpend, err := report.readReferrals(ctx, queries, period)
	if err != nil {
		return report, err
	}
	codes := map[string]string{}
	for _, row := range outcomes {
		codes[uuidString(row.CampaignID)] = row.Code
	}
	channelSpend := map[string]map[string]int64{"referral": referralSpend}
	for id, byCurrency := range campaignSpend {
		if code, ok := codes[id]; ok {
			channelSpend["campaign:"+code] = byCurrency
		}
	}
	for source, byCurrency := range sourceSpend {
		channelSpend["utm:"+source] = byCurrency
	}
	value, err := queries.PayerChannelValue(ctx, dbgen.PayerChannelValueParams{Since: since, Until: until})
	if err != nil {
		return report, err
	}
	report.ChannelValue = channelValue(value, channelSpend)
	return report, nil
}

// readReferrals fills the referral programme and returns its money rewards
// per currency, the programme's acquisition cost.
func (report *AcquisitionReport) readReferrals(ctx context.Context, queries *dbgen.Queries, period Period) (map[string]int64, error) {
	since, until := timestamp(period.Since), timestamp(period.Until)
	outcomes, err := queries.ReferralOutcomes(ctx, dbgen.ReferralOutcomesParams{Since: since, Until: until})
	if err != nil {
		return nil, err
	}
	programme := &report.Referrals
	programme.Invited, programme.Qualified = outcomes.Invited, outcomes.Qualified
	programme.Rejected, programme.Payers = outcomes.Rejected, outcomes.Payers
	programme.ConversionRate = rate(outcomes.Payers, outcomes.Invited)
	rewards, err := queries.ReferralRewardCosts(ctx, dbgen.ReferralRewardCostsParams{Since: since, Until: until})
	if err != nil {
		return nil, err
	}
	spend := map[string]int64{}
	for _, row := range rewards {
		programme.Rewards = append(programme.Rewards, ReferralRewardLine{
			Role: row.Role, Currency: row.Currency, Rewards: row.Rewards,
			AmountMinor: row.AmountMinor, Days: row.Days,
		})
		if row.AmountMinor > 0 {
			spend[row.Currency] += row.AmountMinor
		}
	}
	revenueRows, err := queries.ReferralRevenue(ctx, dbgen.ReferralRevenueParams{Since: since, Until: until})
	if err != nil {
		return nil, err
	}
	revenue := map[string]int64{}
	for _, row := range revenueRows {
		revenue[row.Currency] = row.RevenueMinor
	}
	programme.Economics = economics(spend, revenue, outcomes.Payers)
	return spend, nil
}

// channelValue sets each channel's revenue against its spend. Spend in a
// currency nobody in the channel paid in still appears, as a line with no
// payers: money that brought nothing is the line most worth seeing.
func channelValue(rows []dbgen.PayerChannelValueRow, spend map[string]map[string]int64) []ChannelValueLine {
	lines := []ChannelValueLine{}
	seen := map[[2]string]bool{}
	for _, row := range rows {
		seen[[2]string{row.Channel, row.Currency}] = true
		line := ChannelValueLine{
			Channel: row.Channel, Currency: row.Currency, Payers: row.Payers,
			RevenueMinor: row.RevenueMinor, SpendMinor: spend[row.Channel][row.Currency],
			LtvMinor: perUnit(row.RevenueMinor, row.Payers),
		}
		if row.Months > 0 {
			monthly := int64(float64(row.RevenueMinor) / row.Months)
			line.MonthlyRevenueMinor = &monthly
		}
		fillAcquisitionCost(&line)
		lines = append(lines, line)
	}
	for channel, byCurrency := range spend {
		for currency, amount := range byCurrency {
			if amount > 0 && !seen[[2]string{channel, currency}] {
				lines = append(lines, ChannelValueLine{Channel: channel, Currency: currency, SpendMinor: amount})
			}
		}
	}
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].Payers != lines[j].Payers {
			return lines[i].Payers > lines[j].Payers
		}
		if lines[i].Channel != lines[j].Channel {
			return lines[i].Channel < lines[j].Channel
		}
		return lines[i].Currency < lines[j].Currency
	})
	return lines
}

func fillAcquisitionCost(line *ChannelValueLine) {
	if line.SpendMinor <= 0 || line.Payers <= 0 {
		return
	}
	cac := line.SpendMinor / line.Payers
	line.CacMinor = &cac
	if cac <= 0 {
		return
	}
	if line.LtvMinor != nil {
		ratio := float64(*line.LtvMinor) / float64(cac)
		line.LtvToCac = &ratio
	}
	if line.MonthlyRevenueMinor != nil && *line.MonthlyRevenueMinor > 0 {
		months := float64(cac) / float64(*line.MonthlyRevenueMinor)
		line.PaybackMonths = &months
	}
}

func add(target map[string]map[string]int64, key, currency string, amount int64) {
	if target[key] == nil {
		target[key] = map[string]int64{}
	}
	target[key][currency] += amount
}

// economics pairs spend and revenue by currency. CAC divides spend by the
// paying customers the campaign brought, whatever currency they paid in: a
// customer acquired is one customer.
func economics(spend, revenue map[string]int64, payers int64) []Economics {
	currencies := map[string]bool{}
	for currency := range spend {
		currencies[currency] = true
	}
	for currency := range revenue {
		currencies[currency] = true
	}
	lines := make([]Economics, 0, len(currencies))
	for currency := range currencies {
		line := Economics{Currency: currency, SpendMinor: spend[currency], RevenueMinor: revenue[currency]}
		if line.SpendMinor > 0 {
			line.CacMinor = perUnit(line.SpendMinor, payers)
			line.Roi = roi(line.RevenueMinor, line.SpendMinor)
		}
		lines = append(lines, line)
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].Currency < lines[j].Currency })
	return lines
}

func sourceLine(source, currency string, orders, payers, newPayers, revenue, spend int64) SourceLine {
	line := SourceLine{
		UtmSource: source, Currency: currency, Orders: orders, Payers: payers,
		NewPayers: newPayers, RevenueMinor: revenue, SpendMinor: spend,
	}
	if spend > 0 {
		line.CacMinor = perUnit(spend, newPayers)
		line.Roi = roi(revenue, spend)
	}
	return line
}

// roi is (revenue − spend) ÷ spend.
func roi(revenue, spend int64) *float64 {
	if spend <= 0 {
		return nil
	}
	value := float64(revenue-spend) / float64(spend)
	return &value
}

// Spend is one recorded advertising cost.
type Spend struct {
	ID           string    `json:"id"`
	CampaignID   string    `json:"campaignId,omitempty"`
	CampaignCode string    `json:"campaignCode,omitempty"`
	CampaignName string    `json:"campaignName,omitempty"`
	UtmSource    string    `json:"utmSource,omitempty"`
	SpentOn      string    `json:"spentOn"`
	Currency     string    `json:"currency"`
	AmountMinor  int64     `json:"amountMinor"`
	Note         string    `json:"note"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
}

// SpendInput is a new advertising cost.
type SpendInput struct {
	CampaignID  string `json:"campaignId"`
	UtmSource   string `json:"utmSource"`
	SpentOn     string `json:"spentOn"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amountMinor"`
	Note        string `json:"note"`
}

// Actor is the operator a change is attributed to in the audit trail.
type Actor struct {
	AdminID   string
	RequestID string
	Reason    string
	// IdempotencyKey makes a retried creation return what the first one
	// recorded instead of recording the cost twice.
	IdempotencyKey string
}

var (
	utmSourceShape = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)
	currencyShape  = regexp.MustCompile(`^[A-Z]{3}$`)
)

// ListSpend lists the costs recorded for days in the period.
func (service *Service) ListSpend(ctx context.Context, period Period) ([]Spend, error) {
	firstDay, lastDay := period.days()
	rows, err := service.queries().ListAcquisitionSpend(ctx, dbgen.ListAcquisitionSpendParams{
		FromDay: date(firstDay), ToDay: date(lastDay),
	})
	if err != nil {
		return nil, err
	}
	items := make([]Spend, 0, len(rows))
	for _, row := range rows {
		items = append(items, Spend{
			ID: uuidString(row.ID), CampaignID: uuidString(row.CampaignID),
			CampaignCode: row.CampaignCode, CampaignName: row.CampaignName,
			UtmSource: row.UtmSource.String, SpentOn: dayString(row.SpentOn),
			Currency: row.Currency, AmountMinor: row.AmountMinor, Note: row.Note,
			CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt.Time.UTC(),
		})
	}
	return items, nil
}

// CreateSpend records an advertising cost against a campaign or a source,
// with an audit event in the same transaction.
func (service *Service) CreateSpend(ctx context.Context, actor Actor, input SpendInput) (Spend, error) {
	params := dbgen.CreateAcquisitionSpendParams{
		Currency: strings.ToUpper(strings.TrimSpace(input.Currency)), AmountMinor: input.AmountMinor,
		Note: strings.TrimSpace(input.Note), CreatedBy: actor.AdminID,
		IdempotencyKey: text(strings.TrimSpace(actor.IdempotencyKey)),
	}
	campaign := strings.TrimSpace(input.CampaignID)
	source := strings.ToLower(strings.TrimSpace(input.UtmSource))
	switch {
	case campaign != "" && source != "":
		return Spend{}, invalid("a cost belongs to a campaign or a source, not both")
	case campaign != "":
		id, err := parseUUID(campaign)
		if err != nil {
			return Spend{}, err
		}
		params.CampaignID = id
	case source != "":
		if !utmSourceShape.MatchString(source) {
			return Spend{}, invalid("a source is 1 to 64 lowercase letters, digits, dots, dashes, or underscores")
		}
		params.UtmSource = pgtype.Text{String: source, Valid: true}
	default:
		return Spend{}, invalid("choose the campaign or the source the money was spent on")
	}
	if !currencyShape.MatchString(params.Currency) {
		return Spend{}, invalid("the currency is a three-letter code")
	}
	if params.AmountMinor <= 0 {
		return Spend{}, invalid("the amount must be positive")
	}
	if utf8.RuneCountInString(params.Note) > 200 {
		return Spend{}, invalid("the note is at most 200 characters")
	}
	spentOn, err := time.Parse(time.DateOnly, strings.TrimSpace(input.SpentOn))
	if err != nil {
		return Spend{}, invalid("the date is YYYY-MM-DD")
	}
	if spentOn.After(service.now().AddDate(0, 0, 1)) {
		return Spend{}, invalid("the date is in the future")
	}
	params.SpentOn = date(spentOn)
	if actor.AdminID == "" {
		return Spend{}, invalid("the change needs an operator")
	}

	if len(params.IdempotencyKey.String) > 128 {
		return Spend{}, invalid("the idempotency key is at most 128 characters")
	}
	var created dbgen.CreateAcquisitionSpendRow
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		var txErr error
		created, txErr = queries.CreateAcquisitionSpend(ctx, params)
		if txErr != nil || !created.Inserted {
			return txErr
		}
		return audit(ctx, queries, actor, "acquisition_spend.created", uuidString(created.ID), map[string]any{
			"currency": created.Currency, "amountMinor": created.AmountMinor,
			"spentOn": dayString(created.SpentOn), "campaignId": uuidString(created.CampaignID),
			"utmSource": created.UtmSource.String,
		})
	})
	if err != nil {
		if isForeignKeyViolation(err) {
			return Spend{}, invalid("that campaign does not exist")
		}
		return Spend{}, err
	}
	return Spend{
		ID: uuidString(created.ID), CampaignID: uuidString(created.CampaignID),
		UtmSource: created.UtmSource.String, SpentOn: dayString(created.SpentOn),
		Currency: created.Currency, AmountMinor: created.AmountMinor, Note: created.Note,
		CreatedBy: created.CreatedBy, CreatedAt: created.CreatedAt.Time.UTC(),
	}, nil
}

// DeleteSpend removes a cost entered by mistake. The audit trail keeps what
// it was.
func (service *Service) DeleteSpend(ctx context.Context, actor Actor, id string) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, err := queries.DeleteAcquisitionSpend(ctx, parsed)
		if err != nil {
			return err
		}
		if removed == 0 {
			return ErrNotFound
		}
		return audit(ctx, queries, actor, "acquisition_spend.deleted", id, nil)
	})
}

func audit(ctx context.Context, queries *dbgen.Queries, actor Actor, action, targetID string, metadata map[string]any) error {
	return auditTarget(ctx, queries, actor, "acquisition_spend", action, targetID, metadata)
}

// auditTarget records a financial change by an operator against one record.
func auditTarget(ctx context.Context, queries *dbgen.Queries, actor Actor, targetType, action, targetID string, metadata map[string]any) error {
	encoded := []byte("{}")
	if len(metadata) > 0 {
		var err error
		if encoded, err = json.Marshal(metadata); err != nil {
			return err
		}
	}
	_, err := queries.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{
		ActorType: "admin", ActorID: text(actor.AdminID), Action: action, Category: "financial",
		Outcome: "success", TargetType: targetType, TargetID: targetID,
		Reason: text(actor.Reason), RequestID: text(actor.RequestID), Metadata: encoded,
	})
	return err
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func isForeignKeyViolation(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == "23503"
}

// Funnel steps and channels recorded by RecordFunnelStep.
const (
	StepPlansViewed     = "plans_viewed"
	StepCheckoutStarted = "checkout_started"
	ChannelBot          = "bot"
	ChannelWeb          = "web"
)

// RecordFunnelStep notes that a customer reached a step, the first time only.
// It is called on the customer's own request path and must never fail it, so
// the caller logs an error and carries on.
func (service *Service) RecordFunnelStep(ctx context.Context, customerID, step, channel string) error {
	if service == nil || customerID == "" {
		return nil
	}
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	return service.queries().RecordFunnelEvent(ctx, dbgen.RecordFunnelEventParams{
		UserID: id, Step: step, Channel: channel,
	})
}
