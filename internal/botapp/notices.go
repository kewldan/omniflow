package botapp

import (
	"context"

	"github.com/omniflow/omniflow/internal/notice"
)

// Operator wording for the transactional notices.
//
// The bot renders these from `internal/notice` rather than from its own
// catalogue, so the panel and the customer are looking at one string. An
// installation that has overridden nothing behaves exactly as it did: the
// override table is empty, every lookup falls through to the shipped default,
// and the shipped defaults are the same words that used to live in `messages.go`.
//
// Freshness is one notifier pass. The set is loaded once at the top of a pass
// and used for every message in it, which is both cheaper than a query per
// message and more coherent: a batch of expiry warnings all say the same thing,
// even if somebody saves an edit halfway through. The panel says the change
// takes effect on the next pass rather than immediately, because that is true.

// notices holds the wording in force for one pass.
//
// The zero value is usable and means "no overrides", which is what a lookup
// failure should degrade to: an installation whose override table cannot be
// read must still send its notices, and the shipped wording is the safe thing
// to send.
type notices struct {
	overrides map[string]string
}

// loadNotices reads the operator overrides.
//
// A failure is returned rather than swallowed so the caller can log it, but the
// returned set is still usable — sending the shipped wording is strictly better
// than sending nothing, and a customer whose subscription expires tomorrow does
// not care whose words warned them.
func (store *PostgresStore) loadNotices(ctx context.Context) (notices, error) {
	rows, err := store.pool.Query(ctx, `SELECT code, locale, body FROM notice_overrides`)
	if err != nil {
		return notices{}, err
	}
	defer rows.Close()

	loaded := notices{overrides: map[string]string{}}
	for rows.Next() {
		var code, locale, body string
		if err := rows.Scan(&code, &locale, &body); err != nil {
			return loaded, err
		}
		loaded.overrides[code+"/"+locale] = body
	}
	return loaded, rows.Err()
}

// text renders one notice, preferring the operator's wording.
//
// A stored body has already been checked against the definition when it was
// saved: every placeholder in it is one this notice carries, and the markup is
// a subset Telegram accepts. So there is no second validation here — only the
// substitution.
func (set notices) text(locale Locale, code notice.Code, values map[string]string) string {
	definition, ok := notice.Lookup(string(code))
	if !ok {
		return ""
	}
	language := string(locale)
	body, overridden := set.overrides[string(code)+"/"+language]
	if !overridden {
		body = definition.Default[language]
	}
	return notice.Render(body, values)
}
