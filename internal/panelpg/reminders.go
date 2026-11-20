package panelpg

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Reminder is one operator-built repeating reminder.
type Reminder struct {
	ID                string    `json:"id,omitempty"`
	Name              string    `json:"name"`
	Active            bool      `json:"active"`
	Category          string    `json:"category"`
	Segment           string    `json:"segment"`
	SegmentDays       int32     `json:"segmentDays"`
	BalanceBelowMinor int64     `json:"balanceBelowMinor"`
	Currency          string    `json:"currency"`
	RepeatEveryDays   int32     `json:"repeatEveryDays"`
	MaxSends          int32     `json:"maxSends"`
	TitleRU           string    `json:"titleRu"`
	BodyRU            string    `json:"bodyRu"`
	TitleEN           string    `json:"titleEn"`
	BodyEN            string    `json:"bodyEn"`
	ButtonRoute       string    `json:"buttonRoute,omitempty"`
	ShowInCabinet     bool      `json:"showInCabinet"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

var (
	reminderSegments = map[string]bool{"no_subscription": true, "expiring": true, "expired": true, "low_balance": true}
	reminderRoutes   = map[string]bool{"": true, "plans": true, "connect": true, "wallet": true, "support": true, "home": true}
	currencyShape    = regexp.MustCompile(`^[A-Z]{3}$`)
)

func reminderFrom(row dbgen.Reminder) Reminder {
	return Reminder{
		ID: uuidString(row.ID), Name: row.Name, Active: row.Active, Category: row.Category,
		Segment: row.Segment, SegmentDays: row.SegmentDays, BalanceBelowMinor: row.BalanceBelowMinor,
		Currency: row.Currency, RepeatEveryDays: row.RepeatEveryDays, MaxSends: row.MaxSends,
		TitleRU: row.TitleRu, BodyRU: row.BodyRu, TitleEN: row.TitleEn, BodyEN: row.BodyEn,
		ButtonRoute: row.ButtonRoute.String, ShowInCabinet: row.ShowInCabinet, UpdatedAt: row.UpdatedAt.Time,
	}
}

// Reminders lists every reminder.
func (service *Service) Reminders(ctx context.Context) ([]Reminder, error) {
	rows, err := service.queries().ListReminders(ctx)
	if err != nil {
		return nil, err
	}
	reminders := make([]Reminder, 0, len(rows))
	for _, row := range rows {
		reminders = append(reminders, reminderFrom(row))
	}
	return reminders, nil
}

// validate normalises a reminder and refuses one the table would.
func (reminder *Reminder) validate() error {
	reminder.Name = strings.TrimSpace(reminder.Name)
	reminder.Currency = strings.ToUpper(strings.TrimSpace(reminder.Currency))
	if reminder.Currency == "" {
		reminder.Currency = "RUB"
	}
	if reminder.Category == "" {
		reminder.Category = "service"
	}
	switch {
	case reminder.Name == "" || utf8.RuneCountInString(reminder.Name) > 80:
		return fmt.Errorf("a name of 1 to 80 characters is required")
	case reminder.Category != "service" && reminder.Category != "marketing":
		return fmt.Errorf("the category is service or marketing")
	case !reminderSegments[reminder.Segment]:
		return fmt.Errorf("the segment is one of no_subscription, expiring, expired, low_balance")
	case reminder.SegmentDays < 0 || reminder.SegmentDays > 365:
		return fmt.Errorf("segment days are 0 to 365")
	case reminder.BalanceBelowMinor < 0 || !currencyShape.MatchString(reminder.Currency):
		return fmt.Errorf("the balance threshold needs a three-letter currency and cannot be negative")
	case reminder.RepeatEveryDays < 1 || reminder.RepeatEveryDays > 365:
		return fmt.Errorf("repeat every 1 to 365 days")
	case reminder.MaxSends < 1 || reminder.MaxSends > 20:
		return fmt.Errorf("send it 1 to 20 times")
	case !reminderRoutes[reminder.ButtonRoute]:
		return fmt.Errorf("the button opens plans, connect, wallet, support, or home")
	}
	for _, text := range []struct {
		value *string
		limit int
	}{{&reminder.TitleRU, 120}, {&reminder.TitleEN, 120}, {&reminder.BodyRU, 2000}, {&reminder.BodyEN, 2000}} {
		*text.value = strings.TrimSpace(*text.value)
		if *text.value == "" || utf8.RuneCountInString(*text.value) > text.limit {
			return fmt.Errorf("titles of 1 to 120 and texts of 1 to 2000 characters are required in both languages")
		}
	}
	return nil
}

// SaveReminder creates or updates a reminder.
func (service *Service) SaveReminder(ctx context.Context, reminder Reminder, actor Actor) (Reminder, error) {
	if err := reminder.validate(); err != nil {
		return Reminder{}, wrapValidation(err)
	}
	var saved Reminder
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.UpsertReminder(ctx, dbgen.UpsertReminderParams{
			ID: optionalUUID(reminder.ID), Name: reminder.Name, Active: reminder.Active,
			Category: reminder.Category, Segment: reminder.Segment, SegmentDays: reminder.SegmentDays,
			BalanceBelowMinor: reminder.BalanceBelowMinor, Currency: reminder.Currency,
			RepeatEveryDays: reminder.RepeatEveryDays, MaxSends: reminder.MaxSends,
			TitleRu: reminder.TitleRU, BodyRu: reminder.BodyRU, TitleEn: reminder.TitleEN, BodyEn: reminder.BodyEN,
			ButtonRoute:   pgtype.Text{String: reminder.ButtonRoute, Valid: reminder.ButtonRoute != ""},
			ShowInCabinet: reminder.ShowInCabinet, UpdatedBy: optionalUUID(actor.AdminID),
		})
		if err != nil {
			return err
		}
		saved = reminderFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"reminder.saved", "marketing", "reminder", saved.ID,
			map[string]any{"segment": saved.Segment, "active": saved.Active},
		))
	})
	return saved, err
}

// DeleteReminder removes a reminder and its per-customer progress.
func (service *Service) DeleteReminder(ctx context.Context, id string, actor Actor) error {
	target := optionalUUID(id)
	if !target.Valid {
		return ErrNotFound
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, err := queries.DeleteReminder(ctx, target)
		if err != nil {
			return err
		}
		if removed == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit("reminder.deleted", "marketing", "reminder", id, nil))
	})
}

// ReminderAudience counts the customers inside a reminder's segment now —
// the preview an operator checks before switching it on.
func (service *Service) ReminderAudience(ctx context.Context, id string) (int64, error) {
	target := optionalUUID(id)
	if !target.Valid {
		return 0, ErrNotFound
	}
	return service.queries().CountReminderAudience(ctx, target)
}
