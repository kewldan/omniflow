package adminauth

import (
	"errors"
	"net/url"
	"strings"
)

// The passkey rules that are decisions rather than protocol.
//
// The WebAuthn exchange itself is handled by `github.com/go-webauthn/webauthn`,
// which knows the format of an attestation and how to verify a signature.
// What it cannot know is what this product wants from a passkey, and those
// choices live here so they are stated once and testable without a browser.
//
// The choice that shapes the rest: a passkey signs in on its own. The
// authenticator proves possession of a private key and verifies the person
// holding it, so an assertion carries both factors and issues a complete
// session rather than a pending one. That is only true when the authenticator
// actually performed user verification, which is why an assertion that did not
// is refused rather than downgraded to a first factor.

var (
	// ErrPasskeyUnverified reports an assertion the authenticator did not
	// verify a person for. Accepting it would make a passkey a possession
	// factor alone, and this product signs in with one.
	ErrPasskeyUnverified = errors.New("the authenticator did not verify the person holding it")
	// ErrPasskeyCloned reports a signature counter that went backwards, which
	// means two authenticators are answering for one credential.
	ErrPasskeyCloned = errors.New("passkey signature counter went backwards")
	// ErrPasskeyOriginUnknown reports a relying-party configuration that cannot
	// be derived from the panel's public URL.
	ErrPasskeyOriginUnknown = errors.New("passkey support needs an absolute public URL")
)

// PasskeyLabelLimit bounds what an operator may call a key. It matches the
// column's own check, so a label refused by the database is refused before it
// gets there.
const PasskeyLabelLimit = 60

// RelyingParty is the identity a passkey is bound to.
//
// A credential registered for one relying-party identifier will not be offered
// for another, which is what makes a passkey unphishable — and what makes
// changing the panel's domain a decision with consequences: every existing key
// stops working, because the browser will not offer it to a site it was not
// created for.
type RelyingParty struct {
	// ID is the domain, without scheme or port. A credential is scoped to it.
	ID string
	// Origin is the exact scheme, host, and port the browser will report.
	Origin string
	// Name is what an authenticator shows the person when it asks.
	Name string
}

// NewRelyingParty derives the relying party from the panel's public URL.
//
// It is derived rather than configured separately so the two cannot disagree.
// A mismatch here does not fail loudly: the browser simply refuses to produce a
// credential, and an operator is left with a button that does nothing.
func NewRelyingParty(publicURL, name string) (RelyingParty, error) {
	trimmed := strings.TrimSpace(publicURL)
	if trimmed == "" {
		return RelyingParty{}, ErrPasskeyOriginUnknown
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return RelyingParty{}, ErrPasskeyOriginUnknown
	}
	if name = strings.TrimSpace(name); name == "" {
		name = "Omniflow"
	}
	return RelyingParty{
		ID: parsed.Hostname(),
		// Origin keeps the port, because the browser reports it and a mismatch
		// is a refusal. Localhost on a development port is the common case.
		Origin: parsed.Scheme + "://" + parsed.Host,
		Name:   name,
	}, nil
}

// CheckSignCount decides whether an assertion's counter is acceptable.
//
// An authenticator that implements a counter increments it on every assertion,
// so a value at or below the stored one means the credential answered twice
// from what may be two devices. Many authenticators — including most platform
// ones — never implement it and report zero forever, which is legal, and
// treating that as a clone would lock out the most common hardware there is.
func CheckSignCount(stored, presented uint32) error {
	if stored == 0 && presented == 0 {
		return nil
	}
	if presented <= stored {
		return ErrPasskeyCloned
	}
	return nil
}

// PasskeyLabel normalises what an operator typed, and supplies something
// readable when they typed nothing.
func PasskeyLabel(label, fallback string) string {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		trimmed = strings.TrimSpace(fallback)
	}
	if trimmed == "" {
		trimmed = "Passkey"
	}
	if len(trimmed) > PasskeyLabelLimit {
		trimmed = strings.TrimSpace(trimmed[:PasskeyLabelLimit])
	}
	return trimmed
}
