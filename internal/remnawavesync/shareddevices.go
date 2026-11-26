package remnawavesync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// Shared devices.
//
// A second account on the same phone is how a trial is taken twice and how a
// customer invites themselves for the referral reward. Remnawave already knows
// which HWIDs connected through each subscription; this pass reads that list,
// keeps one sighting per device and customer, and puts every device seen on
// two or more customers in the risk queue. It also holds a referral for review
// when the invited customer is on the inviter's device.
//
// It never suspends anybody. A family on one tablet looks exactly like a
// customer with a second account, so what the pass produces is a row a person
// reads and a verdict they record.

// DeviceLister is the panel read the scan needs.
type DeviceLister interface {
	AllDevices(ctx context.Context, start, size int) ([]remnawave.PanelDevice, int, error)
}

// SightingRetention is how long a device that stopped connecting is kept as
// evidence. Long enough to catch a trial taken again months later, short
// enough that the table does not become a history of every phone a customer
// ever owned.
const SightingRetention = 180 * 24 * time.Hour

// devicePage is how many devices one panel request returns, and how many
// rows one statement writes.
const devicePage = 1000

// DeviceScanner keeps device sightings and the shared-device queue.
type DeviceScanner struct {
	pool      *pgxpool.Pool
	panel     DeviceLister
	operators []int64
	logger    *slog.Logger
}

// NewDeviceScanner builds the scan. Customers signed in with one of the
// operator Telegram IDs are left out: an operator testing on their own phone
// with several accounts is not a customer abusing trials.
func NewDeviceScanner(pool *pgxpool.Pool, panel DeviceLister, operatorTelegramIDs []int64, logger *slog.Logger) *DeviceScanner {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeviceScanner{pool: pool, panel: panel, operators: operatorTelegramIDs, logger: logger}
}

// DeviceScanResult is what one scan saw and changed.
type DeviceScanResult struct {
	Devices         int
	Sightings       int
	Shared          int64
	NewlyOpen       int
	ReferralsHeld   int
	PrunedSightings int64
}

type sighting struct {
	handle   string
	user     pgtype.UUID
	platform string
	seenAt   time.Time
}

// Scan reads every device the panel knows and refreshes the queue. It is
// idempotent: a second run over the same panel state changes nothing.
func (scanner *DeviceScanner) Scan(ctx context.Context, now time.Time) (DeviceScanResult, error) {
	var result DeviceScanResult
	byPanelUser := map[int64][]remnawave.PanelDevice{}
	for start := 0; ; start += devicePage {
		devices, total, err := scanner.panel.AllDevices(ctx, start, devicePage)
		if err != nil {
			return result, err
		}
		for _, device := range devices {
			if device.Handle == "" || device.UserID <= 0 {
				continue
			}
			byPanelUser[device.UserID] = append(byPanelUser[device.UserID], device)
			result.Devices++
		}
		if len(devices) < devicePage || start+len(devices) >= total {
			break
		}
		if err = ctx.Err(); err != nil {
			return result, err
		}
	}

	queries := dbgen.New(scanner.pool)
	sightings, err := scanner.resolve(ctx, queries, byPanelUser)
	if err != nil {
		return result, err
	}
	result.Sightings = len(sightings)

	var opened []string
	err = pgx.BeginFunc(ctx, scanner.pool, func(tx pgx.Tx) error {
		queries := dbgen.New(tx)
		before, txErr := queries.ListOpenSharedDeviceHandles(ctx)
		if txErr != nil {
			return txErr
		}
		for chunk := range slices.Chunk(sightings, devicePage) {
			params := dbgen.UpsertDeviceSightingsParams{
				Handles: make([]string, 0, len(chunk)), UserIds: make([]pgtype.UUID, 0, len(chunk)),
				Platforms: make([]string, 0, len(chunk)), SeenAt: make([]pgtype.Timestamptz, 0, len(chunk)),
			}
			for _, seen := range chunk {
				params.Handles = append(params.Handles, seen.handle)
				params.UserIds = append(params.UserIds, seen.user)
				params.Platforms = append(params.Platforms, seen.platform)
				params.SeenAt = append(params.SeenAt, pgtype.Timestamptz{Time: seen.seenAt, Valid: true})
			}
			if txErr = queries.UpsertDeviceSightings(ctx, params); txErr != nil {
				return txErr
			}
		}
		if result.PrunedSightings, txErr = queries.PruneDeviceSightings(ctx, pgtype.Timestamptz{
			Time: now.Add(-SightingRetention), Valid: true,
		}); txErr != nil {
			return txErr
		}
		if result.Shared, txErr = queries.RefreshSharedDevices(ctx); txErr != nil {
			return txErr
		}
		if _, txErr = queries.DropUnsharedDevices(ctx); txErr != nil {
			return txErr
		}
		held, txErr := queries.FlagSharedDeviceReferrals(ctx)
		if txErr != nil {
			return txErr
		}
		for _, referred := range held {
			if txErr = queries.AttachReferralSignal(ctx, dbgen.AttachReferralSignalParams{
				ReferredUserID: referred, Code: "shared_device",
			}); txErr != nil {
				return txErr
			}
		}
		result.ReferralsHeld = len(held)
		after, txErr := queries.ListOpenSharedDeviceHandles(ctx)
		if txErr != nil {
			return txErr
		}
		opened = newlyOpen(before, after)
		result.NewlyOpen = len(opened)
		return scanner.announce(ctx, queries, opened, len(after))
	})
	if err != nil {
		return result, err
	}
	scanner.logger.Info("shared device scan finished", "devices", result.Devices, "sightings", result.Sightings,
		"shared", result.Shared, "newly_open", result.NewlyOpen, "referrals_held", result.ReferralsHeld,
		"pruned", result.PrunedSightings)
	return result, nil
}

// resolve maps panel users to customers and folds the devices into one
// sighting per device and customer — a customer with two subscriptions on one
// phone is one sighting, not two.
func (scanner *DeviceScanner) resolve(
	ctx context.Context, queries *dbgen.Queries, byPanelUser map[int64][]remnawave.PanelDevice,
) ([]sighting, error) {
	panelUsers := make([]int64, 0, len(byPanelUser))
	for id := range byPanelUser {
		panelUsers = append(panelUsers, id)
	}
	slices.Sort(panelUsers)
	customers := map[int64][]pgtype.UUID{}
	for chunk := range slices.Chunk(panelUsers, devicePage) {
		rows, err := queries.ResolveRemnawaveCustomers(ctx, chunk)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			customers[row.RemnawaveID] = append(customers[row.RemnawaveID], row.UserID)
		}
	}
	exempt := map[pgtype.UUID]bool{}
	if len(scanner.operators) > 0 {
		ids := make([]string, 0, len(scanner.operators))
		for _, id := range scanner.operators {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		operators, err := queries.ListCustomersWithTelegramIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, id := range operators {
			exempt[id] = true
		}
	}

	type key struct {
		handle string
		user   pgtype.UUID
	}
	folded := map[key]*sighting{}
	for panelUser, devices := range byPanelUser {
		for _, customer := range customers[panelUser] {
			if exempt[customer] {
				continue
			}
			for _, device := range devices {
				platform := ""
				if device.Platform != nil {
					platform = clip(*device.Platform, 64)
				}
				entry := folded[key{device.Handle, customer}]
				if entry == nil {
					folded[key{device.Handle, customer}] = &sighting{
						handle: device.Handle, user: customer, platform: platform, seenAt: device.UpdatedAt,
					}
					continue
				}
				if device.UpdatedAt.After(entry.seenAt) {
					entry.seenAt = device.UpdatedAt
					if platform != "" {
						entry.platform = platform
					}
				}
			}
		}
	}
	sightings := make([]sighting, 0, len(folded))
	for _, entry := range folded {
		if entry.seenAt.IsZero() {
			entry.seenAt = time.Now()
		}
		sightings = append(sightings, *entry)
	}
	slices.SortFunc(sightings, func(left, right sighting) int {
		if left.handle != right.handle {
			if left.handle < right.handle {
				return -1
			}
			return 1
		}
		return slices.Compare(left.user.Bytes[:], right.user.Bytes[:])
	})
	return sightings, nil
}

// announce tells the operator group about devices that entered the queue in
// this scan. It names how many, never whom: the chat is not where a customer
// is looked up.
func (scanner *DeviceScanner) announce(ctx context.Context, queries *dbgen.Queries, opened []string, open int) error {
	if len(opened) == 0 {
		return nil
	}
	digest := sha256.New()
	for _, handle := range opened {
		digest.Write([]byte(handle + "\n"))
	}
	payload, err := json.Marshal(map[string]any{
		"event": "shared_devices", "devices": strconv.Itoa(len(opened)), "open": strconv.Itoa(open),
	})
	if err != nil {
		return err
	}
	_, err = queries.EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{
		Kind: "monitoring", DedupeKey: "shared_devices:" + hex.EncodeToString(digest.Sum(nil)[:12]), Payload: payload,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// newlyOpen is what is open now and was not before, sorted.
func newlyOpen(before, after []string) []string {
	previous := make(map[string]bool, len(before))
	for _, handle := range before {
		previous[handle] = true
	}
	var opened []string
	for _, handle := range after {
		if !previous[handle] {
			opened = append(opened, handle)
		}
	}
	slices.Sort(opened)
	return opened
}

func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return text
}
