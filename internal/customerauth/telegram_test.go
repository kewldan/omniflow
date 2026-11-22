package customerauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testBotToken = "123456:AA-test-token"

// signWidget produces the hash Telegram would put on a login-widget payload.
func signWidget(values url.Values, token string) url.Values {
	secret := sha256.Sum256([]byte(token))
	values.Set("hash", hex.EncodeToString(macOver(values, secret[:])))
	return values
}

// signInitData produces the hash Telegram would put on Mini App initData, which
// derives its key differently from the widget's.
func signInitData(values url.Values, token string) string {
	key := hmac.New(sha256.New, []byte("WebAppData"))
	key.Write([]byte(token))
	values.Set("hash", hex.EncodeToString(macOver(values, key.Sum(nil))))
	return values.Encode()
}

// macOver computes the check hash as Telegram does with the bot token: over
// every field but the hash, the Ed25519 signature included.
func macOver(values url.Values, secret []byte) []byte {
	return macOverExcluding(values, secret, "hash")
}

func macOverExcluding(values url.Values, secret []byte, excluded ...string) []byte {
	pairs := make([]string, 0, len(values))
	for key, entries := range values {
		if slices.Contains(excluded, key) {
			continue
		}
		pairs = append(pairs, key+"="+entries[0])
	}
	sort.Strings(pairs)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strings.Join(pairs, "\n")))
	return mac.Sum(nil)
}

func widgetPayload(now time.Time) url.Values {
	return url.Values{
		"id":         {"4820194"},
		"first_name": {"Alexey"},
		"last_name":  {"Smirnov"},
		"username":   {"alexey"},
		"auth_date":  {strconv.FormatInt(now.Unix(), 10)},
	}
}

func TestVerifyLoginWidgetAcceptsAGenuinePayload(t *testing.T) {
	now := time.Now().UTC()
	identity, err := VerifyLoginWidget(signWidget(widgetPayload(now), testBotToken), testBotToken, now, TelegramMaxAge)
	if err != nil {
		t.Fatalf("VerifyLoginWidget: %v", err)
	}
	if identity.ID != 4820194 {
		t.Fatalf("id = %d, want 4820194", identity.ID)
	}
	if identity.Subject() != "4820194" {
		t.Fatalf("subject = %q", identity.Subject())
	}
	if identity.DisplayName() != "Alexey Smirnov" {
		t.Fatalf("display name = %q", identity.DisplayName())
	}
}

func TestVerifyLoginWidgetRejectsATamperedField(t *testing.T) {
	now := time.Now().UTC()
	values := signWidget(widgetPayload(now), testBotToken)
	// Changing the identifier after signing is the whole attack: a valid hash for
	// one account must not authenticate another.
	values.Set("id", "999")

	if _, err := VerifyLoginWidget(values, testBotToken, now, TelegramMaxAge); !errors.Is(err, ErrTelegramSignature) {
		t.Fatalf("error = %v, want ErrTelegramSignature", err)
	}
}

func TestVerifyLoginWidgetRejectsAnAddedField(t *testing.T) {
	now := time.Now().UTC()
	values := signWidget(widgetPayload(now), testBotToken)
	// Every received field participates in the check string, so an appended one
	// invalidates the payload rather than riding along unchecked.
	values.Set("photo_url", "https://example.test/avatar.jpg")

	if _, err := VerifyLoginWidget(values, testBotToken, now, TelegramMaxAge); !errors.Is(err, ErrTelegramSignature) {
		t.Fatalf("error = %v, want ErrTelegramSignature", err)
	}
}

func TestVerifyLoginWidgetRejectsAnotherBotsSignature(t *testing.T) {
	now := time.Now().UTC()
	values := signWidget(widgetPayload(now), "999999:BB-other-bot")

	if _, err := VerifyLoginWidget(values, testBotToken, now, TelegramMaxAge); !errors.Is(err, ErrTelegramSignature) {
		t.Fatalf("error = %v, want ErrTelegramSignature", err)
	}
}

func TestVerifyLoginWidgetRejectsAReplayedPayload(t *testing.T) {
	signedAt := time.Now().UTC().Add(-2 * time.Hour)
	values := signWidget(widgetPayload(signedAt), testBotToken)

	// The hash stays valid forever, which is exactly why the freshness bound
	// exists: a payload captured from a URL must not authenticate a day later.
	if _, err := VerifyLoginWidget(values, testBotToken, time.Now().UTC(), TelegramMaxAge); !errors.Is(err, ErrTelegramStale) {
		t.Fatalf("error = %v, want ErrTelegramStale", err)
	}
}

func TestVerifyLoginWidgetRejectsAFutureAuthDate(t *testing.T) {
	now := time.Now().UTC()
	values := signWidget(widgetPayload(now.Add(10*time.Minute)), testBotToken)

	if _, err := VerifyLoginWidget(values, testBotToken, now, TelegramMaxAge); !errors.Is(err, ErrTelegramStale) {
		t.Fatalf("error = %v, want ErrTelegramStale", err)
	}
}

func TestVerifyLoginWidgetToleratesASlightlyFastClock(t *testing.T) {
	now := time.Now().UTC()
	values := signWidget(widgetPayload(now.Add(10*time.Second)), testBotToken)

	if _, err := VerifyLoginWidget(values, testBotToken, now, TelegramMaxAge); err != nil {
		t.Fatalf("a client ten seconds ahead was refused: %v", err)
	}
}

func TestVerifyLoginWidgetRefusesWithoutABotToken(t *testing.T) {
	now := time.Now().UTC()
	values := signWidget(widgetPayload(now), testBotToken)

	if _, err := VerifyLoginWidget(values, "", now, TelegramMaxAge); !errors.Is(err, ErrTelegramSignature) {
		t.Fatalf("error = %v, want ErrTelegramSignature", err)
	}
}

func TestVerifyMiniAppInitDataAcceptsAGenuinePayload(t *testing.T) {
	now := time.Now().UTC()
	values := url.Values{
		"user":      {`{"id":4820194,"username":"alexey","first_name":"Alexey","language_code":"ru"}`},
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
		"query_id":  {"AAH-test"},
	}
	identity, err := VerifyMiniAppInitData(signInitData(values, testBotToken), testBotToken, now, TelegramMaxAge)
	if err != nil {
		t.Fatalf("VerifyMiniAppInitData: %v", err)
	}
	if identity.ID != 4820194 || identity.LanguageCode != "ru" {
		t.Fatalf("identity = %+v", identity)
	}
}

func TestMiniAppAndWidgetKeysAreNotInterchangeable(t *testing.T) {
	now := time.Now().UTC()
	values := url.Values{
		"user":      {`{"id":4820194}`},
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
	}
	// Signed with the Mini App key, presented as a widget payload. The two
	// constructions differ, and treating them as one would accept a payload the
	// widget path has no grounds to trust.
	initData := signInitData(values, testBotToken)
	parsed, err := url.ParseQuery(initData)
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	parsed.Set("id", "4820194")

	if _, err = VerifyLoginWidget(parsed, testBotToken, now, TelegramMaxAge); err == nil {
		t.Fatal("a Mini App payload verified as a login-widget payload")
	}
}

func TestVerifyMiniAppInitDataRejectsMalformedUser(t *testing.T) {
	now := time.Now().UTC()
	values := url.Values{
		"user":      {`not json`},
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
	}
	if _, err := VerifyMiniAppInitData(
		signInitData(values, testBotToken), testBotToken, now, TelegramMaxAge,
	); !errors.Is(err, ErrTelegramMalformed) {
		t.Fatalf("error = %v, want ErrTelegramMalformed", err)
	}
}

func TestVerifyMiniAppInitDataIgnoresTheEd25519Signature(t *testing.T) {
	now := time.Now().UTC()
	values := url.Values{
		"user":      {`{"id":4820194}`},
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
	}
	initData := signInitData(values, testBotToken)
	// Telegram's separate third-party attestation is not part of the HMAC check
	// string, so its presence must not invalidate an otherwise good payload.
	initData += "&signature=" + url.QueryEscape("irrelevant-to-the-hmac")

	if _, err := VerifyMiniAppInitData(initData, testBotToken, now, TelegramMaxAge); err != nil {
		t.Fatalf("a payload carrying `signature` was refused: %v", err)
	}
}

func TestDisplayNameFallsBackWhenNamesAreAbsent(t *testing.T) {
	if got := (TelegramIdentity{ID: 7, Username: "nick"}).DisplayName(); got != "@nick" {
		t.Fatalf("display name = %q, want @nick", got)
	}
	if got := (TelegramIdentity{ID: 7}).DisplayName(); got != "7" {
		t.Fatalf("display name = %q, want 7", got)
	}
}

// TestVerifyMiniAppInitDataCoversTheSignature pins Telegram's rule for the
// bot-token check: a client that sends the Ed25519 signature signs over it,
// and launch data from such a client must be accepted. An earlier check left
// the signature out and refused every current client.
func TestVerifyMiniAppInitDataCoversTheSignature(t *testing.T) {
	now := time.Now().UTC()
	values := url.Values{
		"user":      {`{"id":4820194,"first_name":"Alexey"}`},
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
		"query_id":  {"AAH"},
		"signature": {"c2lnbmF0dXJl"},
	}
	initData := signInitData(values, testBotToken)
	if _, err := VerifyMiniAppInitData(initData, testBotToken, now, MiniAppMaxAge); err != nil {
		t.Fatalf("launch data signed over its signature was refused: %v", err)
	}

	// A hash computed without the signature is still the bot's, and accepted.
	key := hmac.New(sha256.New, []byte("WebAppData"))
	key.Write([]byte(testBotToken))
	legacy := url.Values{}
	for name, entries := range values {
		if name != "hash" {
			legacy[name] = entries
		}
	}
	legacy.Set("hash", hex.EncodeToString(macOverExcluding(legacy, key.Sum(nil), "hash", "signature")))
	if _, err := VerifyMiniAppInitData(legacy.Encode(), testBotToken, now, MiniAppMaxAge); err != nil {
		t.Fatalf("launch data hashed without its signature was refused: %v", err)
	}

	// Changing the signature after signing breaks the hash.
	tampered, _ := url.ParseQuery(initData)
	tampered.Set("signature", "b3RoZXI")
	if _, err := VerifyMiniAppInitData(tampered.Encode(), testBotToken, now, MiniAppMaxAge); err == nil {
		t.Fatal("a signature changed after signing was accepted")
	}
}

// TestMiniAppLaunchDataOutlivesTheWidgetMinute: a Mini App reopened from the
// background keeps its launch data, so ten minutes later it still signs in,
// and past the hour it does not.
func TestMiniAppLaunchDataOutlivesTheWidgetMinute(t *testing.T) {
	launched := time.Now().UTC().Add(-10 * time.Minute)
	values := url.Values{
		"user":      {`{"id":4820194,"first_name":"Alexey"}`},
		"auth_date": {strconv.FormatInt(launched.Unix(), 10)},
	}
	initData := signInitData(values, testBotToken)
	if _, err := VerifyMiniAppInitData(initData, testBotToken, time.Now().UTC(), MiniAppMaxAge); err != nil {
		t.Fatalf("ten-minute-old launch data was refused: %v", err)
	}
	if _, err := VerifyMiniAppInitData(initData, testBotToken, launched.Add(2*time.Hour), MiniAppMaxAge); !errors.Is(err, ErrTelegramStale) {
		t.Fatalf("two-hour-old launch data gave %v, want ErrTelegramStale", err)
	}
}
