package botapp

import (
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/remnawave"
)

func TestPanelEventsBecomeTheRightNotices(t *testing.T) {
	cases := []struct {
		event   string
		details map[string]any
		kind    string
		want    string
	}{
		{remnawave.EventFirstConnected, nil, "connection", "You are connected"},
		{remnawave.EventNotConnected, nil, "connection", "not connected yet"},
		{remnawave.EventTrafficReset, nil, "access", "Traffic reset"},
		{remnawave.EventRevoked, nil, "access", "re-issued"},
		{remnawave.EventTorrentBlocked, nil, "access", "Torrent"},
		{remnawave.EventDeviceAdded, map[string]any{"deviceModel": "<b>Pixel</b>", "platform": "Android"}, "device", "&lt;b&gt;Pixel&lt;/b&gt; (Android)"},
		{remnawave.EventDeviceDeleted, map[string]any{"alias": "Kitchen tablet", "platform": "iPadOS"}, "device", "Kitchen tablet"},
		{remnawave.EventDeviceAdded, map[string]any{}, "device", "unnamed"},
	}
	for _, testCase := range cases {
		notice, ok := panelEventNotice(notices{}, LocaleEnglish, 7, testCase.event, testCase.details, "")
		if !ok || notice.Kind != testCase.kind || notice.Dedupe != "panel:7" || !strings.Contains(notice.View.Text, testCase.want) {
			t.Fatalf("%s: %+v (%v)", testCase.event, notice, ok)
		}
	}

	threshold, ok := panelEventNotice(notices{}, LocaleEnglish, 9, remnawave.EventBandwidthReached,
		map[string]any{"thresholdPercent": float64(80), "trafficLimitBytes": float64(1024), "expireAt": "2026-12-01T00:00:00Z"}, "")
	if !ok || threshold.Kind != "traffic" || threshold.Dedupe != "80:1024:2026-12-01" {
		t.Fatalf("threshold notice = %+v", threshold)
	}
	if _, ok := panelEventNotice(notices{}, LocaleEnglish, 9, remnawave.EventBandwidthReached, map[string]any{}, ""); ok {
		t.Fatal("a threshold with no percentage has nothing to say")
	}
	if _, ok := panelEventNotice(notices{}, LocaleEnglish, 9, "user.modified", nil, ""); ok {
		t.Fatal("an edit in the panel is not a customer notice")
	}
	named, _ := panelEventNotice(notices{}, LocaleRussian, 1, remnawave.EventFirstConnected, nil, "Работа")
	if !strings.Contains(named.View.Text, "Работа") {
		t.Fatalf("a multi-subscription customer's notice must name the subscription: %q", named.View.Text)
	}
}
