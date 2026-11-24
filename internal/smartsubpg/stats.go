package smartsubpg

import (
	"context"
	"sort"
)

// minimumCrowd is how many customers must share a choice before the
// operator sees it: a domain one person routes is theirs, not a trend.
const minimumCrowd = 3

// Count is one value and how many customers chose it.
type Count struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// RuleCount is how customers set one service rule.
type RuleCount struct {
	ID  string `json:"id"`
	On  int    `json:"on"`
	Off int    `json:"off"`
}

// Stats is what customers did with the smart subscription, aggregated.
type Stats struct {
	// Customized is how many subscriptions have choices of their own.
	Customized int `json:"customized"`
	// Hidden and AutoExcluded count subscriptions per country.
	Hidden       []Count `json:"hidden"`
	AutoExcluded []Count `json:"autoExcluded"`
	// Rules counts customers' explicit choices on each service rule.
	Rules []RuleCount `json:"rules"`
	// Pinned counts subscriptions per pinned host.
	Pinned []Count `json:"pinned"`
	// Whitelist is how many subscriptions use the whitelist mode, and
	// ReserveOff how many turned the bypass servers off.
	Whitelist  int `json:"whitelist"`
	ReserveOff int `json:"reserveOff"`
	// Domains are the customers' own domains shared by at least three
	// subscriptions, with the action most of them chose: candidates for a
	// service rule.
	Domains []DomainCount `json:"domains"`
}

// DomainCount is one customer domain and how customers route it.
type DomainCount struct {
	Domain string `json:"domain"`
	Action string `json:"action"`
	Count  int    `json:"count"`
}

// Stats aggregates every subscription's choices.
func (service *Service) Stats(ctx context.Context) (Stats, error) {
	stats := Stats{Hidden: []Count{}, AutoExcluded: []Count{}, Rules: []RuleCount{}, Pinned: []Count{}, Domains: []DomainCount{}}
	err := service.pool.QueryRow(ctx, `SELECT count(*),
			count(*) FILTER (WHERE whitelist_mode), count(*) FILTER (WHERE NOT reserve)
		FROM smart_subscription_preferences`).Scan(&stats.Customized, &stats.Whitelist, &stats.ReserveOff)
	if err != nil {
		return Stats{}, err
	}
	counts := func(query string) ([]Count, error) {
		rows, err := service.pool.Query(ctx, query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []Count{}
		for rows.Next() {
			var count Count
			if err := rows.Scan(&count.Key, &count.Count); err != nil {
				return nil, err
			}
			out = append(out, count)
		}
		return out, rows.Err()
	}
	if stats.Hidden, err = counts(`SELECT country, count(*)::int FROM smart_subscription_preferences,
		unnest(hidden_countries) AS country GROUP BY country ORDER BY 2 DESC, 1`); err != nil {
		return Stats{}, err
	}
	if stats.AutoExcluded, err = counts(`SELECT country, count(*)::int FROM smart_subscription_preferences,
		unnest(auto_excluded) AS country GROUP BY country ORDER BY 2 DESC, 1`); err != nil {
		return Stats{}, err
	}
	if stats.Pinned, err = counts(`SELECT pinned_host::text, count(*)::int FROM smart_subscription_preferences
		WHERE pinned_host IS NOT NULL GROUP BY 1 ORDER BY 2 DESC, 1`); err != nil {
		return Stats{}, err
	}

	rows, err := service.pool.Query(ctx, `SELECT key, count(*) FILTER (WHERE value = 'true')::int,
			count(*) FILTER (WHERE value = 'false')::int
		FROM smart_subscription_preferences, jsonb_each_text(toggles) GROUP BY key ORDER BY key`)
	if err != nil {
		return Stats{}, err
	}
	for rows.Next() {
		var rule RuleCount
		if err := rows.Scan(&rule.ID, &rule.On, &rule.Off); err != nil {
			rows.Close()
			return Stats{}, err
		}
		stats.Rules = append(stats.Rules, rule)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}

	rows, err = service.pool.Query(ctx, `SELECT rule->>'domain', rule->>'action', count(*)::int
		FROM smart_subscription_preferences, jsonb_array_elements(rules) AS rule
		GROUP BY 1, 2 HAVING count(*) >= $1 ORDER BY 3 DESC, 1 LIMIT 50`, minimumCrowd)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	best := map[string]DomainCount{}
	for rows.Next() {
		var domain DomainCount
		if err := rows.Scan(&domain.Domain, &domain.Action, &domain.Count); err != nil {
			return Stats{}, err
		}
		if current, seen := best[domain.Domain]; !seen || domain.Count > current.Count {
			best[domain.Domain] = domain
		}
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}
	for _, domain := range best {
		stats.Domains = append(stats.Domains, domain)
	}
	sort.Slice(stats.Domains, func(i, j int) bool {
		if stats.Domains[i].Count != stats.Domains[j].Count {
			return stats.Domains[i].Count > stats.Domains[j].Count
		}
		return stats.Domains[i].Domain < stats.Domains[j].Domain
	})
	if len(stats.Domains) > 20 {
		stats.Domains = stats.Domains[:20]
	}
	return stats, nil
}
