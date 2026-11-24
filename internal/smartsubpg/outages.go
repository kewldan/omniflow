package smartsubpg

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/smartsub"
)

// OutageNotice is one bot message waiting to go out: a country went down
// ("down") or came back ("up").
type OutageNotice struct {
	OutageID string
	UserID   string
	Kind     string
	Country  string
	// Open is whether the outage is still going on.
	Open bool
}

// countriesDown are the countries every ordinary server of which is down,
// as the panel's node status says.
func countriesDown(catalog smartsub.Catalog, settings smartsub.Settings) map[string]bool {
	total, down := map[string]int{}, map[string]int{}
	for _, host := range catalog.Hosts {
		if host.Disabled || host.Hidden {
			continue
		}
		server := catalog.Classify(host.Remark, host.Address, host.Port, settings)
		if server.Role != smartsub.RoleMain || server.Country == smartsub.UnknownCountry {
			continue
		}
		total[server.Country]++
		if server.Down {
			down[server.Country]++
		}
	}
	out := map[string]bool{}
	for country, count := range total {
		if count > 0 && down[country] == count {
			out[country] = true
		}
	}
	return out
}

// TrackOutages records which countries are down and queues the bot's
// messages: a "down" to every customer with a live subscription who has not
// hidden the country, once the outage has lasted the operator's minutes, and
// an "up" to the same people when it ends. It does nothing while the module
// or its notices are off.
func (service *Service) TrackOutages(ctx context.Context) error {
	settings := service.Settings(ctx)
	if !settings.Enabled || !settings.OutageNotices {
		return nil
	}
	catalog, err := service.FreshCatalog(ctx)
	if err != nil {
		// No node status is not "everything is down".
		return nil
	}
	down := countriesDown(catalog, settings)
	now := service.clock().UTC()

	rows, err := service.pool.Query(ctx, `SELECT id::text, country, started_at, announced_at IS NOT NULL
		FROM smart_subscription_outages WHERE ended_at IS NULL`)
	if err != nil {
		return err
	}
	type open struct {
		id        string
		started   time.Time
		announced bool
	}
	opened := map[string]open{}
	for rows.Next() {
		var entry open
		var country string
		if err := rows.Scan(&entry.id, &country, &entry.started, &entry.announced); err != nil {
			rows.Close()
			return err
		}
		opened[country] = entry
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	countries := make([]string, 0, len(down))
	for country := range down {
		countries = append(countries, country)
	}
	sort.Strings(countries)
	for _, country := range countries {
		entry, isOpen := opened[country]
		if !isOpen {
			if _, err := service.pool.Exec(ctx, `INSERT INTO smart_subscription_outages (country, started_at)
				VALUES ($1, $2) ON CONFLICT DO NOTHING`, country, now); err != nil {
				return err
			}
			continue
		}
		if entry.announced || now.Sub(entry.started) < time.Duration(settings.OutageMinutes)*time.Minute {
			continue
		}
		if err := service.announceOutage(ctx, entry.id, country, now); err != nil {
			return err
		}
	}
	for country, entry := range opened {
		if down[country] {
			continue
		}
		if err := service.endOutage(ctx, entry.id, now); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) announceOutage(ctx context.Context, outageID, country string, now time.Time) error {
	return pgx.BeginFunc(ctx, service.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE smart_subscription_outages SET announced_at = $2
			WHERE id = $1 AND announced_at IS NULL`, outageID, now)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO smart_subscription_outage_notices (outage_id, user_id, kind)
			SELECT DISTINCT $1::uuid, s.user_id, 'down'
			FROM subscriptions s
			LEFT JOIN smart_subscription_preferences p ON p.subscription_id = s.id
			WHERE s.status = 'active' AND s.remnawave_user_id IS NOT NULL
			  AND NOT ($2 = ANY (COALESCE(p.hidden_countries, '{}')))
			  AND EXISTS (SELECT 1 FROM entitlements e
			              WHERE e.subscription_id = s.id AND e.status IN ('active', 'limited')
				              AND e.starts_at <= now() AND e.ends_at > now())
			ON CONFLICT DO NOTHING`, outageID, country)
		return err
	})
}

func (service *Service) endOutage(ctx context.Context, outageID string, now time.Time) error {
	return pgx.BeginFunc(ctx, service.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE smart_subscription_outages SET ended_at = $2 WHERE id = $1 AND ended_at IS NULL`,
			outageID, now); err != nil {
			return err
		}
		// Only the people told about the outage hear that it is over.
		_, err := tx.Exec(ctx, `INSERT INTO smart_subscription_outage_notices (outage_id, user_id, kind)
			SELECT outage_id, user_id, 'up' FROM smart_subscription_outage_notices
			WHERE outage_id = $1 AND kind = 'down' AND sent_at IS NOT NULL
			ON CONFLICT DO NOTHING`, outageID)
		return err
	})
}

// PendingOutageNotices reads the next messages to send, oldest first.
func (service *Service) PendingOutageNotices(ctx context.Context, limit int) ([]OutageNotice, error) {
	rows, err := service.pool.Query(ctx, `SELECT n.outage_id::text, n.user_id::text, n.kind, o.country, o.ended_at IS NULL
		FROM smart_subscription_outage_notices n
		JOIN smart_subscription_outages o ON o.id = n.outage_id
		WHERE n.sent_at IS NULL ORDER BY n.created_at, n.user_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notices []OutageNotice
	for rows.Next() {
		var notice OutageNotice
		if err := rows.Scan(&notice.OutageID, &notice.UserID, &notice.Kind, &notice.Country, &notice.Open); err != nil {
			return nil, err
		}
		notices = append(notices, notice)
	}
	return notices, rows.Err()
}

// MarkOutageNotice records that a message was handled — sent, held back by
// the customer's preferences, or dropped because the outage ended first.
func (service *Service) MarkOutageNotice(ctx context.Context, notice OutageNotice) error {
	var outage, user pgtype.UUID
	if outage.Scan(notice.OutageID) != nil || user.Scan(notice.UserID) != nil {
		return errors.New("malformed outage notice")
	}
	_, err := service.pool.Exec(ctx, `UPDATE smart_subscription_outage_notices SET sent_at = now()
		WHERE outage_id = $1 AND user_id = $2 AND kind = $3`, outage, user, notice.Kind)
	return err
}
