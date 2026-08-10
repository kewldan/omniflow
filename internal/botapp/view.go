package botapp

import (
	"fmt"
	"html"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/remnawave"
)

type Locale string

const (
	LocaleEnglish Locale = "en"
	LocaleRussian Locale = "ru"
)

type View struct {
	Text       string
	Keyboard   *models.InlineKeyboardMarkup
	Protect    bool
	RetryRoute string
}

func localeFrom(code string) Locale {
	if strings.HasPrefix(strings.ToLower(code), "ru") {
		return LocaleRussian
	}
	return LocaleEnglish
}

func loadingView(locale Locale) View {
	if locale == LocaleRussian {
		return View{Text: "⏳ <b>Обновляем данные…</b>"}
	}
	return View{Text: "⏳ <b>Refreshing your data…</b>"}
}

func homeView(locale Locale, user remnawave.User) View {
	status := statusLabel(locale, user.Status)
	traffic := trafficLine(locale, user.Traffic.UsedBytes, user.TrafficLimitBytes)
	expires := formatDate(user.ExpireAt)
	var text string
	if locale == LocaleRussian {
		text = fmt.Sprintf("🌊 <b>Omniflow</b>\n\n%s\n\n👤 <b>%s</b>\n%s\n📅 До: <b>%s</b>\n\nВыберите действие:", status, html.EscapeString(user.Username), traffic, expires)
	} else {
		text = fmt.Sprintf("🌊 <b>Omniflow</b>\n\n%s\n\n👤 <b>%s</b>\n%s\n📅 Until: <b>%s</b>\n\nChoose an action:", status, html.EscapeString(user.Username), traffic, expires)
	}
	return View{Text: text, Keyboard: mainKeyboard(locale), RetryRoute: routeHome}
}

func subscriptionView(locale Locale, subscription remnawave.Subscription) View {
	user := subscription.User
	status := statusLabel(locale, user.Status)
	var text string
	if locale == LocaleRussian {
		text = fmt.Sprintf("📊 <b>Моя подписка</b>\n\n%s\n⏳ Осталось: <b>%d дн.</b>\n📅 До: <b>%s</b>\n📡 Использовано: <b>%s / %s</b>\n\nНажмите кнопку ниже, чтобы открыть или скопировать подписку.", status, user.DaysLeft, formatDate(user.ExpiresAt), html.EscapeString(user.TrafficUsed), html.EscapeString(user.TrafficLimit))
	} else {
		text = fmt.Sprintf("📊 <b>My subscription</b>\n\n%s\n⏳ Remaining: <b>%d days</b>\n📅 Until: <b>%s</b>\n📡 Used: <b>%s / %s</b>\n\nUse a button below to open or copy your subscription.", status, user.DaysLeft, formatDate(user.ExpiresAt), html.EscapeString(user.TrafficUsed), html.EscapeString(user.TrafficLimit))
	}
	return View{
		Text:       text,
		Keyboard:   subscriptionKeyboard(locale, subscription.SubscriptionURL, false),
		Protect:    true,
		RetryRoute: routeSubscription,
	}
}

func connectView(locale Locale, subscription remnawave.Subscription) View {
	var text string
	if locale == LocaleRussian {
		text = "🚀 <b>Подключение за минуту</b>\n\n1. Нажмите <b>Открыть подписку</b>.\n2. Выберите установленное VPN-приложение.\n3. Подтвердите добавление профиля.\n\nЕсли приложение не открылось, скопируйте ссылку и импортируйте её вручную. Никому не пересылайте ссылку подписки."
	} else {
		text = "🚀 <b>Connect in one minute</b>\n\n1. Tap <b>Open subscription</b>.\n2. Choose your installed VPN app.\n3. Confirm the profile import.\n\nIf no app opens, copy the link and import it manually. Never share your subscription link."
	}
	return View{
		Text:       text,
		Keyboard:   subscriptionKeyboard(locale, subscription.SubscriptionURL, true),
		Protect:    true,
		RetryRoute: routeConnect,
	}
}

func devicesView(locale Locale, devices remnawave.Devices, limit *int) View {
	limitText := "∞"
	if limit != nil {
		limitText = fmt.Sprintf("%d", *limit)
	}
	lines := make([]string, 0, 5)
	for index, device := range devices.Devices {
		if index == 5 {
			break
		}
		name := firstNonEmpty(device.DeviceModel, device.Platform)
		if name == "" {
			if locale == LocaleRussian {
				name = "Неизвестное устройство"
			} else {
				name = "Unknown device"
			}
		}
		lines = append(lines, fmt.Sprintf("• %s · %s", html.EscapeString(name), formatDate(device.UpdatedAt)))
	}
	if len(lines) == 0 {
		if locale == LocaleRussian {
			lines = append(lines, "Пока нет подключённых устройств.")
		} else {
			lines = append(lines, "No connected devices yet.")
		}
	}
	var text string
	if locale == LocaleRussian {
		text = fmt.Sprintf("📱 <b>Устройства</b>\n\nИспользуется: <b>%d / %s</b>\n\n%s\n\nДля безопасности бот не показывает HWID и IP-адреса.", devices.Total, limitText, strings.Join(lines, "\n"))
	} else {
		text = fmt.Sprintf("📱 <b>Devices</b>\n\nIn use: <b>%d / %s</b>\n\n%s\n\nFor your privacy, the bot never displays HWIDs or IP addresses.", devices.Total, limitText, strings.Join(lines, "\n"))
	}
	return View{Text: text, Keyboard: backRefreshKeyboard(locale, routeDevices), RetryRoute: routeDevices}
}

func notLinkedView(locale Locale, supportURL string) View {
	var text string
	if locale == LocaleRussian {
		text = "👋 <b>Добро пожаловать в Omniflow</b>\n\nВаш Telegram пока не связан с подпиской. Обратитесь в поддержку — сотрудник безопасно привяжет аккаунт."
	} else {
		text = "👋 <b>Welcome to Omniflow</b>\n\nYour Telegram account is not linked to a subscription yet. Contact support and an operator will link it securely."
	}
	return View{Text: text, Keyboard: supportKeyboard(locale, supportURL)}
}

func supportView(locale Locale, supportURL string) View {
	if locale == LocaleRussian {
		return View{
			Text:     "💬 <b>Поддержка</b>\n\nОпишите вопрос одним сообщением и приложите скриншот, если он поможет. Никогда не отправляйте токены, пароли или ссылку подписки.",
			Keyboard: supportKeyboard(locale, supportURL),
		}
	}
	return View{
		Text:     "💬 <b>Support</b>\n\nDescribe the issue in one message and attach a screenshot if useful. Never send tokens, passwords, or your subscription link.",
		Keyboard: supportKeyboard(locale, supportURL),
	}
}

func errorView(locale Locale, route string) View {
	if locale == LocaleRussian {
		return View{Text: "⚠️ <b>Не удалось загрузить данные</b>\n\nПроверьте соединение чуть позже или повторите запрос.", Keyboard: retryKeyboard(locale, route)}
	}
	return View{Text: "⚠️ <b>Could not load your data</b>\n\nPlease try again in a moment.", Keyboard: retryKeyboard(locale, route)}
}

func mainKeyboard(locale Locale) *models.InlineKeyboardMarkup {
	if locale == LocaleRussian {
		return keyboard(
			row(callbackButton("📊 Подписка", routeSubscription), callbackButton("🚀 Подключиться", routeConnect)),
			row(callbackButton("📱 Устройства", routeDevices), callbackButton("💬 Поддержка", routeSupport)),
			row(callbackButton("🔄 Обновить", routeHome)),
		)
	}
	return keyboard(
		row(callbackButton("📊 Subscription", routeSubscription), callbackButton("🚀 Connect", routeConnect)),
		row(callbackButton("📱 Devices", routeDevices), callbackButton("💬 Support", routeSupport)),
		row(callbackButton("🔄 Refresh", routeHome)),
	)
}

func subscriptionKeyboard(locale Locale, subscriptionURL string, connect bool) *models.InlineKeyboardMarkup {
	open, copy, back := "🔗 Open subscription", "📋 Copy link", "‹ Back"
	if locale == LocaleRussian {
		open, copy, back = "🔗 Открыть подписку", "📋 Скопировать ссылку", "‹ Назад"
	}
	rows := make([][]models.InlineKeyboardButton, 0, 3)
	if safeURL(subscriptionURL) {
		rows = append(rows, row(models.InlineKeyboardButton{Text: open, URL: subscriptionURL}))
		rows = append(rows, row(models.InlineKeyboardButton{Text: copy, CopyText: &models.CopyTextButton{Text: subscriptionURL}}))
	}
	refreshRoute := routeSubscription
	if connect {
		refreshRoute = routeConnect
	}
	rows = append(rows, row(callbackButton("🔄", refreshRoute), callbackButton(back, routeHome)))
	return keyboard(rows...)
}

func backRefreshKeyboard(locale Locale, route string) *models.InlineKeyboardMarkup {
	back := "‹ Back"
	if locale == LocaleRussian {
		back = "‹ Назад"
	}
	return keyboard(row(callbackButton("🔄", route), callbackButton(back, routeHome)))
}

func supportKeyboard(locale Locale, supportURL string) *models.InlineKeyboardMarkup {
	contact, back := "💬 Contact support", "‹ Back"
	if locale == LocaleRussian {
		contact, back = "💬 Написать в поддержку", "‹ Назад"
	}
	buttons := make([][]models.InlineKeyboardButton, 0, 2)
	if safeURL(supportURL) {
		buttons = append(buttons, row(models.InlineKeyboardButton{Text: contact, URL: supportURL}))
	}
	buttons = append(buttons, row(callbackButton(back, routeHome)))
	return keyboard(buttons...)
}

func retryKeyboard(locale Locale, route string) *models.InlineKeyboardMarkup {
	retry, back := "Try again", "‹ Back"
	if locale == LocaleRussian {
		retry, back = "Повторить", "‹ Назад"
	}
	return keyboard(row(callbackButton("🔄 "+retry, route)), row(callbackButton(back, routeHome)))
}

func statusLabel(locale Locale, status string) string {
	labels := map[string][2]string{
		"ACTIVE":   {"🟢 <b>Active</b>", "🟢 <b>Активна</b>"},
		"DISABLED": {"⚪️ <b>Disabled</b>", "⚪️ <b>Отключена</b>"},
		"LIMITED":  {"🟠 <b>Limited</b>", "🟠 <b>Ограничена</b>"},
		"EXPIRED":  {"🔴 <b>Expired</b>", "🔴 <b>Истекла</b>"},
	}
	label, found := labels[strings.ToUpper(status)]
	if !found {
		label = [2]string{"⚪️ <b>Unknown</b>", "⚪️ <b>Неизвестно</b>"}
	}
	if locale == LocaleRussian {
		return label[1]
	}
	return label[0]
}

func trafficLine(locale Locale, used, limit int64) string {
	if limit <= 0 {
		if locale == LocaleRussian {
			return fmt.Sprintf("📡 Трафик: <b>%s · безлимит</b>", formatBytes(used))
		}
		return fmt.Sprintf("📡 Traffic: <b>%s · unlimited</b>", formatBytes(used))
	}
	ratio := math.Min(1, math.Max(0, float64(used)/float64(limit)))
	filled := int(math.Round(ratio * 10))
	bar := strings.Repeat("■", filled) + strings.Repeat("□", 10-filled)
	return fmt.Sprintf("📡 <b>%s</b>\n%s %d%%", formatBytes(used)+" / "+formatBytes(limit), bar, int(math.Round(ratio*100)))
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor, exponent := int64(unit), 0
	for quotient := value / unit; quotient >= unit && exponent < 4; quotient /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}

func formatDate(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.UTC().Format("02 Jan 2006, 15:04 UTC")
}

func firstNonEmpty(values ...*string) string {
	for _, value := range values {
		if value != nil && strings.TrimSpace(*value) != "" {
			return strings.TrimSpace(*value)
		}
	}
	return ""
}

func safeURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "https" || parsed.Scheme == "http")
}

func keyboard(rows ...[]models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func row(buttons ...models.InlineKeyboardButton) []models.InlineKeyboardButton {
	return buttons
}

func callbackButton(text, route string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: callbackPrefix + route}
}
