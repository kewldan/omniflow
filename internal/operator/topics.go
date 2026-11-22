package operator

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	telegram "github.com/go-telegram/bot"
)

// The operator group's forum topics.
//
// A topic is named in plain words — "Infrastructure", not "🛠 Infrastructure" —
// and carries its emoji as the topic's icon instead. Telegram allows only the
// icons getForumTopicIconStickers lists, so each stream names one of those
// emoji, and the bot looks up its custom emoji ID when it creates or repairs a
// topic. 🛠 and 🧾, which the old names used, are not on Telegram's list.

// topicNames are the topic names in English. The operator supplies only a
// group; the bot owns every topic in it.
var topicNames = map[string]string{
	"purchase":            "Purchases",
	"renewal":             "Renewals",
	"topup":               "Wallet top-ups",
	"refund":              "Refunds",
	"fulfillment_failure": "Fulfillment failures",
	"incident":            "Incidents",
	"backup":              "Backups",
	"security":            "Admin security",
	"support":             "Support tickets",
	"campaign_test":       "Campaign previews",
	"notice_test":         "Notice previews",
	"node":                "Remnawave nodes",
	"monitoring":          "Usage monitoring",
	"anomaly":             "Anomalies",
	"trial":               "Trials",
	"addon":               "Add-ons",
	"promo":               "Promo codes",
	"partner":             "Partners",
	"error":               "Errors",
	"infrastructure":      "Infrastructure",
	"report":              "Reports",
	"marketing":           "Loyalty and referrals",
	"receipt":             "Self-employed receipts",
}

// topicTitlesRU are the topic names in Russian, for a group whose notices
// are in Russian.
var topicTitlesRU = map[string]string{
	"purchase": "Покупки", "renewal": "Продления", "topup": "Пополнения", "refund": "Возвраты",
	"fulfillment_failure": "Сбои выдачи", "incident": "Инциденты", "backup": "Резервные копии",
	"security": "Безопасность", "support": "Поддержка", "campaign_test": "Превью рассылок",
	"notice_test": "Превью уведомлений", "node": "Ноды Remnawave", "monitoring": "Мониторинг",
	"anomaly": "Аномалии", "trial": "Пробные периоды", "addon": "Дополнения", "promo": "Промокоды",
	"partner": "Партнёры", "error": "Ошибки", "infrastructure": "Инфраструктура", "report": "Отчёты",
	"marketing": "Лояльность и рефералы", "receipt": "Чеки самозанятого",
}

// topicEmoji is each stream's icon, every one from Telegram's list of forum
// topic icons.
var topicEmoji = map[string]string{
	"purchase": "🛒", "renewal": "📆", "topup": "💰", "refund": "💸",
	"fulfillment_failure": "❗️", "incident": "‼️", "backup": "📁", "security": "👮‍♂️",
	"support": "💬", "campaign_test": "📣", "notice_test": "📰", "node": "💻",
	"monitoring": "📈", "anomaly": "📉", "trial": "🎟", "addon": "🛍",
	"promo": "🎉", "partner": "💼", "error": "🦠", "infrastructure": "🤖",
	"report": "📝", "marketing": "🏆", "receipt": "🖨",
}

// topicTitle is a topic's plain name in the group's language.
func topicTitle(kind, locale string) string {
	if locale == "ru" {
		if title := topicTitlesRU[kind]; title != "" {
			return title
		}
	}
	if title := topicNames[kind]; title != "" {
		return title
	}
	return humanize(kind)
}

// topicHeading is the topic's icon and name, for a message header that names
// its topic.
func topicHeading(kind, locale string) string {
	if emoji := topicEmoji[kind]; emoji != "" {
		return emoji + " " + topicTitle(kind, locale)
	}
	return topicTitle(kind, locale)
}

// topicIcons caches Telegram's forum icon list: emoji to custom emoji ID.
type topicIcons struct {
	mutex  sync.Mutex
	byChar map[string]string
}

// plainEmoji drops variation selectors, so "❗️" and "❗" are the same icon.
func plainEmoji(emoji string) string {
	return strings.ReplaceAll(emoji, "️", "")
}

// iconFor is the custom emoji ID of a stream's icon, or "" when Telegram's
// list could not be read or does not have it — the topic is then created
// with Telegram's default icon rather than not at all.
func (notifier *Notifier) iconFor(ctx context.Context, kind string) string {
	emoji := topicEmoji[kind]
	if emoji == "" || notifier.client == nil {
		return ""
	}
	icons := &notifier.icons
	icons.mutex.Lock()
	defer icons.mutex.Unlock()
	if icons.byChar == nil {
		stickers, err := notifier.client.GetForumTopicIconStickers(ctx)
		if err != nil {
			notifier.logger.Warn("forum topic icons could not be read", "error", err)
			return ""
		}
		icons.byChar = map[string]string{}
		for _, sticker := range stickers {
			if sticker != nil && sticker.Emoji != "" && sticker.CustomEmojiID != "" {
				icons.byChar[plainEmoji(sticker.Emoji)] = sticker.CustomEmojiID
			}
		}
	}
	return icons.byChar[plainEmoji(emoji)]
}

var retryAfter = regexp.MustCompile(`(?i)retry[ _]after:? (\d+)`)

// rateLimited reports Telegram's "Too Many Requests: retry after N" and how
// long to wait. Creating or editing many topics at once trips it after about
// twenty calls.
func rateLimited(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	var tooMany *telegram.TooManyRequestsError
	if errors.As(err, &tooMany) {
		return time.Duration(tooMany.RetryAfter+1) * time.Second, true
	}
	if match := retryAfter.FindStringSubmatch(err.Error()); match != nil {
		seconds, _ := strconv.Atoi(match[1])
		return time.Duration(seconds+1) * time.Second, true
	}
	return 0, false
}

// waitingForTelegram reports whether a "retry after" is still running.
func (notifier *Notifier) waitingForTelegram() bool {
	return notifier.clock().Before(notifier.telegramBackoff)
}

// backOff records a "retry after".
func (notifier *Notifier) backOff(wait time.Duration) {
	notifier.telegramBackoff = notifier.clock().Add(wait)
}
