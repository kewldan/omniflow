package mailer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// ErrBadUnsubscribeToken reports a token that was not issued here.
var ErrBadUnsubscribeToken = errors.New("unsubscribe token is not valid")

// UnsubscribeSigner issues and checks the token a one-click unsubscribe link
// carries. The token names a customer and is authenticated with a key derived
// from the installation's data key, so following the link needs no session —
// which RFC 8058 requires, because the mail client posts it on the reader's
// behalf — and a stranger cannot forge one for somebody else's address.
//
// It does not expire: an unsubscribe link in a two-year-old e-mail must still
// work, and all it can ever do is stop marketing mail.
type UnsubscribeSigner struct {
	key []byte
}

// NewUnsubscribeSigner derives the signing key. The derivation label keeps it
// distinct from every other use of the data key.
func NewUnsubscribeSigner(dataKey []byte) (UnsubscribeSigner, error) {
	if len(dataKey) != 32 {
		return UnsubscribeSigner{}, errors.New("unsubscribe signing needs the 32-byte data key")
	}
	mac := hmac.New(sha256.New, dataKey)
	_, _ = mac.Write([]byte("omniflow/email-unsubscribe/v1"))
	return UnsubscribeSigner{key: mac.Sum(nil)}, nil
}

// Token returns the token for one customer.
func (signer UnsubscribeSigner) Token(customerID string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(customerID))
	return payload + "." + signer.sign(payload)
}

// Verify returns the customer a token names.
func (signer UnsubscribeSigner) Verify(token string) (string, error) {
	payload, signature, found := strings.Cut(strings.TrimSpace(token), ".")
	if !found || len(signer.key) == 0 {
		return "", ErrBadUnsubscribeToken
	}
	if !hmac.Equal([]byte(signature), []byte(signer.sign(payload))) {
		return "", ErrBadUnsubscribeToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || len(raw) == 0 || len(raw) > 64 {
		return "", ErrBadUnsubscribeToken
	}
	return string(raw), nil
}

func (signer UnsubscribeSigner) sign(payload string) string {
	mac := hmac.New(sha256.New, signer.key)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:18])
}
