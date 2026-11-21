package app

import "testing"

func TestTelegramMethodHidesToken(t *testing.T) {
	cases := map[string]string{
		"/bot123:ABC-def/sendMessage": "sendMessage",
		"/bot123:ABC/":                "other",
		"/bot123:ABC/send_1":          "other",
	}
	for path, want := range cases {
		if got := TelegramMethod(path); got != want {
			t.Errorf("TelegramMethod(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestTelegramOutcome(t *testing.T) {
	cases := map[int]string{200: "ok", 403: "blocked", 429: "rate_limited", 400: "rejected", 404: "not_found", 502: "telegram_error"}
	for status, want := range cases {
		if got := TelegramOutcome(status); got != want {
			t.Errorf("TelegramOutcome(%d) = %q, want %q", status, got, want)
		}
	}
}
