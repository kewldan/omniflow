package panelpg

import (
	"context"
	"strconv"
	"strings"
)

// MaxCustomerLabels bounds one lookup: a page of any panel table.
const MaxCustomerLabels = 200

// CustomerLabel is how the panel names a customer wherever it lists one: the
// Telegram name and @username the bot last saw, and whether there is a
// Telegram account to fetch a picture for. A customer without a Telegram
// identity has neither, and the panel falls back to the identifier.
type CustomerLabel struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Username   string `json:"username,omitempty"`
	TelegramID *int64 `json:"telegramId,omitempty"`
	// Avatar says a picture can be asked for at /customers/{id}/avatar.
	Avatar bool `json:"avatar"`
}

// CustomerLabels names up to MaxCustomerLabels customers at once. Unknown
// identifiers are left out rather than refused, so one deleted customer does
// not blank a table.
func (service *Service) CustomerLabels(ctx context.Context, ids []string) ([]CustomerLabel, error) {
	valid := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if _, err := parseUUID(id); err != nil || seen[id] {
			continue
		}
		seen[id] = true
		valid = append(valid, id)
	}
	if len(valid) > MaxCustomerLabels {
		return nil, ErrValidaton
	}
	labels := make([]CustomerLabel, 0, len(valid))
	if len(valid) == 0 {
		return labels, nil
	}
	rows, err := service.pool.Query(ctx, `SELECT u.id::text,
			COALESCE(i.metadata->>'firstName', ''), COALESCE(i.metadata->>'lastName', ''),
			COALESCE(i.metadata->>'username', ''), i.provider_subject
		FROM users u
		LEFT JOIN LATERAL (
			SELECT provider_subject, metadata FROM identities
			WHERE user_id = u.id AND provider = 'telegram' AND status = 'active'
			ORDER BY created_at LIMIT 1
		) i ON true
		WHERE u.id = ANY($1::uuid[])`, valid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			label                 CustomerLabel
			first, last, username string
			subject               *string
		)
		if err := rows.Scan(&label.ID, &first, &last, &username, &subject); err != nil {
			return nil, err
		}
		label.Name = strings.TrimSpace(first + " " + last)
		label.Username = username
		if subject != nil {
			if telegramID, parseErr := strconv.ParseInt(*subject, 10, 64); parseErr == nil {
				label.TelegramID = &telegramID
			}
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}
