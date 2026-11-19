package remnawave

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// WebhookSignatureHeader carries the hex HMAC-SHA256 of the raw body, keyed
// with the panel's WEBHOOK_SECRET_HEADER value.
const WebhookSignatureHeader = "X-Remnawave-Signature"

// MaxWebhookBody bounds one delivery. The panel's envelopes are a few
// kilobytes; anything larger is not one.
const MaxWebhookBody = 64 << 10

// ErrWebhookMalformed reports a body that is not the panel's envelope.
var ErrWebhookMalformed = errors.New("remnawave webhook is malformed")

// VerifyWebhook reports whether the signature is the body's HMAC under the
// secret. An empty secret verifies nothing.
func VerifyWebhook(secret string, body []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	received, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), received)
}

// WebhookEvent is a delivery reduced to what Omniflow acts on.
//
// Details is an allowlist: the panel's user object carries the access link,
// its device object the hardware ID, and neither may be stored. What remains
// is enough to word a notice — a percentage, a device's own label, a node's
// name — and nothing that identifies a person beyond the panel's user ID.
type WebhookEvent struct {
	Event   string
	Scope   string
	UserID  int64
	Details map[string]any
}

// Customer-facing events, by the name the panel gives them.
const (
	EventFirstConnected     = "user.first_connected"
	EventNotConnected       = "user.not_connected"
	EventBandwidthReached   = "user.bandwidth_usage_threshold_reached"
	EventTrafficReset       = "user.traffic_reset"
	EventRevoked            = "user.revoked"
	EventTorrentBlocked     = "torrent_blocker.report"
	EventDeviceAdded        = "user_hwid_devices.added"
	EventDeviceDeleted      = "user_hwid_devices.deleted"
	EventNodeConnectionLost = "node.connection_lost"
	EventNodeRestored       = "node.connection_restored"
)

// ParseWebhook decodes the panel's envelope: `{event, scope?, timestamp?,
// data, meta?}`, where `event` is the fully qualified name.
func ParseWebhook(body []byte) (WebhookEvent, error) {
	var envelope struct {
		Event string          `json:"event"`
		Scope string          `json:"scope"`
		Data  json.RawMessage `json:"data"`
		Meta  map[string]any  `json:"meta"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return WebhookEvent{}, ErrWebhookMalformed
	}
	name := strings.TrimSpace(envelope.Event)
	if name == "" || len(name) > 100 || !utf8.ValidString(name) {
		return WebhookEvent{}, ErrWebhookMalformed
	}
	scope := strings.TrimSpace(envelope.Scope)
	if scope == "" {
		scope, _, _ = strings.Cut(name, ".")
	}
	var data map[string]any
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, &data); err != nil {
			// A scalar or list where an object belongs: keep the event, drop
			// the payload.
			data = nil
		}
	}
	event := WebhookEvent{Event: name, Scope: scope, Details: map[string]any{}}
	user, _ := data["user"].(map[string]any)
	device, _ := data["hwidUserDevice"].(map[string]any)
	node, _ := data["node"].(map[string]any)
	for _, candidate := range []any{data["id"], user["id"], device["userId"]} {
		if id, ok := panelID(candidate); ok {
			event.UserID = id
			break
		}
	}
	// A node event's data is the node itself, so its `id` is not a user.
	if scope == "node" {
		event.UserID = 0
	}

	switch {
	case name == EventBandwidthReached:
		if percent, ok := firstNumber(data["lastTriggeredThreshold"], data["thresholdPercent"], data["threshold"], envelope.Meta["thresholdPercent"]); ok && percent > 0 && percent <= 1000 {
			event.Details["thresholdPercent"] = percent
		}
		copyUserLimits(event.Details, data)
	case name == EventNotConnected:
		if hours, ok := firstNumber(envelope.Meta["notConnectedAfterHours"], data["notConnectedAfterHours"]); ok && hours > 0 {
			event.Details["afterHours"] = hours
		}
	case scope == "user_hwid_devices":
		if label := shortLabel(device["deviceModel"]); label != "" {
			event.Details["deviceModel"] = label
		}
		if label := shortLabel(device["platform"]); label != "" {
			event.Details["platform"] = label
		}
		if hwid, ok := device["hwid"].(string); ok && hwid != "" {
			// A digest the customer surfaces can match an alias against;
			// the hardware ID itself is not kept.
			event.Details["deviceDigest"] = hwidDigest(hwid)
		}
	case scope == "node":
		if label := shortLabel(data["name"]); label != "" {
			event.Details["node"] = label
		}
		if label := shortLabel(data["countryCode"]); label != "" {
			event.Details["country"] = label
		}
	case name == EventTorrentBlocked:
		if label := shortLabel(node["name"]); label != "" {
			event.Details["node"] = label
		}
	case scope == "crm":
		if label := shortLabel(data["nodeName"]); label != "" {
			event.Details["node"] = label
		}
		if label := shortLabel(data["providerName"]); label != "" {
			event.Details["provider"] = label
		}
	}
	return event, nil
}

func copyUserLimits(details map[string]any, data map[string]any) {
	if limit, ok := firstNumber(data["trafficLimitBytes"]); ok && limit > 0 {
		details["trafficLimitBytes"] = limit
	}
	if raw, ok := data["expireAt"].(string); ok {
		if expires, err := time.Parse(time.RFC3339, raw); err == nil {
			details["expireAt"] = expires.UTC().Format(time.RFC3339)
		}
	}
}

func panelID(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if typed > 0 && typed == float64(int64(typed)) {
			return int64(typed), true
		}
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil && parsed > 0 {
			return parsed, true
		}
	}
	return 0, false
}

func firstNumber(values ...any) (int64, bool) {
	for _, value := range values {
		switch typed := value.(type) {
		case float64:
			return int64(typed), true
		case string:
			cleaned := strings.TrimSuffix(strings.TrimSpace(typed), "%")
			if parsed, err := strconv.ParseFloat(cleaned, 64); err == nil {
				return int64(parsed), true
			}
		}
	}
	return 0, false
}

// shortLabel keeps a short printable label and drops anything else.
func shortLabel(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" || !utf8.ValidString(text) {
		return ""
	}
	if utf8.RuneCountInString(text) > 64 {
		runes := []rune(text)
		text = string(runes[:64])
	}
	return text
}

// hwidDigest is the same digest commerce.DeviceHandle produces, repeated here
// so the adapter does not import the domain.
func hwidDigest(hwid string) string {
	sum := sha256.Sum256([]byte("omniflow.device." + hwid))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}
