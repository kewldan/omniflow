package remnawavesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/valkey-io/valkey-go"
)

// MonitorSettings are the operator's usage-monitoring passes. Both read what
// Remnawave already counts; Omniflow keeps no traffic ledger of its own. The
// only state between passes is the previous counter reading, held in Valkey
// with a TTL — temporary state, which is what Valkey is for.
type MonitorSettings struct {
	// FastInterval and FastThresholdBytes flag a user whose counter grew by
	// at least the threshold since the previous fast pass. Zero interval is
	// off.
	FastInterval       time.Duration
	FastThresholdBytes int64
	// DailyThresholdBytes flags a user whose counter grew by at least this
	// much over a day. Zero is off.
	DailyThresholdBytes int64
	// MonitoredNodes limits the passes to users last connected through these
	// nodes; IgnoredNodes leaves users of these out. UUIDs.
	MonitoredNodes map[string]bool
	IgnoredNodes   map[string]bool
	// ExcludedUsers are panel user IDs never reported.
	ExcludedUsers map[int64]bool
	// Cooldown is how long a reported user is not reported again.
	Cooldown time.Duration
	// DeviceReport posts a daily summary of users with more active devices
	// than their limit.
	DeviceReport bool
}

// MonitorSettingsFromEnv reads APP_TRAFFIC_MONITOR_* and
// APP_DEVICE_OVERFLOW_REPORT.
func MonitorSettingsFromEnv(getenv func(string) string) (MonitorSettings, error) {
	gigabytes := func(key string) (int64, error) {
		raw := strings.TrimSpace(getenv(key))
		if raw == "" {
			return 0, nil
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 {
			return 0, fmt.Errorf("%s must be a number of gigabytes", key)
		}
		return int64(value * (1 << 30)), nil
	}
	settings := MonitorSettings{Cooldown: time.Hour}
	var err error
	if settings.FastThresholdBytes, err = gigabytes("APP_TRAFFIC_MONITOR_FAST_THRESHOLD_GB"); err != nil {
		return MonitorSettings{}, err
	}
	if settings.DailyThresholdBytes, err = gigabytes("APP_TRAFFIC_MONITOR_DAILY_THRESHOLD_GB"); err != nil {
		return MonitorSettings{}, err
	}
	if raw := strings.TrimSpace(getenv("APP_TRAFFIC_MONITOR_FAST_INTERVAL_MINUTES")); raw != "" {
		minutes, parseErr := strconv.Atoi(raw)
		if parseErr != nil || minutes < 1 || minutes > 1440 {
			return MonitorSettings{}, errors.New("APP_TRAFFIC_MONITOR_FAST_INTERVAL_MINUTES must be 1 to 1440")
		}
		settings.FastInterval = time.Duration(minutes) * time.Minute
	} else if settings.FastThresholdBytes > 0 {
		settings.FastInterval = 10 * time.Minute
	}
	if settings.FastThresholdBytes == 0 {
		settings.FastInterval = 0
	}
	if raw := strings.TrimSpace(getenv("APP_TRAFFIC_MONITOR_COOLDOWN_MINUTES")); raw != "" {
		minutes, parseErr := strconv.Atoi(raw)
		if parseErr != nil || minutes < 0 {
			return MonitorSettings{}, errors.New("APP_TRAFFIC_MONITOR_COOLDOWN_MINUTES must be a whole number")
		}
		settings.Cooldown = time.Duration(minutes) * time.Minute
	}
	settings.MonitoredNodes = stringSet(getenv("APP_TRAFFIC_MONITOR_NODES"))
	settings.IgnoredNodes = stringSet(getenv("APP_TRAFFIC_MONITOR_IGNORED_NODES"))
	settings.ExcludedUsers = map[int64]bool{}
	for _, raw := range strings.Split(getenv("APP_TRAFFIC_MONITOR_EXCLUDED_USERS"), ",") {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || id <= 0 {
			return MonitorSettings{}, fmt.Errorf("APP_TRAFFIC_MONITOR_EXCLUDED_USERS: %q is not a panel user ID", raw)
		}
		settings.ExcludedUsers[id] = true
	}
	settings.DeviceReport, _ = strconv.ParseBool(strings.TrimSpace(getenv("APP_DEVICE_OVERFLOW_REPORT")))
	return settings, nil
}

func stringSet(raw string) map[string]bool {
	set := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			set[value] = true
		}
	}
	return set
}

// MonitorPanel is the read set the passes need.
type MonitorPanel interface {
	ListUsers(ctx context.Context, start, size int) ([]remnawave.User, int, error)
	AllDevices(ctx context.Context, start, size int) ([]remnawave.PanelDevice, int, error)
}

// SnapshotStore keeps one counter reading between passes, and a per-user
// cooldown.
type SnapshotStore interface {
	Load(ctx context.Context, key string) (map[int64]int64, error)
	Save(ctx context.Context, key string, counters map[int64]int64, ttl time.Duration) error
	Once(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

// Monitor runs the usage passes.
type Monitor struct {
	pool     *pgxpool.Pool
	panel    MonitorPanel
	store    SnapshotStore
	settings MonitorSettings
	logger   *slog.Logger
}

// NewMonitor builds the passes.
func NewMonitor(pool *pgxpool.Pool, panel MonitorPanel, store SnapshotStore, settings MonitorSettings, logger *slog.Logger) *Monitor {
	return &Monitor{pool: pool, panel: panel, store: store, settings: settings, logger: logger}
}

// Settings reports what this monitor was configured with.
func (monitor *Monitor) Settings() MonitorSettings { return monitor.settings }

const userPage = 500

// eachUser pages through every panel user.
func (monitor *Monitor) eachUser(ctx context.Context, visit func(remnawave.User)) error {
	for start := 0; ; start += userPage {
		users, total, err := monitor.panel.ListUsers(ctx, start, userPage)
		if err != nil {
			return err
		}
		for _, user := range users {
			visit(user)
		}
		if len(users) < userPage || start+len(users) >= total || ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// watched reports whether a user is inside the operator's filters.
func (monitor *Monitor) watched(user remnawave.User) bool {
	if monitor.settings.ExcludedUsers[user.ID] {
		return false
	}
	status := strings.ToUpper(user.Status)
	if status == "DISABLED" || status == "EXPIRED" {
		return false
	}
	node := strings.ToLower(user.Traffic.LastConnectedNodeUUID)
	if len(monitor.settings.MonitoredNodes) > 0 && !monitor.settings.MonitoredNodes[node] {
		return false
	}
	return !monitor.settings.IgnoredNodes[node]
}

// TrafficPass compares every watched user's counter with the previous
// reading of this window and reports the users whose counter grew by at
// least the threshold. The first pass of a window only takes the reading.
func (monitor *Monitor) TrafficPass(ctx context.Context, window string, threshold int64, keep time.Duration) error {
	if threshold <= 0 || monitor.store == nil {
		return nil
	}
	key := "omniflow:traffic-monitor:" + window
	previous, err := monitor.store.Load(ctx, key)
	if err != nil {
		return err
	}
	current := map[int64]int64{}
	var flagged []int64
	err = monitor.eachUser(ctx, func(user remnawave.User) {
		current[user.ID] = user.Traffic.UsedBytes
		before, seen := previous[user.ID]
		// A counter that went down was reset; it has no growth to report.
		if !seen || user.Traffic.UsedBytes < before || !monitor.watched(user) {
			return
		}
		if user.Traffic.UsedBytes-before >= threshold {
			flagged = append(flagged, user.ID)
		}
	})
	if err != nil {
		return err
	}
	if err = monitor.store.Save(ctx, key, current, keep); err != nil {
		return err
	}
	reported := flagged[:0]
	for _, id := range flagged {
		if fresh, onceErr := monitor.store.Once(ctx, key+":cooldown:"+strconv.FormatInt(id, 10), max(monitor.settings.Cooldown, time.Second)); onceErr == nil && fresh {
			reported = append(reported, id)
		}
	}
	if len(reported) == 0 {
		return nil
	}
	return monitor.notify(ctx, "traffic_spike:"+window+":"+strconv.FormatInt(time.Now().Unix(), 10), map[string]any{
		"event": "traffic_spike", "window": window,
		"gigabytes": fmt.Sprintf("%.1f", float64(threshold)/(1<<30)),
		"users":     joinIDs(reported, 20),
	})
}

// activeDeviceWindow is how recently a device must have connected to count
// against its user's limit, as Bedolaga counts them.
const activeDeviceWindow = 30 * 24 * time.Hour

// DeviceOverflowPass reports users with more recently active devices than
// their panel limit. A limit of zero is unlimited and never overflows.
func (monitor *Monitor) DeviceOverflowPass(ctx context.Context, now time.Time) error {
	counts := map[int64]int{}
	for start := 0; ; start += userPage {
		devices, total, err := monitor.panel.AllDevices(ctx, start, userPage)
		if err != nil {
			return err
		}
		for _, device := range devices {
			if now.Sub(device.UpdatedAt) <= activeDeviceWindow {
				counts[device.UserID]++
			}
		}
		if len(devices) < userPage || start+len(devices) >= total || ctx.Err() != nil {
			break
		}
	}
	var over []string
	err := monitor.eachUser(ctx, func(user remnawave.User) {
		if user.HWIDDeviceLimit == nil || *user.HWIDDeviceLimit <= 0 || monitor.settings.ExcludedUsers[user.ID] {
			return
		}
		if counts[user.ID] > *user.HWIDDeviceLimit {
			over = append(over, fmt.Sprintf("%d:%d/%d", user.ID, counts[user.ID], *user.HWIDDeviceLimit))
		}
	})
	if err != nil || len(over) == 0 {
		return err
	}
	sort.Strings(over)
	shown := over
	if len(shown) > 20 {
		shown = shown[:20]
	}
	return monitor.notify(ctx, "devices_over_limit:"+now.UTC().Format(time.DateOnly), map[string]any{
		"event": "devices_over_limit", "users": strconv.Itoa(len(over)), "devices": strings.Join(shown, ", "),
	})
}

func (monitor *Monitor) notify(ctx context.Context, dedupe string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = dbgen.New(monitor.pool).EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{
		Kind: "monitoring", DedupeKey: dedupe, Payload: encoded,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func joinIDs(ids []int64, limit int) string {
	parts := make([]string, 0, min(len(ids), limit))
	for index, id := range ids {
		if index == limit {
			parts = append(parts, fmt.Sprintf("+%d", len(ids)-limit))
			break
		}
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ", ")
}

// ValkeySnapshots keeps the readings in Valkey.
type ValkeySnapshots struct{ Client valkey.Client }

// Load reads a reading, or an empty one when there is none yet.
func (store ValkeySnapshots) Load(ctx context.Context, key string) (map[int64]int64, error) {
	raw, err := store.Client.Do(ctx, store.Client.B().Get().Key(key).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return map[int64]int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	counters := map[int64]int64{}
	if err = json.Unmarshal([]byte(raw), &counters); err != nil {
		return map[int64]int64{}, nil
	}
	return counters, nil
}

// Save stores a reading with its TTL.
func (store ValkeySnapshots) Save(ctx context.Context, key string, counters map[int64]int64, ttl time.Duration) error {
	encoded, err := json.Marshal(counters)
	if err != nil {
		return err
	}
	return store.Client.Do(ctx, store.Client.B().Set().Key(key).Value(string(encoded)).Px(ttl).Build()).Error()
}

// Once claims a cooldown key.
func (store ValkeySnapshots) Once(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	err := store.Client.Do(ctx, store.Client.B().Set().Key(key).Value("1").Nx().Px(ttl).Build()).Error()
	if valkey.IsValkeyNil(err) {
		return false, nil
	}
	return err == nil, err
}
