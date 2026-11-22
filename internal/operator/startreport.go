package operator

import (
	"context"
	"html"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The startup report.
//
// When the bot starts it posts one summary to the operator group, the way the
// previous bot did: what needs attention, the customers, the subscriptions,
// the money, and whether the Remnawave panel answers. It is figures only —
// counts and sums, nothing that names a customer — read once at start. It is
// one status message in the infrastructure topic, edited on every start rather
// than posted again; see status.go.

// StartReport is what the startup summary says.
type StartReport struct {
	Name    string
	Version string

	UsersTotal int64
	UsersNew   int64

	PaidSubscriptions  int64
	TrialSubscriptions int64

	// TopupsDay and Balances are minor units by currency.
	TopupsDay map[string]int64
	Balances  map[string]int64

	// AwaitingReply counts the open tickets waiting on the desk; OpenTickets
	// adds the ones waiting on the customer.
	AwaitingReply    int64
	OpenTickets      int64
	FailedDeliveries int64
	Maintenance      bool

	// RemnawaveChecked is false when no probe ran.
	RemnawaveChecked   bool
	RemnawaveReachable bool
	RemnawaveLatency   time.Duration

	// SalesMode is the access section's mode: open, invite, or closed.
	SalesMode string
}

// Probe measures one call to the Remnawave panel.
type Probe func(ctx context.Context) error

// CollectStartReport reads the figures. A figure that cannot be read is left
// at zero rather than holding the report back: the report is a glance, and
// the panel has every number exactly.
func CollectStartReport(ctx context.Context, pool *pgxpool.Pool, probe Probe) StartReport {
	report := StartReport{TopupsDay: map[string]int64{}, Balances: map[string]int64{}}
	if pool != nil {
		_ = pool.QueryRow(ctx, `SELECT
				count(*) FILTER (WHERE status IN ('active', 'suspended')),
				count(*) FILTER (WHERE status IN ('active', 'suspended') AND created_at > now() - interval '24 hours')
			FROM users`).Scan(&report.UsersTotal, &report.UsersNew)
		_ = pool.QueryRow(ctx, `SELECT
				count(*) FILTER (WHERE p.kind NOT IN ('trial', 'free')),
				count(*) FILTER (WHERE p.kind = 'trial')
			FROM entitlements e
			JOIN plan_versions v ON v.id = e.plan_version_id
			JOIN plans p ON p.id = v.plan_id
			WHERE e.status IN ('active', 'limited') AND e.ends_at > now()`).
			Scan(&report.PaidSubscriptions, &report.TrialSubscriptions)
		sums(ctx, pool, report.TopupsDay, `SELECT currency, COALESCE(sum(paid_minor), 0)::bigint FROM orders
			WHERE operation = 'topup' AND paid_at > now() - interval '24 hours' GROUP BY currency`)
		sums(ctx, pool, report.Balances, `SELECT currency, COALESCE(sum(amount_minor), 0)::bigint FROM ledger_entries
			WHERE account_type = 'customer_wallet' GROUP BY currency`)
		_ = pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'open'),
				count(*) FILTER (WHERE status IN ('open', 'pending'))
			FROM support_tickets`).Scan(&report.AwaitingReply, &report.OpenTickets)
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment_operations
			WHERE status = 'failed' AND updated_at > now() - interval '7 days'`).Scan(&report.FailedDeliveries)
		_ = pool.QueryRow(ctx, `SELECT COALESCE(bool_or(active), false) FROM maintenance_state`).Scan(&report.Maintenance)
	}
	if probe != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		started := time.Now()
		err := probe(probeCtx)
		cancel()
		report.RemnawaveChecked = true
		report.RemnawaveReachable = err == nil
		report.RemnawaveLatency = time.Since(started)
	}
	return report
}

func sums(ctx context.Context, pool *pgxpool.Pool, into map[string]int64, query string) {
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var (
			currency string
			minor    int64
		)
		if rows.Scan(&currency, &minor) == nil && minor != 0 {
			into[currency] = minor
		}
	}
}

// payload is the report as a notice payload. The keys stay clear of the
// redaction patterns (no "pan…", "card", "token").
func (report StartReport) payload() map[string]any {
	return map[string]any{
		"event": "bot_started", "process": "bot", "version": report.Version,
		"report": map[string]any{
			"name": report.Name, "usersTotal": report.UsersTotal, "usersNew": report.UsersNew,
			"paidSubscriptions": report.PaidSubscriptions, "trialSubscriptions": report.TrialSubscriptions,
			"topupsDay": report.TopupsDay, "balances": report.Balances,
			"awaitingReply": report.AwaitingReply, "openTickets": report.OpenTickets,
			"failedDeliveries": report.FailedDeliveries, "maintenance": report.Maintenance,
			"remnawaveChecked": report.RemnawaveChecked, "remnawaveReachable": report.RemnawaveReachable,
			"remnawaveMs": report.RemnawaveLatency.Milliseconds(), "salesMode": report.SalesMode,
		},
	}
}

var salesModes = map[string]phrase{
	"open":   {"open to everyone", "открыты для всех"},
	"invite": {"by invitation", "по приглашению"},
	"closed": {"closed", "закрыты"},
}

// renderStartReport writes the startup report: plain label and value lines
// under a few bold headings, no table.
func (n *notice) renderStartReport() {
	report, _ := n.input.Payload["report"].(map[string]any)
	value := func(key string) int64 {
		number, _ := report[key].(float64)
		return int64(number)
	}
	flag := func(key string) bool {
		yes, _ := report[key].(bool)
		return yes
	}
	name, _ := report["name"].(string)
	title := "🚀 " + html.EscapeString(firstNonEmpty(name, "Omniflow"))
	if version := n.input.str("version"); version != "" {
		if !strings.HasPrefix(version, "v") {
			version = "v" + version
		}
		title += " · " + html.EscapeString(version)
	}
	n.title, n.keepCase = title, true
	n.subtitle = n.input.say(phrase{"Started and ready to work", "Запущен и готов к работе"})
	n.stamp = phrase{"Started", "Запущен"}

	attention := []string{}
	if count := value("awaitingReply"); count > 0 {
		attention = append(attention, n.input.say(phrase{"Tickets waiting for a reply", "Тикетов ждут ответа"})+": <b>"+groupDigits(count)+"</b>")
	}
	if flag("remnawaveChecked") && !flag("remnawaveReachable") {
		attention = append(attention, n.input.say(phrase{"The Remnawave panel does not answer", "Панель Remnawave не отвечает"}))
	}
	if count := value("failedDeliveries"); count > 0 {
		attention = append(attention, n.input.say(phrase{"Failed deliveries this week", "Сбоев выдачи за неделю"})+": <b>"+groupDigits(count)+"</b>")
	}
	if flag("maintenance") {
		attention = append(attention, n.input.say(phrase{"Maintenance mode is on", "Включён режим обслуживания"}))
	}
	if len(attention) > 0 {
		n.lines = append(n.lines, "⚠️ <b>"+n.input.say(phrase{"Needs attention", "Требует внимания"})+"</b>")
		for _, item := range attention {
			n.lines = append(n.lines, "• "+item)
		}
	} else {
		n.lines = append(n.lines, "✅ <b>"+n.input.say(phrase{"Nothing needs attention", "Всё в порядке"})+"</b>")
	}

	section := func(heading string) { n.lines = append(n.lines, "", heading) }
	pair := func(label phrase, text string) {
		n.lines = append(n.lines, n.input.say(label)+": <b>"+text+"</b>")
	}
	section("👥 <b>" + n.input.say(phrase{"Customers", "Пользователи"}) + "</b>")
	pair(phrase{"Total", "Всего"}, groupDigits(value("usersTotal")))
	pair(phrase{"New in 24 hours", "Новых за сутки"}, "+"+groupDigits(value("usersNew")))

	section("💳 <b>" + n.input.say(phrase{"Active subscriptions", "Активные подписки"}) + "</b>")
	pair(phrase{"Paid", "Платных"}, groupDigits(value("paidSubscriptions")))
	pair(phrase{"Trials", "Пробных"}, groupDigits(value("trialSubscriptions")))

	section("💰 <b>" + n.input.say(phrase{"Money", "Деньги"}) + "</b>")
	pair(phrase{"Top-ups in 24 hours", "Пополнения за сутки"}, moneyMap(report["topupsDay"]))
	pair(phrase{"On balances", "На балансах"}, moneyMap(report["balances"]))

	section("🛠 <b>" + n.input.say(phrase{"System", "Система"}) + "</b>")
	if flag("remnawaveChecked") {
		panel := "🔴 " + n.input.say(phrase{"no answer", "не отвечает"})
		if flag("remnawaveReachable") {
			panel = "🟢 " + n.input.say(phrase{"online", "на связи"}) + " · " + groupDigits(value("remnawaveMs")) + " " + n.input.say(phrase{"ms", "мс"})
		}
		n.lines = append(n.lines, "Remnawave: "+panel)
	}
	pair(phrase{"Open tickets", "Тикетов открыто"}, groupDigits(value("openTickets")))
	mode, _ := report["salesMode"].(string)
	if text, ok := salesModes[mode]; ok {
		n.lines = append(n.lines, n.input.say(phrase{"Sales", "Продажи"})+": "+n.input.say(text))
	}
	n.lines = append(n.lines, "")
	n.button(n.input.say(phrase{"📊 Panel", "📊 Панель"}), "/admin")
	if value("openTickets") > 0 {
		n.button(n.input.say(phrase{"💬 Tickets", "💬 Тикеты"}), "/admin/support")
	}
}

// moneyMap renders "{RUB: 151800}" as "1 518 ₽", several currencies joined.
func moneyMap(raw any) string {
	amounts, _ := raw.(map[string]any)
	if len(amounts) == 0 {
		return "0"
	}
	currencies := make([]string, 0, len(amounts))
	for currency := range amounts {
		currencies = append(currencies, currency)
	}
	slices.Sort(currencies)
	parts := make([]string, 0, len(currencies))
	for _, currency := range currencies {
		minor, _ := amounts[currency].(float64)
		parts = append(parts, formatMoney(int64(minor), currency))
	}
	return strings.Join(parts, " · ")
}
