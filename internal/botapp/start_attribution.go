package botapp

import (
	"context"
	"regexp"
	"strings"
)

// startTags are advertising tags read from a /start link.
type startTags struct {
	Source, Medium, Campaign string
	ClickID, ClickSource     string
}

var (
	// startTagPart is one UTM value: Telegram allows only letters, digits,
	// `_`, and `-` in a start parameter, and `__` separates the values.
	startTagPart = regexp.MustCompile(`^[A-Za-z0-9-]+(_[A-Za-z0-9-]+)*$`)
	startClickID = regexp.MustCompile(`^[A-Za-z0-9_-]{6,60}$`)
)

// parseStartTags reads `utm_<source>[__<medium>[__<campaign>]]`,
// `yclid_<id>`, or `gclid_<id>`, and reports false for anything else.
func parseStartTags(argument string) (startTags, bool) {
	switch {
	case strings.HasPrefix(argument, "yclid_"):
		id := strings.TrimPrefix(argument, "yclid_")
		return startTags{ClickID: id, ClickSource: "yandex"}, startClickID.MatchString(id)
	case strings.HasPrefix(argument, "gclid_"):
		id := strings.TrimPrefix(argument, "gclid_")
		return startTags{ClickID: id, ClickSource: "google"}, startClickID.MatchString(id)
	case strings.HasPrefix(argument, "utm_"):
		parts := strings.Split(strings.TrimPrefix(argument, "utm_"), "__")
		if len(parts) > 3 {
			return startTags{}, false
		}
		for _, part := range parts {
			if !startTagPart.MatchString(part) {
				return startTags{}, false
			}
		}
		tags := startTags{Source: strings.ToLower(parts[0])}
		if len(parts) > 1 {
			tags.Medium = strings.ToLower(parts[1])
		}
		if len(parts) > 2 {
			tags.Campaign = strings.ToLower(parts[2])
		}
		return tags, true
	default:
		return startTags{}, false
	}
}

// RecordStartTags keeps the tags a customer first arrived with. Later links
// do not overwrite them: the advertisement that brought the customer is the
// one a conversion is credited to.
func (store *PostgresStore) RecordStartTags(ctx context.Context, customerID string, tags startTags) error {
	_, err := store.pool.Exec(ctx, `INSERT INTO customer_start_attributions
		(user_id, click_id, click_source, utm_source, utm_medium, utm_campaign)
		VALUES ($1::uuid, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''))
		ON CONFLICT (user_id) DO NOTHING`,
		customerID, tags.ClickID, tags.ClickSource, tags.Source, tags.Medium, tags.Campaign)
	return err
}

// recordStartTags attributes the customer behind a /start link to its tags.
func (app *App) recordStartTags(ctx context.Context, telegramID int64, telegramLocale, argument string) {
	tags, ok := parseStartTags(argument)
	if !ok || app.customers == nil {
		return
	}
	session, err := app.commerceContext(ctx, telegramID, telegramLocale)
	if err != nil {
		app.logger.Warn("customer resolution failed during start attribution", "error", err)
		return
	}
	if err := app.customers.RecordStartTags(ctx, session.Customer.ID, tags); err != nil {
		app.logger.Warn("start attribution failed", "error", err)
	}
}
