package customerauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
)

// The widget's own serialisation: id and auth_date are numbers, not strings.
// That is the document every browser posts, and it is what the API must
// accept — the earlier handler refused it before the signature was checked.
func TestWidgetValuesFromJSONKeepsNumbersAsTheDigitsTelegramSigned(t *testing.T) {
	raw := []byte(`{"id":7700012345678901,"first_name":"Alexey","auth_date":1786660859,"hash":"abc"}`)
	values, err := WidgetValuesFromJSON(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := values.Get("id"); got != "7700012345678901" {
		t.Fatalf("id = %q, want the digits verbatim (float formatting would mangle them)", got)
	}
	if got := values.Get("auth_date"); got != "1786660859" {
		t.Fatalf("auth_date = %q", got)
	}
	if got := values.Get("first_name"); got != "Alexey" {
		t.Fatalf("first_name = %q", got)
	}
}

// A numeric payload, signed the way Telegram signs it, must verify end to end
// through the decoder. This is the property the handler relies on.
func TestANumericWidgetPayloadVerifiesAfterDecoding(t *testing.T) {
	const token = "000000:unit-token"
	at := time.Unix(1786660859, 0)
	fields := map[string]string{"auth_date": "1786660859", "first_name": "Playwright", "id": "770001"}
	pairs := make([]string, 0, len(fields))
	for key, value := range fields {
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs)
	secret := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(pairs, "\n")))
	hash := hex.EncodeToString(mac.Sum(nil))

	raw := []byte(`{"id":770001,"first_name":"Playwright","auth_date":1786660859,"hash":"` + hash + `"}`)
	values, err := WidgetValuesFromJSON(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	identity, err := VerifyLoginWidget(values, token, at, TelegramMaxAge)
	if err != nil {
		t.Fatalf("a numeric widget payload did not verify: %v", err)
	}
	if identity.ID != 770001 {
		t.Fatalf("id = %d", identity.ID)
	}
}

func TestWidgetValuesFromJSONRefusesWhatTheWidgetNeverSends(t *testing.T) {
	cases := map[string]string{
		"nested object":  `{"id":1,"auth_date":2,"hash":"x","user":{"id":1}}`,
		"array":          `{"id":[1],"auth_date":2,"hash":"x"}`,
		"not an object":  `[1,2,3]`,
		"two documents":  `{"id":1}{"id":2}`,
		"malformed json": `{"id":`,
		"null document":  `null`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := WidgetValuesFromJSON([]byte(raw)); !errors.Is(err, ErrWidgetPayloadInvalid) {
				t.Fatalf("err = %v, want ErrWidgetPayloadInvalid", err)
			}
		})
	}
}

func TestWidgetValuesFromJSONCarriesBooleansAndDropsNull(t *testing.T) {
	values, err := WidgetValuesFromJSON([]byte(`{"id":1,"auth_date":2,"hash":"x","flag":true,"photo_url":null}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if values.Get("flag") != "true" {
		t.Fatalf("flag = %q", values.Get("flag"))
	}
	if _, present := values["photo_url"]; present {
		t.Fatal("a null field was carried into the signed set")
	}
}
