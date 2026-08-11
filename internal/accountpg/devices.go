package accountpg

import (
	"context"
	"fmt"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// Device is one connected device as the customer sees it.
//
// There is deliberately no HWID and no IP address here. The handle is a digest
// the server can resolve back, the name is whatever the client reported about
// itself, and the timestamp is a date rather than a precise last-seen instant —
// enough for the customer to recognise their own laptop, not enough to build a
// movement log out of.
type Device struct {
	Handle   string
	Name     string
	Platform string
	LastSeen time.Time
}

// Devices lists the devices connected to one subscription.
func (service *Service) Devices(
	ctx context.Context, customerID, subscriptionID string,
) ([]Device, error) {
	row, err := service.subscriptionRecord(ctx, customerID, subscriptionID, "")
	if err != nil {
		return nil, err
	}
	if row.RemnawaveID <= 0 {
		return nil, ErrNotProvisioned
	}
	if service.remnawave == nil {
		return nil, ErrRemnawaveUnavailable
	}
	devices, err := service.remnawave.Devices(ctx, row.RemnawaveID)
	if err != nil {
		return nil, ErrRemnawaveUnavailable
	}
	return projectDevices(devices), nil
}

func projectDevices(devices remnawave.Devices) []Device {
	projected := make([]Device, 0, len(devices.Devices))
	for _, device := range devices.Devices {
		projected = append(projected, Device{
			Handle:   commerce.DeviceHandle(device.HWID),
			Name:     firstNonEmpty(device.DeviceModel, device.Platform),
			Platform: derefString(device.Platform),
			LastSeen: device.UpdatedAt.UTC(),
		})
	}
	return projected
}

// RemoveDevice disconnects one device by its handle.
//
// The handle is resolved against the current device list rather than trusted:
// the server hashes each HWID it holds and matches, so the caller never supplies
// the identifier being acted on. A handle that matches nothing is reported as
// not found, which is also what a device removed from another tab a moment ago
// looks like.
func (service *Service) RemoveDevice(
	ctx context.Context, customerID, subscriptionID, handle string, request SecurityRequest,
) error {
	row, err := service.subscriptionRecord(ctx, customerID, subscriptionID, "")
	if err != nil {
		return err
	}
	if row.RemnawaveID <= 0 {
		return ErrNotProvisioned
	}
	if service.remnawave == nil {
		return ErrRemnawaveUnavailable
	}

	devices, err := service.remnawave.Devices(ctx, row.RemnawaveID)
	if err != nil {
		return ErrRemnawaveUnavailable
	}
	target := ""
	name := ""
	for _, device := range devices.Devices {
		if commerce.DeviceHandle(device.HWID) == handle {
			target, name = device.HWID, firstNonEmpty(device.DeviceModel, device.Platform)
			break
		}
	}
	if target == "" {
		return ErrNotFound
	}

	if err = service.remnawave.DeleteDevice(ctx, row.RemnawaveID, target); err != nil {
		return ErrRemnawaveUnavailable
	}
	// The recorded metadata is the customer's own device label. The HWID stays
	// on the server, including out of the log the customer reads.
	service.recordSecurity(ctx, customerID, "device_removed", request, map[string]any{
		"subscription": row.Label, "device": name,
	})
	return nil
}

// RemoveAllDevices disconnects every device on one subscription.
//
// It is a separate operation rather than a loop over RemoveDevice because
// Remnawave offers it as one call: doing it device by device would leave a
// partially disconnected subscription behind if the third call failed.
func (service *Service) RemoveAllDevices(
	ctx context.Context, customerID, subscriptionID string, request SecurityRequest,
) (int, error) {
	row, err := service.subscriptionRecord(ctx, customerID, subscriptionID, "")
	if err != nil {
		return 0, err
	}
	if row.RemnawaveID <= 0 {
		return 0, ErrNotProvisioned
	}
	if service.remnawave == nil {
		return 0, ErrRemnawaveUnavailable
	}

	devices, err := service.remnawave.Devices(ctx, row.RemnawaveID)
	if err != nil {
		return 0, ErrRemnawaveUnavailable
	}
	if devices.Total == 0 {
		return 0, nil
	}
	if err = service.remnawave.DeleteAllDevices(ctx, row.RemnawaveID); err != nil {
		return 0, ErrRemnawaveUnavailable
	}
	service.recordSecurity(ctx, customerID, "devices_removed_all", request, map[string]any{
		"subscription": row.Label, "removed": devices.Total,
	})
	return devices.Total, nil
}

// RenameSubscription stores the customer's own label for a subscription.
func (service *Service) RenameSubscription(
	ctx context.Context, customerID, subscriptionID, label string,
) error {
	normalized, err := commerce.NormalizeSubscriptionLabel(label)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
	}
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	target, err := parseUUID(subscriptionID)
	if err != nil {
		return ErrNotFound
	}
	if _, err = dbgen.New(service.pool).RenameAccountSubscription(ctx, dbgen.RenameAccountSubscriptionParams{
		SubscriptionID: target, UserID: userID, Label: normalized,
	}); err != nil {
		return ErrNotFound
	}
	return nil
}

func firstNonEmpty(values ...*string) string {
	for _, value := range values {
		if value != nil && *value != "" {
			return *value
		}
	}
	return ""
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
