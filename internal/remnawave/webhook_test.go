package remnawave

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookSignatureVerifies(t *testing.T) {
	t.Parallel()
	body := `{"event":"user.first_connected","data":{"id":7}}`
	if !VerifyWebhook("s3cret", []byte(body), sign("s3cret", body)) {
		t.Fatal("a correct signature was refused")
	}
	for _, signature := range []string{"", "zz", sign("other", body), sign("s3cret", body+" ")} {
		if VerifyWebhook("s3cret", []byte(body), signature) {
			t.Fatalf("signature %q verified", signature)
		}
	}
	if VerifyWebhook("", []byte(body), sign("", body)) {
		t.Fatal("an empty secret must verify nothing")
	}
}

func TestWebhookKeepsOnlyTheAllowlist(t *testing.T) {
	t.Parallel()
	threshold, err := ParseWebhook([]byte(`{"scope":"user","event":"user.bandwidth_usage_threshold_reached",
		"data":{"id":42,"subscriptionUrl":"https://sub.example/abc","lastTriggeredThreshold":80,
		"trafficLimitBytes":1073741824,"expireAt":"2026-12-01T00:00:00.000Z","telegramId":5}}`))
	if err != nil {
		t.Fatal(err)
	}
	if threshold.UserID != 42 || threshold.Details["thresholdPercent"] != int64(80) ||
		threshold.Details["trafficLimitBytes"] != int64(1073741824) || threshold.Details["expireAt"] != "2026-12-01T00:00:00Z" {
		t.Fatalf("threshold = %+v", threshold)
	}
	for key := range threshold.Details {
		if strings.Contains(strings.ToLower(key), "url") || key == "telegramId" {
			t.Fatalf("%s leaked into details", key)
		}
	}

	device, err := ParseWebhook([]byte(`{"event":"user_hwid_devices.added","data":{"user":{"id":9},
		"hwidUserDevice":{"hwid":"HW-1","userId":9,"platform":"iOS","deviceModel":"iPhone 15","userAgent":"Happ/1"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if device.UserID != 9 || device.Scope != "user_hwid_devices" || device.Details["platform"] != "iOS" ||
		device.Details["deviceModel"] != "iPhone 15" || device.Details["deviceDigest"] == "" {
		t.Fatalf("device = %+v", device)
	}
	for _, value := range device.Details {
		if value == "HW-1" {
			t.Fatal("the hardware ID was kept")
		}
	}

	node, err := ParseWebhook([]byte(`{"event":"node.connection_lost","data":{"id":3,"name":"Frankfurt 1","address":"10.0.0.1"}}`))
	if err != nil || node.UserID != 0 || node.Details["node"] != "Frankfurt 1" || node.Details["address"] != nil {
		t.Fatalf("node = %+v (%v)", node, err)
	}

	waiting, err := ParseWebhook([]byte(`{"event":"user.not_connected","data":{"id":"11"},"meta":{"notConnectedAfterHours":24}}`))
	if err != nil || waiting.UserID != 11 || waiting.Details["afterHours"] != int64(24) {
		t.Fatalf("not connected = %+v (%v)", waiting, err)
	}
}

func TestMalformedWebhooksAreRefused(t *testing.T) {
	t.Parallel()
	for _, body := range []string{``, `[]`, `{"data":{}}`, `{"event":""}`, `not json`} {
		if _, err := ParseWebhook([]byte(body)); err == nil {
			t.Fatalf("%q parsed", body)
		}
	}
	// A data field of the wrong shape keeps the event and drops the payload.
	event, err := ParseWebhook([]byte(`{"event":"service.panel_started","data":[1,2]}`))
	if err != nil || event.Scope != "service" || len(event.Details) != 0 {
		t.Fatalf("event = %+v (%v)", event, err)
	}
}
