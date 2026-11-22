package fulfillment

import (
	"context"
	"errors"
	"fmt"
	"time"

	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// SyncField is one attribute of a subscription as Omniflow and Remnawave each
// hold it. Field names are the drift kinds a reconcile records: expiry,
// traffic, device_limit, squads, status.
type SyncField struct {
	Field     string `json:"field"`
	Omniflow  any    `json:"omniflow"`
	Remnawave any    `json:"remnawave"`
	Matches   bool   `json:"matches"`
}

// SyncPreview is what the panel shows before an operator pushes Omniflow's
// state to one subscription's Remnawave user.
type SyncPreview struct {
	EntitlementID     string `json:"entitlementId"`
	EntitlementStatus string `json:"entitlementStatus"`
	RemnawaveUserID   int64  `json:"remnawaveUserId,omitempty"`
	// RemoteExists is false when the subscription has no Remnawave user, or
	// the one it points at is gone; applying then creates or recovers it.
	RemoteExists bool        `json:"remoteExists"`
	InSync       bool        `json:"inSync"`
	Fields       []SyncField `json:"fields"`
	CheckedAt    time.Time   `json:"checkedAt"`
}

// UserReader reads one Remnawave user. The sync preview needs nothing else
// from the panel, and it never writes.
type UserReader interface {
	User(context.Context, int64) (remnawave.User, error)
}

// compareFields lists every attribute the fulfillment pipeline pushes, with
// the value each side holds. driftsBetween is its mismatching part, so the
// drift a reconcile records and the diff the panel shows cannot disagree.
func compareFields(desired desiredState, entitlementStatus string, remote remnawave.User) []SyncField {
	remoteSquads := make([]string, 0, len(remote.ActiveInternalSquads))
	for _, squad := range remote.ActiveInternalSquads {
		remoteSquads = append(remoteSquads, squad.UUID)
	}
	reconciled := reconciledStatus(entitlementStatus, remote)
	return []SyncField{
		{
			Field: "expiry", Omniflow: desired.remoteExpireAt, Remnawave: remote.ExpireAt,
			Matches: sameInstant(remote.ExpireAt, desired.remoteExpireAt),
		},
		// A nil desired limit means unlimited, and unlimited is a value
		// Remnawave can disagree with: a user still carrying the previous
		// plan's cap is drift, not "nothing configured".
		{
			Field: "traffic", Omniflow: desired.TrafficAllowanceBytes, Remnawave: remote.TrafficLimitBytes,
			Matches: trafficLimitAgrees(desired.TrafficAllowanceBytes, remote.TrafficLimitBytes),
		},
		{
			Field: "device_limit", Omniflow: desired.DeviceLimit, Remnawave: remote.HWIDDeviceLimit,
			Matches: deviceLimitAgrees(desired.DeviceLimit, remote.HWIDDeviceLimit),
		},
		{
			Field: "squads", Omniflow: desired.SquadIDs, Remnawave: remoteSquads,
			Matches: sameStringSet(desired.SquadIDs, remoteSquads),
		},
		{
			Field: "status", Omniflow: entitlementStatus, Remnawave: reconciled,
			Matches: reconciled == entitlementStatus,
		},
	}
}

// sameInstant compares two expiries at the precision Remnawave keeps them.
// PostgreSQL stores microseconds and Remnawave milliseconds, so an expiry
// pushed as …19.508415 comes back as …19.508; compared exactly, every
// reconcile would see drift and push the same value again.
func sameInstant(left, right time.Time) bool {
	return left.Truncate(time.Millisecond).Equal(right.Truncate(time.Millisecond))
}

// PreviewSync compares one entitlement's state with its Remnawave user without
// changing either. The desired state is built exactly as the scheduled
// reconcile builds it — the entitlement's own expiry, limits, and squads, with
// the plan's grace folded into the expiry — so the diff is what a reconcile
// would push.
func (service *Service) PreviewSync(
	ctx context.Context, entitlementID string, reader UserReader,
) (SyncPreview, error) {
	id, err := parseUUID(entitlementID)
	if err != nil {
		return SyncPreview{}, err
	}
	queries := dbgen.New(service.pool)
	entitlement, err := queries.GetEntitlement(ctx, id)
	if err != nil {
		return SyncPreview{}, err
	}
	desired := desiredState{
		EndsAt:   entitlement.EndsAt.Time,
		SquadIDs: databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds),
	}
	if entitlement.TrafficAllowanceBytes.Valid {
		value := entitlement.TrafficAllowanceBytes.Int64
		desired.TrafficAllowanceBytes = &value
	}
	if entitlement.DeviceLimit.Valid {
		value := int(entitlement.DeviceLimit.Int32)
		desired.DeviceLimit = &value
	}
	graceSeconds, err := queries.GetPlanVersionGracePeriod(ctx, entitlement.PlanVersionID)
	if err != nil {
		return SyncPreview{}, err
	}
	desired = desired.withGrace(time.Duration(graceSeconds) * time.Second)
	// The resolution rules are the worker's, read-only: they take nothing
	// from the worker but the queries they are handed.
	resolver := &Worker{}
	subscription, err := resolver.subscriptionFor(ctx, queries, entitlement)
	if err != nil {
		return SyncPreview{}, err
	}
	remoteID, err := resolver.remoteUserID(ctx, queries, entitlement, subscription)
	if err != nil {
		return SyncPreview{}, err
	}
	preview := SyncPreview{
		EntitlementID: entitlementID, EntitlementStatus: entitlement.Status,
		RemnawaveUserID: remoteID, CheckedAt: service.clock().UTC(),
	}
	var remote remnawave.User
	if remoteID != 0 {
		remote, err = reader.User(ctx, remoteID)
		switch {
		case errors.Is(err, remnawave.ErrNotFound):
		case err != nil:
			return SyncPreview{}, fmt.Errorf("read remnawave user: %w", err)
		default:
			preview.RemoteExists = true
		}
	}
	if !preview.RemoteExists {
		// Nothing to compare against: every value is Omniflow's alone, and
		// applying creates or recovers the user.
		for _, field := range compareFields(desired, entitlement.Status, remnawave.User{}) {
			field.Remnawave, field.Matches = nil, false
			preview.Fields = append(preview.Fields, field)
		}
		return preview, nil
	}
	preview.Fields = compareFields(desired, entitlement.Status, remote)
	preview.InSync = true
	for _, field := range preview.Fields {
		preview.InSync = preview.InSync && field.Matches
	}
	return preview, nil
}

// SyncFollowUp is the extra operation a sync needs when the two sides disagree
// on whether the user is switched on. A reconcile pushes the expiry, limits,
// and squads but adopts the remote status, so Omniflow's status is pushed with
// an explicit enable or disable. It returns "" when no such step is needed.
func SyncFollowUp(preview SyncPreview) string {
	if !preview.RemoteExists {
		return ""
	}
	for _, field := range preview.Fields {
		if field.Field != "status" || field.Matches {
			continue
		}
		remote, _ := field.Remnawave.(string)
		switch preview.EntitlementStatus {
		case "active", "limited":
			if remote == "disabled" {
				return "enable"
			}
		case "paused", "disabled":
			if remote == "active" || remote == "limited" {
				return "disable"
			}
		}
	}
	return ""
}
