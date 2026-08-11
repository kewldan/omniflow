// Package websession holds the primitives every cookie-backed browser session
// in Omniflow is built from: the session token, its storage digest, and the
// per-session CSRF secret.
//
// It exists because there are two such surfaces — the operator panel and, from
// v0.9, the customer panel — and the part they genuinely share is exactly this
// small piece of cryptography. What they do not share is policy: the two have
// different lifetimes, different revocation vocabularies, and different second
// factors, so each keeps its own policy type in its own domain package.
//
// The rule this package encodes is that a stored session must be useless to
// whoever reads the storage. Only digests are persisted, so a database dump or
// a leaked backup never yields a cookie that authenticates anything.
package websession

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

const (
	// TokenBytes is the entropy in a session token. 256 bits makes the token
	// unguessable independently of any rate limit in front of it.
	TokenBytes = 32
	// CSRFSecretBytes is the per-session secret the double-submit token is
	// derived from.
	CSRFSecretBytes = 32
)

// NewToken returns a URL-safe session token and the digest to store for it.
func NewToken() (token string, digest []byte, err error) {
	raw := make([]byte, TokenBytes)
	if _, err = rand.Read(raw); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken returns the storage digest for a session token.
//
// SHA-256 is correct here rather than a password hash: the token is 256 bits of
// uniform entropy, so there is no low-entropy secret for a slow hash to defend,
// and this runs on every authenticated request.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// NewCSRFSecret returns a per-session secret for the double-submit token.
func NewCSRFSecret() ([]byte, error) {
	secret := make([]byte, CSRFSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// CSRFToken derives the token handed to the browser from the session's secret.
//
// Binding it to the session means a token minted for one session cannot be
// replayed inside another, which a bare random double-submit cookie allows.
// `label` separates the domains: a token derived for the operator panel is not
// a valid token for the customer panel even if both sessions somehow shared a
// secret, so one surface can never be used to mint credentials for the other.
func CSRFToken(secret []byte, label string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(label))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ValidCSRFToken reports whether a submitted token matches the session secret
// under the given label.
func ValidCSRFToken(secret []byte, label, submitted string) bool {
	if len(secret) == 0 || submitted == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(CSRFToken(secret, label)), []byte(submitted)) == 1
}
