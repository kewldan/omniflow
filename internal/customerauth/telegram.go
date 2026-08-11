package customerauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrTelegramSignature reports a payload whose hash does not verify. It is
	// deliberately the same error for a forged hash, a tampered field, and a
	// payload signed by a different bot: none of those distinctions is something
	// the caller should be told.
	ErrTelegramSignature = errors.New("telegram payload did not verify")
	// ErrTelegramStale reports a payload whose auth_date is outside the accepted
	// window.
	ErrTelegramStale = errors.New("telegram payload is too old")
	// ErrTelegramMalformed reports a payload missing the fields every Telegram
	// sign-in carries.
	ErrTelegramMalformed = errors.New("telegram payload is malformed")
)

// TelegramMaxAge is how long a signed Telegram payload stays acceptable.
//
// The hash never expires on its own, so without this a payload captured once —
// from a browser history entry, a referrer, a screenshot of a URL — would
// authenticate forever. A minute is far longer than the round trip needs and far
// shorter than any window worth capturing for.
const TelegramMaxAge = time.Minute

// telegramClockSkew tolerates a client whose clock runs slightly ahead. Without
// it, a user whose machine is a few seconds fast cannot sign in at all.
const telegramClockSkew = 30 * time.Second

// TelegramIdentity is what a verified Telegram payload establishes.
//
// It carries the numeric ID, which is the subject the identity is keyed on, and
// the display fields Telegram supplies. Nothing here is trusted for
// authorization: the ID decides which customer this is, and the rest is only
// ever shown back to the person who just proved they own the account.
type TelegramIdentity struct {
	ID           int64
	Username     string
	FirstName    string
	LastName     string
	PhotoURL     string
	LanguageCode string
	AuthDate     time.Time
}

// Subject is the value stored in `identities.provider_subject`.
func (identity TelegramIdentity) Subject() string {
	return strconv.FormatInt(identity.ID, 10)
}

// DisplayName is the customer-facing name, falling back to the username and then
// to the bare identifier so it is never empty.
func (identity TelegramIdentity) DisplayName() string {
	name := strings.TrimSpace(identity.FirstName + " " + identity.LastName)
	if name != "" {
		return name
	}
	if identity.Username != "" {
		return "@" + identity.Username
	}
	return identity.Subject()
}

// VerifyLoginWidget checks a Telegram Login Widget callback.
//
// The widget signs its fields with HMAC-SHA256 under SHA-256 of the bot token,
// which means only an installation holding that token can verify a payload — and
// that a payload verifying at all is proof Telegram produced it for this bot.
func VerifyLoginWidget(
	values url.Values, botToken string, now time.Time, maxAge time.Duration,
) (TelegramIdentity, error) {
	if strings.TrimSpace(botToken) == "" {
		return TelegramIdentity{}, ErrTelegramSignature
	}
	secret := sha256.Sum256([]byte(botToken))
	if err := verifyTelegramHash(values, secret[:], values.Get("hash")); err != nil {
		return TelegramIdentity{}, err
	}

	identity := TelegramIdentity{
		Username:  values.Get("username"),
		FirstName: values.Get("first_name"),
		LastName:  values.Get("last_name"),
		PhotoURL:  values.Get("photo_url"),
	}
	id, err := strconv.ParseInt(values.Get("id"), 10, 64)
	if err != nil || id <= 0 {
		return TelegramIdentity{}, ErrTelegramMalformed
	}
	identity.ID = id

	if identity.AuthDate, err = parseAuthDate(values.Get("auth_date")); err != nil {
		return TelegramIdentity{}, err
	}
	if err = checkFreshness(identity.AuthDate, now, maxAge); err != nil {
		return TelegramIdentity{}, err
	}
	return identity, nil
}

// VerifyMiniAppInitData checks the `initData` string a Telegram Mini App passes
// to its own page.
//
// The construction differs from the widget's in one place that matters: the HMAC
// key is HMAC-SHA256 of the bot token under the literal "WebAppData", rather
// than a plain SHA-256 of the token. Using the widget's key here would verify
// nothing, so the two paths stay separate rather than sharing a "verify Telegram
// thing" helper that guesses which is which.
func VerifyMiniAppInitData(
	initData, botToken string, now time.Time, maxAge time.Duration,
) (TelegramIdentity, error) {
	if strings.TrimSpace(botToken) == "" {
		return TelegramIdentity{}, ErrTelegramSignature
	}
	values, err := url.ParseQuery(initData)
	if err != nil {
		return TelegramIdentity{}, ErrTelegramMalformed
	}

	mac := hmac.New(sha256.New, []byte("WebAppData"))
	mac.Write([]byte(botToken))
	if err = verifyTelegramHash(values, mac.Sum(nil), values.Get("hash")); err != nil {
		return TelegramIdentity{}, err
	}

	// The user is a JSON document inside the signed payload rather than flat
	// fields, so it is parsed only after the signature has been established.
	var user struct {
		ID           int64  `json:"id"`
		Username     string `json:"username"`
		FirstName    string `json:"first_name"`
		LastName     string `json:"last_name"`
		PhotoURL     string `json:"photo_url"`
		LanguageCode string `json:"language_code"`
	}
	if err = json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID <= 0 {
		return TelegramIdentity{}, ErrTelegramMalformed
	}

	identity := TelegramIdentity{
		ID: user.ID, Username: user.Username, FirstName: user.FirstName,
		LastName: user.LastName, PhotoURL: user.PhotoURL, LanguageCode: user.LanguageCode,
	}
	if identity.AuthDate, err = parseAuthDate(values.Get("auth_date")); err != nil {
		return TelegramIdentity{}, err
	}
	if err = checkFreshness(identity.AuthDate, now, maxAge); err != nil {
		return TelegramIdentity{}, err
	}
	return identity, nil
}

// verifyTelegramHash rebuilds the data-check-string and compares the MAC.
//
// Every received field except the hash itself participates, which is the whole
// point: a field Telegram adds later is covered automatically, and a field an
// attacker appends invalidates the payload rather than riding along unchecked.
// `signature` is the one other exclusion — it is Telegram's separate Ed25519
// attestation for third parties and is not part of the HMAC check string.
func verifyTelegramHash(values url.Values, secret []byte, provided string) error {
	if provided == "" {
		return ErrTelegramMalformed
	}
	expected, err := hex.DecodeString(provided)
	if err != nil {
		return ErrTelegramSignature
	}

	pairs := make([]string, 0, len(values))
	for key, entries := range values {
		if key == "hash" || key == "signature" || len(entries) == 0 {
			continue
		}
		pairs = append(pairs, key+"="+entries[0])
	}
	sort.Strings(pairs)

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strings.Join(pairs, "\n")))
	if !hmac.Equal(mac.Sum(nil), expected) {
		return ErrTelegramSignature
	}
	return nil
}

func parseAuthDate(value string) (time.Time, error) {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, ErrTelegramMalformed
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func checkFreshness(authDate, now time.Time, maxAge time.Duration) error {
	if maxAge <= 0 {
		maxAge = TelegramMaxAge
	}
	if now.Sub(authDate) > maxAge {
		return ErrTelegramStale
	}
	// A payload dated meaningfully in the future is not a clock problem, it is a
	// forged or replayed one.
	if authDate.Sub(now) > telegramClockSkew {
		return ErrTelegramStale
	}
	return nil
}
