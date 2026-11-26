package panelpg

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Shared-device verdicts.
const (
	SharedDeviceOpen       = "open"
	SharedDeviceLegitimate = "legitimate"
	SharedDeviceConfirmed  = "confirmed"
)

// SharedDevice is one device seen on two or more customers.
//
// The device is named only by its handle, the digest the customer's own device
// list uses; the hardware ID is never stored. What an operator weighs is on
// the row: how many accounts, how many of them took a trial, and whether one
// invited another.
type SharedDevice struct {
	Handle        string                `json:"handle"`
	Platform      string                `json:"platform,omitempty"`
	Accounts      int32                 `json:"accounts"`
	TrialAccounts int32                 `json:"trialAccounts"`
	ReferralPairs int32                 `json:"referralPairs"`
	Status        string                `json:"status"`
	DetectedAt    time.Time             `json:"detectedAt"`
	LastSeenAt    time.Time             `json:"lastSeenAt"`
	Customers     []SharedDeviceAccount `json:"customers"`
	// The latest verdict. On a reopened device it is the earlier one, kept so
	// the operator sees what was decided for fewer accounts.
	ReviewedAccounts int32      `json:"reviewedAccounts,omitempty"`
	ReviewReason     string     `json:"reviewReason,omitempty"`
	ReviewedBy       string     `json:"reviewedBy,omitempty"`
	ReviewedAt       *time.Time `json:"reviewedAt,omitempty"`
}

// SharedDeviceAccount is one customer on a shared device.
type SharedDeviceAccount struct {
	CustomerID   string    `json:"customerId"`
	FirstSeenAt  time.Time `json:"firstSeenAt"`
	LastSeenAt   time.Time `json:"lastSeenAt"`
	TrialClaimed bool      `json:"trialClaimed"`
	// InvitedBy is the customer who invited this one, when the inviter is on
	// the same device.
	InvitedBy string `json:"invitedBy,omitempty"`
}

// SharedDeviceFilter narrows the queue.
type SharedDeviceFilter struct {
	Status     string
	CustomerID string
	// MinAccounts keeps only devices with at least this many customers.
	MinAccounts int32
	Cursor      string
	PageSize    int32
}

// SharedDevicePage is one page of the queue.
type SharedDevicePage struct {
	Items      []SharedDevice `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// SearchSharedDevices reads the shared-device queue, newest activity first.
func (service *Service) SearchSharedDevices(
	ctx context.Context, filter SharedDeviceFilter,
) (SharedDevicePage, error) {
	switch filter.Status {
	case "", SharedDeviceOpen, SharedDeviceLegitimate, SharedDeviceConfirmed:
	default:
		return SharedDevicePage{}, ErrValidaton
	}
	customer := pgtype.UUID{}
	if filter.CustomerID != "" {
		id, err := parseUUID(filter.CustomerID)
		if err != nil {
			return SharedDevicePage{}, err
		}
		customer = id
	}
	size := pageSize(filter.PageSize)
	params := dbgen.SearchSharedDevicesParams{
		Status: optionalText(filter.Status), UserID: customer, PageSize: size + 1,
	}
	if filter.MinAccounts > 2 {
		params.MinAccounts = pgtype.Int4{Int32: filter.MinAccounts, Valid: true}
	}
	if at, handle, ok := decodeHandleCursor(filter.Cursor); ok {
		params.CursorLastSeenAt = timestamp(at)
		params.CursorHandle = pgtype.Text{String: handle, Valid: true}
	}
	queries := service.queries()
	rows, err := queries.SearchSharedDevices(ctx, params)
	if err != nil {
		return SharedDevicePage{}, err
	}
	page := SharedDevicePage{Items: make([]SharedDevice, 0, min(len(rows), int(size)))}
	if len(rows) > int(size) {
		last := rows[size-1]
		page.NextCursor = timeValue(last.LastSeenAt).UTC().Format(time.RFC3339Nano) + "|" + last.DeviceHandle
		rows = rows[:size]
	}
	handles := make([]string, 0, len(rows))
	for _, row := range rows {
		handles = append(handles, row.DeviceHandle)
		page.Items = append(page.Items, sharedDeviceFrom(row))
	}
	if len(handles) == 0 {
		return page, nil
	}
	accounts, err := queries.ListSharedDeviceAccounts(ctx, handles)
	if err != nil {
		return SharedDevicePage{}, err
	}
	byHandle := map[string][]SharedDeviceAccount{}
	for _, account := range accounts {
		byHandle[account.DeviceHandle] = append(byHandle[account.DeviceHandle], SharedDeviceAccount{
			CustomerID:   uuidString(account.UserID),
			FirstSeenAt:  timeValue(account.FirstSeenAt),
			LastSeenAt:   timeValue(account.LastSeenAt),
			TrialClaimed: account.TrialClaimed,
			InvitedBy:    uuidString(account.InvitedBy),
		})
	}
	for index := range page.Items {
		page.Items[index].Customers = byHandle[page.Items[index].Handle]
		if page.Items[index].Customers == nil {
			page.Items[index].Customers = []SharedDeviceAccount{}
		}
	}
	return page, nil
}

// sharedDeviceLegitimateReason is recorded when an operator marks a device
// legitimate without writing why: lifting a hold is not an adverse decision.
const sharedDeviceLegitimateReason = "legitimate"

// ReviewSharedDevice marks an open device `legitimate` (a family, a shared
// work phone). It stops the device refusing trials and holding referrals,
// covers the customers on the device now, and another customer joining
// reopens it. A reason is optional. The other verdict, a ban, is
// BanSharedDevice.
func (service *Service) ReviewSharedDevice(
	ctx context.Context, handle, status string, actor Actor,
) (SharedDevice, error) {
	if status != SharedDeviceLegitimate {
		return SharedDevice{}, ErrValidaton
	}
	reason := strings.TrimSpace(actor.Reason)
	if reason == "" {
		reason = sharedDeviceLegitimateReason
	}
	if actor.AdminID == "" || !validHandle(handle) {
		return SharedDevice{}, ErrValidaton
	}
	admin, err := parseUUID(actor.AdminID)
	if err != nil {
		return SharedDevice{}, err
	}
	var device SharedDevice
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.GetSharedDevice(ctx, handle); txErr != nil {
			if errors.Is(txErr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return txErr
		}
		row, txErr := queries.ReviewSharedDevice(ctx, dbgen.ReviewSharedDeviceParams{
			DeviceHandle: handle, Status: status,
			ReviewReason: optionalText(clipText(reason, 400)), ReviewedBy: admin,
		})
		if txErr != nil {
			return rejected(txErr)
		}
		device = sharedDeviceFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.shared_device.reviewed", "risk", "shared_device", handle,
			map[string]any{"status": status, "accounts": row.Accounts},
		))
	})
	return device, err
}

// SharedDeviceBan is a ban of customers found on one device.
type SharedDeviceBan struct {
	// CustomerIDs are the customers to ban; each must be on the device.
	CustomerIDs []string `json:"customerIds"`
	// Reason is one of BanTemplates. It is recorded on every customer.
	Reason string `json:"reason"`
	// Notify sends the reason's template to each banned customer.
	Notify bool `json:"notify"`
	// Days is how long the ban lasts; zero is no end.
	Days int `json:"days"`
	// AnnulSubscriptions disables the customers' subscriptions in Remnawave.
	// The caller queues that through fulfillment; this records the request.
	AnnulSubscriptions bool `json:"annulSubscriptions"`
}

// SharedDeviceBanResult is what one ban did, per customer.
type SharedDeviceBanResult struct {
	Device    SharedDevice         `json:"device"`
	Customers []SharedDeviceBanned `json:"customers"`
}

// SharedDeviceBanned is one customer's outcome.
type SharedDeviceBanned struct {
	CustomerID string     `json:"customerId"`
	Outcome    string     `json:"outcome"`
	EndsAt     *time.Time `json:"endsAt,omitempty"`
	// Annulled counts the subscriptions queued to be disabled.
	Annulled int `json:"annulled"`
}

// maxBanDays bounds a timed ban; longer is "no end".
const maxBanDays = 3650

// BanSharedDevice suspends the chosen customers on a device, each with the
// reason and, when asked, the reason's message, ending the ban after Days,
// and records the device as a confirmed second account.
//
// Each customer goes through SetCustomerStatus, so a ban here is the same
// suspension, lifecycle event, and audit entry as one from the customer page.
// Disabling subscriptions is queued by the caller, which owns fulfillment.
func (service *Service) BanSharedDevice(
	ctx context.Context, handle string, input SharedDeviceBan, actor Actor, now time.Time,
) (SharedDeviceBanResult, error) {
	message, known := BanTemplates[input.Reason]
	if !known || actor.AdminID == "" || !validHandle(handle) ||
		len(input.CustomerIDs) == 0 || input.Days < 0 || input.Days > maxBanDays {
		return SharedDeviceBanResult{}, ErrValidaton
	}
	queries := service.queries()
	device, err := queries.GetSharedDevice(ctx, handle)
	if errors.Is(err, pgx.ErrNoRows) {
		return SharedDeviceBanResult{}, ErrNotFound
	}
	if err != nil {
		return SharedDeviceBanResult{}, err
	}
	if device.Status != SharedDeviceOpen {
		return SharedDeviceBanResult{}, ErrRejected
	}
	onDevice, err := queries.ListSharedDeviceCustomers(ctx, handle)
	if err != nil {
		return SharedDeviceBanResult{}, err
	}
	present := map[string]bool{}
	for _, id := range onDevice {
		present[uuidString(id)] = true
	}
	chosen := map[string]bool{}
	for _, id := range input.CustomerIDs {
		if !present[id] || chosen[id] {
			return SharedDeviceBanResult{}, ErrValidaton
		}
		chosen[id] = true
	}

	reason := "shared_device:" + input.Reason
	banActor := actor
	banActor.Reason = reason
	var endsAt *time.Time
	if input.Days > 0 {
		end := now.UTC().Add(time.Duration(input.Days) * 24 * time.Hour)
		endsAt = &end
	}
	result := SharedDeviceBanResult{Customers: make([]SharedDeviceBanned, 0, len(input.CustomerIDs))}
	for _, customerID := range input.CustomerIDs {
		outcome := SharedDeviceBanned{CustomerID: customerID, Outcome: "suspended", EndsAt: endsAt}
		if _, err = service.SetCustomerStatus(ctx, customerID, "suspended", banActor); err != nil {
			return SharedDeviceBanResult{}, err
		}
		if endsAt != nil {
			id, _ := parseUUID(customerID)
			if err = queries.SetSuspensionEnd(ctx, dbgen.SetSuspensionEndParams{
				UserID: id, EndsAt: timestamp(*endsAt),
			}); err != nil {
				return SharedDeviceBanResult{}, err
			}
		}
		if input.Notify {
			if _, err = service.MessageCustomer(ctx, customerID, message, banActor); err != nil {
				outcome.Outcome = "suspended_not_notified"
			}
		}
		result.Customers = append(result.Customers, outcome)
	}

	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.ReviewSharedDevice(ctx, dbgen.ReviewSharedDeviceParams{
			DeviceHandle: handle, Status: SharedDeviceConfirmed,
			ReviewReason: optionalText(reason), ReviewedBy: optionalUUID(actor.AdminID),
		})
		if txErr != nil {
			return rejected(txErr)
		}
		result.Device = sharedDeviceFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.shared_device.banned", "risk", "shared_device", handle,
			map[string]any{
				"reason": input.Reason, "customers": len(input.CustomerIDs), "days": input.Days,
				"notify": input.Notify, "annulSubscriptions": input.AnnulSubscriptions,
			},
		))
	})
	return result, err
}

// LiveEntitlements lists the entitlements of a customer's open subscriptions
// that are still running, for a ban that annuls them.
func (service *Service) LiveEntitlements(ctx context.Context, customerID string) ([]SubscriptionTarget, error) {
	customer, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListCustomerSubscriptionsDetailed(ctx, customer)
	if err != nil {
		return nil, err
	}
	targets := []SubscriptionTarget{}
	for _, row := range rows {
		if row.Subscription.Status != "active" || !row.EntitlementID.Valid {
			continue
		}
		targets = append(targets, SubscriptionTarget{
			SubscriptionID:  uuidString(row.Subscription.ID),
			EntitlementID:   uuidString(row.EntitlementID),
			RemnawaveUserID: row.Subscription.RemnawaveUserID.Int64,
			Label:           row.Subscription.Label,
			Status:          row.Subscription.Status,
		})
	}
	return targets, nil
}

// LiftExpiredBans restores customers whose timed ban has run out. It is the
// worker's, so the lifecycle event and audit entry name the system.
func (service *Service) LiftExpiredBans(ctx context.Context, now time.Time) (int, error) {
	expired, err := service.queries().ListExpiredSuspensions(ctx, dbgen.ListExpiredSuspensionsParams{
		Now: timestamp(now), PageSize: 500,
	})
	if err != nil {
		return 0, err
	}
	lifted := 0
	for _, id := range expired {
		err = service.inTx(ctx, func(queries *dbgen.Queries) error {
			if _, txErr := queries.ApplyCustomerLifecycle(ctx, dbgen.ApplyCustomerLifecycleParams{
				UserID: id, Status: "active",
			}); txErr != nil {
				return notFound(txErr)
			}
			if _, txErr := queries.InsertCustomerLifecycleEvent(ctx, dbgen.InsertCustomerLifecycleEventParams{
				UserID: id, Action: "restored", Reason: "ban_expired", ActorType: "system",
			}); txErr != nil {
				return txErr
			}
			if txErr := queries.SetSuspensionEnd(ctx, dbgen.SetSuspensionEndParams{UserID: id}); txErr != nil {
				return txErr
			}
			return appendAudit(ctx, queries, Actor{Type: "system", Reason: "ban_expired"}.audit(
				"customer.ban_expired", "customer", "customer", uuidString(id), map[string]any{},
			))
		})
		if err != nil {
			return lifted, err
		}
		lifted++
	}
	return lifted, nil
}

func sharedDeviceFrom(row dbgen.SharedDevice) SharedDevice {
	return SharedDevice{
		Handle:           row.DeviceHandle,
		Platform:         textValue(row.Platform),
		Accounts:         row.Accounts,
		TrialAccounts:    row.TrialAccounts,
		ReferralPairs:    row.ReferralPairs,
		Status:           row.Status,
		DetectedAt:       timeValue(row.DetectedAt),
		LastSeenAt:       timeValue(row.LastSeenAt),
		ReviewedAccounts: row.ReviewedAccounts.Int32,
		ReviewReason:     textValue(row.ReviewReason),
		ReviewedBy:       uuidString(row.ReviewedBy),
		ReviewedAt:       timePointer(row.ReviewedAt),
	}
}

// decodeHandleCursor reads "<last seen>|<handle>". An unreadable cursor is
// the first page, as everywhere else in the panel.
func decodeHandleCursor(value string) (time.Time, string, bool) {
	at, handle, found := strings.Cut(strings.TrimSpace(value), "|")
	if !found || !validHandle(handle) {
		return time.Time{}, "", false
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, "", false
	}
	return parsed, handle, true
}

// validHandle accepts what commerce.DeviceHandle produces: unpadded
// URL-safe base64.
func validHandle(handle string) bool {
	if len(handle) < 8 || len(handle) > 64 {
		return false
	}
	for _, character := range handle {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9', character == '-', character == '_':
		default:
			return false
		}
	}
	return true
}

func clipText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}
