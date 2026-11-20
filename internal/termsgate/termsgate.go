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
package termsgate

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
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

// Accepted reports whether the customer's latest terms decision accepts this
// version.
func (gate *Gate) Accepted(ctx context.Context, customerID, version string) (bool, error) {
	if gate == nil || gate.pool == nil {
		return true, nil
	}
	var granted bool
	err := gate.pool.QueryRow(ctx, `SELECT granted AND policy_version = $2 FROM consent_records
		WHERE user_id = $1::uuid AND purpose = 'terms' ORDER BY occurred_at DESC, id DESC LIMIT 1`,
		customerID, version).Scan(&granted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return granted, err
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

// Accept records the customer's acceptance of a version from a surface.
func (gate *Gate) Accept(ctx context.Context, customerID, version, source, requestID string) error {
	if gate == nil || gate.pool == nil {
		return nil
	}
	_, err := gate.pool.Exec(ctx, `INSERT INTO consent_records (user_id, purpose, granted, policy_version, source, request_id)
		VALUES ($1::uuid, 'terms', true, $2, $3, NULLIF($4, ''))`,
		customerID, strings.TrimSpace(version), source, requestID)
	return err
}
