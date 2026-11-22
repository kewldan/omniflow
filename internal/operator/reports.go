package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/installsettings"
	"github.com/omniflow/omniflow/internal/jobs"
)

// Report periods an operator can schedule in the reports section.
const (
	PeriodDay   = "day"
	PeriodWeek  = "week"
	PeriodMonth = "month"
)

// Reports queues the scheduled summaries, the support SLA breach pings, and
// the infrastructure notices the worker is responsible for. Delivery stays
// with the bot's notifier, like every other operator notice.
type Reports struct {
	pool     *pgxpool.Pool
	settings *installsettings.Reader
	gate     *jobs.Gate
	logger   *slog.Logger
	clock    func() time.Time
}

// NewReports builds the report scheduler.
func NewReports(pool *pgxpool.Pool, settings *installsettings.Reader, logger *slog.Logger) *Reports {
	return &Reports{pool: pool, settings: settings, gate: jobs.NewGate(pool), logger: logger, clock: time.Now}
}

// ReportWindow is the period a report due at now covers, in the given
// location: yesterday for a daily report, the seven days before today for a
// weekly one, the previous calendar month for a monthly one.
func ReportWindow(period string, now time.Time) (time.Time, time.Time) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch period {
	case PeriodWeek:
		return today.AddDate(0, 0, -7), today
	case PeriodMonth:
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		return first.AddDate(0, -1, 0), first
	default:
		return today.AddDate(0, 0, -1), today
	}
}

// reportDue reports whether today is a day a period's report goes out: every
// day, Mondays, or the first of the month.
func reportDue(period string, local time.Time) bool {
	switch period {
	case PeriodWeek:
		return local.Weekday() == time.Monday
	case PeriodMonth:
		return local.Day() == 1
	default:
		return true
	}
}

// RunReports queues every report that is due. It runs on a short tick; the
// gate makes each period's report go out once per slot across replicas.
func (reports *Reports) RunReports(ctx context.Context) error {
	section := reports.settings.Section(ctx, "reports")
	location := reports.settings.Location(ctx)
	now := reports.clock().In(location)
	slot, ok := installsettings.ClockMinutes(section.String("time", "09:00"))
	if !ok {
		slot = 9 * 60
	}
	enabled := section.Strings("periods", nil)
	for _, period := range []string{PeriodDay, PeriodWeek, PeriodMonth} {
		if !slices.Contains(enabled, period) || !reportDue(period, now) {
			continue
		}
		claimed, err := reports.gate.ClaimAt(ctx, "operator_report_"+period, now, slot)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if err = reports.QueueReport(ctx, period, now); err != nil {
			return err
		}
	}
	return nil
}

// QueueReport builds one report for the window ending today and queues it.
func (reports *Reports) QueueReport(ctx context.Context, period string, now time.Time) error {
	start, end := ReportWindow(period, now)
	figures, err := dbgen.New(reports.pool).OperatorReportFigures(ctx, dbgen.OperatorReportFiguresParams{
		WindowStart: pgtype.Timestamptz{Time: start, Valid: true},
		WindowEnd:   pgtype.Timestamptz{Time: end, Valid: true},
	})
	if err != nil {
		return err
	}
	revenue := map[string]int64{}
	_ = json.Unmarshal(figures.Revenue, &revenue)
	payload := map[string]any{
		"period": period, "from": start.Format("2006-01-02"), "to": end.AddDate(0, 0, -1).Format("2006-01-02"),
		"newCustomers": figures.NewCustomers, "trials": figures.Trials,
		"paidOrders": figures.PaidOrders, "topups": figures.Topups, "revenue": revenue,
		"ticketsOpened": figures.TicketsOpened, "ticketsWaiting": figures.TicketsWaiting,
		"activeEntitlements": figures.ActiveEntitlements,
	}
	return Queue(ctx, reports.pool, "report", fmt.Sprintf("report:%s:%s", period, start.Format("2006-01-02")), payload)
}

// RunSupportSLA queues a ping for every ticket whose first response is
// overdue, unless the support section switched SLA pings off.
func (reports *Reports) RunSupportSLA(ctx context.Context) error {
	if !reports.settings.Section(ctx, "support").Bool("notifySlaBreach", true) {
		return nil
	}
	queued, err := dbgen.New(reports.pool).QueueSupportSLABreaches(ctx)
	if err == nil && queued > 0 {
		reports.logger.Info("support SLA breaches announced", "tickets", queued)
	}
	return err
}

// Queue writes one operator notice outside any other transaction. The
// dedupe key collapses a repeat into the first.
func Queue(ctx context.Context, pool *pgxpool.Pool, kind, dedupeKey string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = dbgen.New(pool).EnqueueOperatorNotice(ctx, dbgen.EnqueueOperatorNoticeParams{
		Kind: kind, DedupeKey: dedupeKey, Payload: encoded,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// AnnounceStart queues the "bot started" infrastructure notice with the
// running version. One per process start: the key carries the minute.
func AnnounceStart(ctx context.Context, pool *pgxpool.Pool, process, version string, now time.Time) error {
	return Queue(ctx, pool, "infrastructure",
		fmt.Sprintf("started:%s:%s:%d", process, version, now.Unix()/60),
		map[string]any{"event": process + "_started", "process": process, "version": version})
}

// AnnounceRelease queues the "a newer version exists" notice once per
// release, so an operator hears about it without opening the diagnostics.
func AnnounceRelease(ctx context.Context, pool *pgxpool.Pool, current, latest string) error {
	return Queue(ctx, pool, "infrastructure", "release:"+latest,
		map[string]any{"event": "update_available", "version": latest, "previousVersion": current})
}

// renderReport formats a scheduled report: label and value lines under the
// period, in the group's language. The payload is the report's own, built from
// aggregate counts, so nothing in it identifies a customer.
func renderReport(notification dbgen.OperatorNotification, payload map[string]any, config Config) string {
	input := renderInput{Payload: payload, At: notification.CreatedAt.Time, Locale: config.Locale, Location: config.Location}
	n := &notice{input: input}
	titles := map[string]phrase{
		PeriodDay:   {"📊 Daily report", "📊 Отчёт за день"},
		PeriodWeek:  {"📊 Weekly report", "📊 Отчёт за неделю"},
		PeriodMonth: {"📊 Monthly report", "📊 Отчёт за месяц"},
	}
	period, _ := payload["period"].(string)
	title, ok := titles[period]
	if !ok {
		title = titles[PeriodDay]
	}
	n.title = input.say(title)
	from, to := input.str("from"), input.str("to")
	if from == to {
		n.subtitle = html.EscapeString(from)
	} else {
		n.subtitle = html.EscapeString(from) + " — " + html.EscapeString(to)
	}
	count := func(key string) string {
		value, _ := input.num(key)
		return "<b>" + groupDigits(value) + "</b>"
	}
	n.field("👥", phrase{"New customers", "Новых клиентов"}, count("newCustomers"))
	n.field("🎁", phrase{"Trials", "Пробных периодов"}, count("trials"))
	n.field("💳", phrase{"Paid orders", "Оплаченных заказов"}, count("paidOrders"))
	n.field("👛", phrase{"Top-ups", "Пополнений"}, count("topups"))
	n.field("💰", phrase{"Revenue", "Выручка"}, "<b>"+moneyMap(payload["revenue"])+"</b>")
	n.field("💬", phrase{"Tickets opened", "Обращений открыто"}, count("ticketsOpened"))
	n.field("⏳", phrase{"Tickets waiting now", "Ждут ответа сейчас"}, count("ticketsWaiting"))
	n.field("📶", phrase{"Active subscriptions now", "Активных подписок сейчас"}, count("activeEntitlements"))
	text, _ := n.finish()
	return text
}
