// Package termsgate decides whether a customer has to accept the terms before
// using the service, and records their acceptance.
//
// Bedolaga can require every user to accept the offer and rules before the
// menu opens; Omniflow only said "by continuing you accept". When the
// Marketing settings turn `requireTerms` on, the bot and the web panel stop at
// a gate until the customer accepts the current `termsVersion`, and the
// acceptance is a consent_records row — purpose `terms`, the version, the
// surface, and the request — like every other consent. Raising the version
// asks everybody again.
//
// The gate shows the documents the customer agrees to: the published pages of
// kind terms, offer, and privacy. When a privacy policy is among them, the one
// acceptance is recorded as two consents, `terms` and `privacy`, because
// agreeing to the processing of personal data is a consent of its own; a
// customer who accepted the terms before the policy was published is asked
// again.
package termsgate

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultVersion is the policy version when the operator set none.
const DefaultVersion = "1"

// Gate reads the setting and the customer's consents.
type Gate struct {
	pool *pgxpool.Pool
	// fallbackURL is APP_TERMS_URL, used when branding saves no terms page.
	fallbackURL string
}

// WithTermsURL sets the terms page used when the branding settings name none.
func (gate *Gate) WithTermsURL(url string) *Gate {
	gate.fallbackURL = strings.TrimSpace(url)
	return gate
}

// New builds a gate. A nil pool yields a gate that never requires anything.
func New(pool *pgxpool.Pool) *Gate {
	return &Gate{pool: pool}
}

// Document is one page the customer agrees to by accepting.
type Document struct {
	// Kind is terms, offer, or privacy.
	Kind  string
	Slug  string
	Title string
}

// documentKinds are the page kinds the gate shows, in the order it shows them.
var documentKinds = []string{"terms", "offer", "privacy"}

// Policy is what the operator requires.
type Policy struct {
	Required bool
	Version  string
	// TermsURL is the page the customer is asked to read.
	TermsURL string
}

// Policy reads the Marketing settings. An unreadable section requires
// nothing: a settings outage must not lock every customer out.
func (gate *Gate) Policy(ctx context.Context) Policy {
	if gate == nil || gate.pool == nil {
		return Policy{Version: DefaultVersion}
	}
	var required bool
	var version, url string
	err := gate.pool.QueryRow(ctx, `SELECT COALESCE((m.document->>'requireTerms')::boolean, false),
		COALESCE(NULLIF(btrim(m.document->>'termsVersion'), ''), $1),
		COALESCE(NULLIF(btrim((SELECT b.document->>'termsUrl' FROM installation_settings b WHERE b.section = 'branding')), ''), $2)
		FROM installation_settings m WHERE m.section = 'marketing'`, DefaultVersion, gate.fallbackURL).Scan(&required, &version, &url)
	if err != nil {
		return Policy{Version: DefaultVersion, TermsURL: gate.fallbackURL}
	}
	return Policy{Required: required, Version: version, TermsURL: url}
}

// Documents lists the published terms, offer, and privacy policy in the
// reader's language, falling back to the other one: one page of each kind,
// the first by the operator's order. A page need not be listed in the menu to
// be shown here; publishing it is what puts it at its address.
func (gate *Gate) Documents(ctx context.Context, locale string) ([]Document, error) {
	if gate == nil || gate.pool == nil {
		return nil, nil
	}
	rows, err := gate.pool.Query(ctx, `SELECT DISTINCT ON (p.kind) p.kind, p.slug, l.title
		FROM info_pages p
		JOIN info_page_localizations l ON l.page_slug = p.slug
		WHERE p.published_at IS NOT NULL AND p.kind = ANY($2::text[])
		ORDER BY p.kind, p.sort_order, p.slug, (l.locale = $1) DESC, l.locale`, locale, documentKinds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKind := map[string]Document{}
	for rows.Next() {
		var document Document
		if err := rows.Scan(&document.Kind, &document.Slug, &document.Title); err != nil {
			return nil, err
		}
		byKind[document.Kind] = document
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	documents := make([]Document, 0, len(byKind))
	for _, kind := range documentKinds {
		if document, ok := byKind[kind]; ok {
			documents = append(documents, document)
		}
	}
	return documents, nil
}

// privacyPublished is the condition under which an acceptance needs, and
// records, a privacy consent as well.
const privacyPublished = `EXISTS (SELECT 1 FROM info_pages WHERE kind = 'privacy' AND published_at IS NOT NULL)`

// Accepted reports whether the customer's latest terms decision accepts this
// version, and, while a privacy policy is published, their latest privacy
// decision too.
func (gate *Gate) Accepted(ctx context.Context, customerID, version string) (bool, error) {
	if gate == nil || gate.pool == nil {
		return true, nil
	}
	var accepted bool
	err := gate.pool.QueryRow(ctx, `SELECT COALESCE((SELECT granted AND policy_version = $2 FROM consent_records
			WHERE user_id = $1::uuid AND purpose = 'terms' ORDER BY occurred_at DESC, id DESC LIMIT 1), false)
		AND (NOT `+privacyPublished+` OR COALESCE((SELECT granted AND policy_version = $2 FROM consent_records
			WHERE user_id = $1::uuid AND purpose = 'privacy' ORDER BY occurred_at DESC, id DESC LIMIT 1), false))`,
		customerID, version).Scan(&accepted)
	return accepted, err
}

// Blocked reports whether the customer has to accept the terms first, and the
// version they would accept.
func (gate *Gate) Blocked(ctx context.Context, customerID string) (bool, string, error) {
	policy := gate.Policy(ctx)
	if !policy.Required {
		return false, policy.Version, nil
	}
	accepted, err := gate.Accepted(ctx, customerID, policy.Version)
	return !accepted, policy.Version, err
}

// Accept records the customer's acceptance of a version from a surface: a
// terms consent, and a privacy consent while a privacy policy is published,
// in one statement so neither is written without the other.
func (gate *Gate) Accept(ctx context.Context, customerID, version, source, requestID string) error {
	if gate == nil || gate.pool == nil {
		return nil
	}
	_, err := gate.pool.Exec(ctx, `INSERT INTO consent_records (user_id, purpose, granted, policy_version, source, request_id)
		SELECT $1::uuid, purpose, true, $2, $3, NULLIF($4, '')
		FROM (VALUES ('terms'), ('privacy')) AS purposes (purpose)
		WHERE purpose = 'terms' OR `+privacyPublished,
		customerID, strings.TrimSpace(version), source, requestID)
	return err
}
