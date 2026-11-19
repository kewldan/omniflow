package operator

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/installsettings"
)

func TestReportWindows(t *testing.T) {
	moscow, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skip("no tzdata")
	}
	now := time.Date(2026, 12, 1, 9, 5, 0, 0, moscow)
	start, end := ReportWindow(PeriodDay, now)
	if start.Format("2006-01-02") != "2026-11-30" || end.Format("2006-01-02") != "2026-12-01" {
		t.Fatalf("day = %s–%s", start, end)
	}
	start, _ = ReportWindow(PeriodWeek, now)
	if start.Format("2006-01-02") != "2026-11-24" {
		t.Fatalf("week starts %s", start)
	}
	start, end = ReportWindow(PeriodMonth, now)
	if start.Format("2006-01-02") != "2026-11-01" || end.Format("2006-01-02") != "2026-12-01" {
		t.Fatalf("month = %s–%s", start, end)
	}
	if !reportDue(PeriodMonth, now) || reportDue(PeriodWeek, now) {
		t.Fatal("1 December 2026 is a Tuesday and the first of the month")
	}
}

func TestQuietHoursHoldOnlyNonUrgentKinds(t *testing.T) {
	quiet := installsettings.Document{"quietHoursStart": "23:00", "quietHoursEnd": "07:00"}
	held := HeldKinds(time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC), quiet)
	for _, urgent := range []string{"incident", "security", "error", "backup", "anomaly", "infrastructure", "fulfillment_failure"} {
		if slices.Contains(held, urgent) {
			t.Fatalf("%s must never be held", urgent)
		}
	}
	if !slices.Contains(held, "purchase") || !slices.Contains(held, "report") {
		t.Fatalf("held = %v", held)
	}
	if HeldKinds(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), quiet) != nil {
		t.Fatal("nothing is held outside quiet hours")
	}
}

func TestEffectiveConfigReadsTheOperatorGroupSection(t *testing.T) {
	notifier := New(nil, nil, nil, Config{ChatID: -100, NotificationCap: 30, Window: 5 * time.Minute}).
		WithSettings(installsettings.Static(map[string]installsettings.Document{
			"operator_group": {
				"chatId": "-100200300", "createTopics": false, "notificationCap": float64(0),
				"windowMinutes": float64(10), "disabledKinds": []any{"promo", "trial"},
			},
		}))
	config := notifier.effective(context.Background())
	if config.ChatID != -100200300 || !config.Flat || config.NotificationCap != 0 || config.Window != 10*time.Minute {
		t.Fatalf("config = %+v", config)
	}
	if !slices.Equal(config.Disabled, []string{"promo", "trial"}) {
		t.Fatalf("disabled = %v", config.Disabled)
	}
	plain := New(nil, nil, nil, Config{ChatID: -100}).effective(context.Background())
	if plain.ChatID != -100 || plain.Flat {
		t.Fatalf("without settings the environment applies: %+v", plain)
	}
}

func TestReportRendersAggregatesOnly(t *testing.T) {
	text := renderNotification(dbgen.OperatorNotification{
		Kind:      "report",
		Payload:   []byte(`{"period":"day","from":"2026-11-19","to":"2026-11-19","newCustomers":4,"trials":2,"paidOrders":3,"topups":1,"revenue":{"RUB":149900,"USD":500},"ticketsOpened":1,"ticketsWaiting":0,"activeEntitlements":12,"telegramId":12345}`),
		CreatedAt: pgtype.Timestamptz{Time: time.Unix(0, 0), Valid: true},
	})
	for _, want := range []string{"Daily report", "New customers: <code>4</code>", "1499.00 RUB", "5.00 USD"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "12345") {
		t.Fatal("a field outside the report must not be rendered")
	}
}
