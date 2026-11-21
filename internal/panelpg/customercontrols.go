package panelpg

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// RestrictionKinds are the spending a restriction can stop.
var RestrictionKinds = []string{"topup", "purchase", "support"}

// Restriction stops one kind of spending for a customer, with the reason.
type Restriction struct {
	Kind      string    `json:"kind"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
}

// CustomerControls is the state the card's control panel renders.
type CustomerControls struct {
	Restrictions   []Restriction `json:"restrictions"`
	AutoRenew      *AutoRenew    `json:"autoRenew,omitempty"`
	LastActivityAt *time.Time    `json:"lastActivityAt,omitempty"`
}

// AutoRenew is the customer's renewal switch as the card shows it.
type AutoRenew struct {
	Enabled   bool   `json:"enabled"`
	Consented bool   `json:"consented"`
	Provider  string `json:"provider,omitempty"`
	Funding   string `json:"funding"`
}

// CustomerControlState reads restrictions, auto-renew, and last activity.
func (service *Service) CustomerControlState(ctx context.Context, customerID string) (CustomerControls, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return CustomerControls{}, err
	}
	queries := service.queries()
	controls := CustomerControls{Restrictions: []Restriction{}}
	rows, err := queries.ListCustomerRestrictions(ctx, id)
	if err != nil {
		return CustomerControls{}, err
	}
	for _, row := range rows {
		controls.Restrictions = append(controls.Restrictions, Restriction{Kind: row.Kind, Reason: row.Reason, CreatedAt: row.CreatedAt.Time})
	}
	if renew, renewErr := queries.GetCustomerAutoRenew(ctx, id); renewErr == nil {
		controls.AutoRenew = &AutoRenew{Enabled: renew.Enabled, Consented: renew.Consented, Provider: renew.Provider.String, Funding: renew.Funding}
	} else if !errors.Is(renewErr, pgx.ErrNoRows) {
		return CustomerControls{}, renewErr
	}
	last, err := queries.CustomerLastActivity(ctx, id)
	if err != nil {
		return CustomerControls{}, notFound(err)
	}
	controls.LastActivityAt = timePointer(last)
	return controls, nil
}

// SetCustomerRestriction stops or (with an empty reason) lifts one kind of
// spending. The store refuses a restricted order before any money moves.
func (service *Service) SetCustomerRestriction(ctx context.Context, customerID, kind, reason string, restrict bool, actor Actor) error {
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if !slices.Contains(RestrictionKinds, kind) || restrict && (reason == "" || utf8.RuneCountInString(reason) > 500) {
		return ErrValidaton
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		action := "customer.restriction.lifted"
		if restrict {
			action = "customer.restriction.set"
			createdBy := pgtype.UUID{}
			if actor.AdminID != "" {
				createdBy, _ = parseUUID(actor.AdminID)
			}
			if _, err := queries.PutCustomerRestriction(ctx, dbgen.PutCustomerRestrictionParams{
				UserID: id, Kind: kind, Reason: reason, CreatedBy: createdBy,
			}); err != nil {
				return notFound(err)
			}
		} else if _, err := queries.DeleteCustomerRestriction(ctx, dbgen.DeleteCustomerRestrictionParams{UserID: id, Kind: kind}); err != nil {
			return err
		}
		return appendAudit(ctx, queries, actor.audit(action, "customer", "customer", customerID,
			map[string]any{"kind": kind, "reason": reason}))
	})
}

// ResetCustomerTrial forgets the customer's trial claim, so they may take a
// trial again.
func (service *Service) ResetCustomerTrial(ctx context.Context, customerID string, actor Actor) (int64, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return 0, err
	}
	var removed int64
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if removed, err = queries.ResetCustomerTrial(ctx, id); err != nil {
			return err
		}
		return appendAudit(ctx, queries, actor.audit("customer.trial.reset", "customer", "customer", customerID,
			map[string]any{"claims": removed}))
	})
	return removed, err
}

// SetCustomerAutoRenew turns renewal off, or back on where the customer
// consented before. An operator cannot consent on a customer's behalf, so
// turning on a renewal the customer never set up is refused.
func (service *Service) SetCustomerAutoRenew(ctx context.Context, customerID string, enabled bool, actor Actor) error {
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		changed, err := queries.SetCustomerAutoRenew(ctx, dbgen.SetCustomerAutoRenewParams{UserID: id, Enabled: enabled})
		if err != nil {
			return err
		}
		if changed == 0 {
			if enabled {
				return ErrRejected
			}
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit("customer.auto_renew.set", "customer", "customer", customerID,
			map[string]any{"enabled": enabled}))
	})
}

// ActivityEntry is one customer action from the activity log.
type ActivityEntry struct {
	Surface    string    `json:"surface"`
	Action     string    `json:"action"`
	OccurredAt time.Time `json:"occurredAt"`
}

// CustomerActivityLog reads the customer's latest actions.
func (service *Service) CustomerActivityLog(ctx context.Context, customerID string) ([]ActivityEntry, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListCustomerActivity(ctx, dbgen.ListCustomerActivityParams{UserID: id, PageSize: 100})
	if err != nil {
		return nil, err
	}
	entries := make([]ActivityEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, ActivityEntry{Surface: row.Surface, Action: row.Action, OccurredAt: row.OccurredAt.Time})
	}
	return entries, nil
}

// ButtonStat is how often one bot screen or button was used.
type ButtonStat struct {
	Action    string `json:"action"`
	Clicks    int64  `json:"clicks"`
	Customers int64  `json:"customers"`
}

// ButtonStats aggregates bot usage over the last days, with no customer in it.
func (service *Service) ButtonStats(ctx context.Context, days int) ([]ButtonStat, error) {
	if days <= 0 || days > 90 {
		days = 7
	}
	rows, err := service.queries().ButtonClickStats(ctx, timestamp(service.now().AddDate(0, 0, -days)))
	if err != nil {
		return nil, err
	}
	stats := make([]ButtonStat, 0, len(rows))
	for _, row := range rows {
		stats = append(stats, ButtonStat{Action: row.Action, Clicks: row.Clicks, Customers: row.Customers})
	}
	return stats, nil
}

// AdvancedFilter is the customer list's filtered and sorted view.
type AdvancedFilter struct {
	Status           string
	ExpiringDays     int
	WithBalance      bool
	AutoRenew        bool
	Restricted       bool
	TrialOnly        bool
	Campaign         string
	ActiveWithinDays int
	TrafficPercent   int
	Sort             string
}

// AdvancedCustomer is one row of that view.
type AdvancedCustomer struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"`
	TelegramID     *int64     `json:"telegramId,omitempty"`
	BalanceMinor   int64      `json:"balanceMinor"`
	EndsAt         *time.Time `json:"endsAt,omitempty"`
	LastActivityAt *time.Time `json:"lastActivityAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// SearchCustomersAdvanced answers the filtered, sorted list: the first 200.
func (service *Service) SearchCustomersAdvanced(ctx context.Context, filter AdvancedFilter) ([]AdvancedCustomer, error) {
	optionalInt := func(value int) pgtype.Int4 { return pgtype.Int4{Int32: int32(value), Valid: value > 0} }
	if !slices.Contains([]string{"newest", "oldest", "balance", "expiry", "activity"}, filter.Sort) {
		filter.Sort = "newest"
	}
	rows, err := service.queries().SearchCustomersAdvanced(ctx, dbgen.SearchCustomersAdvancedParams{
		Status: pgtype.Text{String: filter.Status, Valid: filter.Status != ""}, ExpiringDays: optionalInt(filter.ExpiringDays),
		WithBalance: filter.WithBalance, AutoRenew: filter.AutoRenew, Restricted: filter.Restricted, TrialOnly: filter.TrialOnly,
		Campaign:         pgtype.Text{String: filter.Campaign, Valid: filter.Campaign != ""},
		ActiveWithinDays: optionalInt(filter.ActiveWithinDays), TrafficPercent: optionalInt(min(filter.TrafficPercent, 100)),
		Sort: filter.Sort,
	})
	if err != nil {
		return nil, err
	}
	customers := make([]AdvancedCustomer, 0, len(rows))
	for _, row := range rows {
		customer := AdvancedCustomer{
			ID: uuidString(row.User.ID), Status: row.User.Status, BalanceMinor: row.BalanceMinor,
			EndsAt: timePointer(row.EndsAt), LastActivityAt: timePointer(row.User.LastActivityAt), CreatedAt: row.User.CreatedAt.Time,
		}
		if row.TelegramID.Valid {
			telegram := row.TelegramID.Int64
			customer.TelegramID = &telegram
		}
		customers = append(customers, customer)
	}
	return customers, nil
}

// LegacyRecord is one row the previous bot kept about a customer.
type LegacyRecord struct {
	Kind       string          `json:"kind"`
	SourceID   string          `json:"sourceId"`
	OccurredAt *time.Time      `json:"occurredAt,omitempty"`
	Data       json.RawMessage `json:"data"`
}

// LegacyKindCount is how many rows of one kind a customer has.
type LegacyKindCount struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

// legacyPage is how many archived rows one page carries.
const legacyPage = 50

// CustomerLegacyRecords reads a page of the previous bot's history about a
// customer, optionally of one kind, with the count of every kind.
func (service *Service) CustomerLegacyRecords(
	ctx context.Context, customerID, kind string, skip int32,
) ([]LegacyRecord, []LegacyKindCount, int64, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, nil, 0, err
	}
	queries := service.queries()
	counts, err := queries.CountCustomerLegacyRecords(ctx, id)
	if err != nil {
		return nil, nil, 0, err
	}
	kinds := make([]LegacyKindCount, 0, len(counts))
	total := int64(0)
	for _, row := range counts {
		kinds = append(kinds, LegacyKindCount{Kind: row.Kind, Count: row.Count})
		if kind == "" || row.Kind == kind {
			total += row.Count
		}
	}
	rows, err := queries.ListCustomerLegacyRecords(ctx, dbgen.ListCustomerLegacyRecordsParams{
		UserID: id, Kind: optionalText(kind), PageSize: legacyPage, Skip: max(skip, 0),
	})
	if err != nil {
		return nil, nil, 0, err
	}
	records := make([]LegacyRecord, 0, len(rows))
	for _, row := range rows {
		record := LegacyRecord{Kind: row.Kind, SourceID: row.SourceID, Data: json.RawMessage(row.Data)}
		if row.OccurredAt.Valid {
			at := row.OccurredAt.Time.UTC()
			record.OccurredAt = &at
		}
		records = append(records, record)
	}
	return records, kinds, total, nil
}
