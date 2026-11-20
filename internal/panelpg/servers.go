package panelpg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Server is one entry of the server catalogue: how Omniflow labels and
// limits the sale of one Remnawave internal squad. Remnawave stays
// authoritative for the squad itself (decision 0004). LiveCustomers is
// Omniflow's own count of customers with a live entitlement on the squad;
// the panel's member count is read live by the route and never stored.
type Server struct {
	SquadID       string           `json:"squadId"`
	NameRU        string           `json:"nameRu"`
	NameEN        string           `json:"nameEn"`
	CountryCode   string           `json:"countryCode"`
	MaxUsers      *int32           `json:"maxUsers"`
	TrialEligible bool             `json:"trialEligible"`
	SortOrder     int32            `json:"sortOrder"`
	Prices        map[string]int64 `json:"prices"`
	LiveCustomers int32            `json:"liveCustomers"`
}

// ErrServerNotFound reports a squad with no catalogue entry.
var ErrServerNotFound = fmt.Errorf("server: %w", ErrNotFound)

// Servers lists the catalogue with each squad's live customer count.
func (service *Service) Servers(ctx context.Context) ([]Server, error) {
	servers := []Server{}
	err := service.inTxRaw(ctx, func(tx pgx.Tx, _ *dbgen.Queries) error {
		rows, err := tx.Query(ctx, `SELECT c.squad_id::text, c.name_ru, c.name_en, COALESCE(c.country_code, ''),
				c.max_users, c.trial_eligible, c.sort_order,
				(SELECT count(DISTINCT e.user_id)::integer FROM entitlements e
				 WHERE e.remnawave_squad_ids @> ARRAY[c.squad_id]
				   AND e.status IN ('pending', 'active', 'limited', 'paused')
				   AND (e.status = 'paused' OR e.ends_at > now()))
			FROM server_catalog c ORDER BY c.sort_order, c.name_en`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				server Server
				limit  pgtype.Int4
			)
			if err = rows.Scan(&server.SquadID, &server.NameRU, &server.NameEN, &server.CountryCode, &limit,
				&server.TrialEligible, &server.SortOrder, &server.LiveCustomers); err != nil {
				return err
			}
			if limit.Valid {
				value := limit.Int32
				server.MaxUsers = &value
			}
			server.Prices = map[string]int64{}
			servers = append(servers, server)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		prices, err := tx.Query(ctx, `SELECT squad_id::text, currency, amount_minor FROM server_catalog_prices`)
		if err != nil {
			return err
		}
		defer prices.Close()
		index := make(map[string]int, len(servers))
		for position, server := range servers {
			index[server.SquadID] = position
		}
		for prices.Next() {
			var (
				squad, currency string
				amount          int64
			)
			if err = prices.Scan(&squad, &currency, &amount); err != nil {
				return err
			}
			if position, ok := index[squad]; ok {
				servers[position].Prices[currency] = amount
			}
		}
		return prices.Err()
	})
	return servers, err
}

// SaveServer creates or replaces one squad's catalogue entry.
func (service *Service) SaveServer(ctx context.Context, server Server, actor Actor) (Server, error) {
	squad, err := parseUUID(strings.TrimSpace(server.SquadID))
	if err != nil {
		return Server{}, ErrValidaton
	}
	server.NameRU, server.NameEN = strings.TrimSpace(server.NameRU), strings.TrimSpace(server.NameEN)
	server.CountryCode = strings.ToUpper(strings.TrimSpace(server.CountryCode))
	if server.NameRU == "" || server.NameEN == "" || len([]rune(server.NameRU)) > 80 || len([]rune(server.NameEN)) > 80 ||
		server.CountryCode != "" && (len(server.CountryCode) != 2 || strings.Trim(server.CountryCode, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "") ||
		server.MaxUsers != nil && *server.MaxUsers <= 0 {
		return Server{}, ErrValidaton
	}
	for currency, amount := range server.Prices {
		if !currencyShape(currency) || amount <= 0 {
			return Server{}, ErrValidaton
		}
	}
	operator, _ := parseUUID(actor.AdminID)
	limit := pgtype.Int4{}
	if server.MaxUsers != nil {
		limit = pgtype.Int4{Int32: *server.MaxUsers, Valid: true}
	}
	err = service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		if _, txErr := tx.Exec(ctx, `INSERT INTO server_catalog (squad_id, name_ru, name_en, country_code, max_users,
				trial_eligible, sort_order, updated_by)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8)
			ON CONFLICT (squad_id) DO UPDATE SET name_ru = EXCLUDED.name_ru, name_en = EXCLUDED.name_en,
				country_code = EXCLUDED.country_code, max_users = EXCLUDED.max_users,
				trial_eligible = EXCLUDED.trial_eligible, sort_order = EXCLUDED.sort_order,
				updated_at = now(), updated_by = EXCLUDED.updated_by`,
			squad, server.NameRU, server.NameEN, server.CountryCode, limit, server.TrialEligible, server.SortOrder, operator); txErr != nil {
			return txErr
		}
		if _, txErr := tx.Exec(ctx, `DELETE FROM server_catalog_prices WHERE squad_id = $1`, squad); txErr != nil {
			return txErr
		}
		for currency, amount := range server.Prices {
			if _, txErr := tx.Exec(ctx, `INSERT INTO server_catalog_prices (squad_id, currency, amount_minor)
				VALUES ($1, $2, $3)`, squad, strings.ToUpper(currency), amount); txErr != nil {
				return txErr
			}
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.server.saved", "configuration", "server", server.SquadID,
			map[string]any{
				"countryCode": server.CountryCode, "maxUsers": server.MaxUsers,
				"trialEligible": server.TrialEligible, "currencies": currencyList(server.Prices),
			},
		))
	})
	if err != nil {
		return Server{}, err
	}
	if server.Prices == nil {
		server.Prices = map[string]int64{}
	}
	return server, nil
}

// DeleteServer removes a squad's catalogue entry; the squad is then sold
// with no label, limit, or price of its own, exactly as before it had one.
func (service *Service) DeleteServer(ctx context.Context, squadID string, actor Actor) error {
	squad, err := parseUUID(squadID)
	if err != nil {
		return ErrServerNotFound
	}
	return service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		tag, txErr := tx.Exec(ctx, `DELETE FROM server_catalog WHERE squad_id = $1`, squad)
		if txErr != nil {
			return txErr
		}
		if tag.RowsAffected() == 0 {
			return ErrServerNotFound
		}
		return appendAudit(ctx, queries, actor.audit("panel.server.deleted", "configuration", "server", squadID, nil))
	})
}
