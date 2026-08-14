package adtracking

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// A counter identifier is interpolated into a script that runs in every
// visitor's browser, and a visitor's browser holds subscription links. What is
// tested here is therefore what gets refused, not what gets stored.

func TestACounterIdentifierCannotCarryAnything(t *testing.T) {
	for _, refused := range []Settings{
		{Counters: map[Provider]string{ProviderMetrica: "12345');alert(1)//"}},
		{Counters: map[Provider]string{ProviderMetrica: "12345 "}},
		{Counters: map[Provider]string{ProviderMetrica: "abc"}},
		{Counters: map[Provider]string{ProviderGA4: "G-abc"}},
		{Counters: map[Provider]string{ProviderGA4: `G-AAAAAA"></script><script>`}},
		{Counters: map[Provider]string{"free_text": "anything"}},
	} {
		if err := CheckSettings(refused); err == nil {
			t.Fatalf("%+v was accepted", refused.Counters)
		}
	}

	accepted := Settings{Counters: map[Provider]string{
		ProviderMetrica: "12345678", ProviderGA4: "G-AB12CD34",
	}}
	if err := CheckSettings(accepted); err != nil {
		t.Fatalf("valid identifiers were refused: %v", err)
	}
}

func TestAVerificationTagIsAnAllowlistedNameAndAnOpaqueToken(t *testing.T) {
	for _, refused := range []Verification{
		{Name: "description", Content: "anything-at-all"},
		{Name: "refresh", Content: "0;url=https://example.test"},
		{Name: "google-site-verification", Content: `"><script>alert(1)</script>`},
		{Name: "google-site-verification", Content: "short"},
	} {
		if err := CheckSettings(Settings{Verifications: []Verification{refused}}); !errors.Is(
			err, ErrUnknownVerification,
		) && !errors.Is(err, ErrMalformedToken) {
			t.Fatalf("%+v was accepted: %v", refused, err)
		}
	}

	if err := CheckSettings(Settings{Verifications: []Verification{
		{Name: "yandex-verification", Content: "a1b2c3d4e5f6"},
	}}); err != nil {
		t.Fatalf("a valid tag was refused: %v", err)
	}
}

// Nothing renders on an installation that has not decided to measure anything.
func TestNothingIsMeasurableUntilSomebodyTurnsItOn(t *testing.T) {
	if Measurable(Settings{}) {
		t.Fatal("a fresh installation is measurable")
	}
	// Identifiers with the switch off are kept but inert, so turning
	// measurement off does not mean finding the numbers again to turn it back on.
	off := Settings{Counters: map[Provider]string{ProviderMetrica: "12345678"}}
	if Measurable(off) {
		t.Fatal("a configured counter measures with the switch off")
	}
	if !Measurable(Settings{Enabled: true, Counters: off.Counters}) {
		t.Fatal("a configured counter does not measure with the switch on")
	}
	// And the switch on its own renders nothing, so no consent is asked for.
	if Measurable(Settings{Enabled: true}) {
		t.Fatal("the switch alone made an installation measurable")
	}
}

// The capture is a closed list. "Store whatever the URL carried" is how a
// session token or an e-mail address ends up in an analytics table.
func TestOnlyKnownAdvertisingParametersAreCaptured(t *testing.T) {
	captured, err := Capture(
		"gclid=Cj0KCQiA_ABC-123&session=secret-token&email=someone%40example.test" +
			"&utm_source=google&utm_medium=cpc&utm_campaign=spring")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if captured.ClickID != "Cj0KCQiA_ABC-123" || captured.ClickSource != "google" {
		t.Fatalf("the click reads %+v", captured)
	}
	if captured.Source != "google" || captured.Medium != "cpc" || captured.Campaign != "spring" {
		t.Fatalf("the campaign reads %+v", captured)
	}

	// Nothing else survived, because there is nowhere for it to go.
	if strings.Contains(captured.Term+captured.Content, "secret-token") {
		t.Fatalf("an unrelated parameter was captured: %+v", captured)
	}
}

// Most visits are not from an advertisement, and a row for every visit would be
// a log of who arrived rather than a record of what an advertisement bought.
func TestAnOrdinaryVisitCapturesNothing(t *testing.T) {
	for _, query := range []string{"", "page=2", "ref=friend", "utm_source=&utm_medium="} {
		if _, err := Capture(query); !errors.Is(err, ErrNoAttribution) {
			t.Fatalf("%q produced an attribution", query)
		}
	}
}

// A malformed click identifier is dropped, not refused. The visitor did nothing
// wrong and is trying to buy something.
func TestAMalformedClickIdentifierDoesNotBreakTheLanding(t *testing.T) {
	captured, err := Capture("gclid=" + url.QueryEscape("<script>") + "&utm_source=google")
	if err != nil {
		t.Fatalf("a malformed click identifier failed the capture: %v", err)
	}
	if captured.ClickID != "" {
		t.Fatalf("a malformed identifier was captured: %q", captured.ClickID)
	}
	if captured.Source != "google" {
		t.Fatalf("the rest of the capture was lost: %+v", captured)
	}
}

// A campaign name travels through a customer's URL bar and ends up in a CSV an
// operator uploads. A newline in a cell is a corrupted file.
func TestACampaignFieldCannotCarryANewlineOrRunAway(t *testing.T) {
	captured, err := Capture(
		"utm_campaign=" + url.QueryEscape("spring\nsale,\"quoted\"") +
			"&utm_term=" + url.QueryEscape(strings.Repeat("x", 500)))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if strings.ContainsAny(captured.Campaign, "\n\r") {
		t.Fatalf("a newline survived: %q", captured.Campaign)
	}
	if len([]rune(captured.Term)) > maxCampaignField {
		t.Fatalf("the term is %d runes", len([]rune(captured.Term)))
	}
}

// An attribution arriving from a browser is checked rather than trusted: the
// storefront is the only thing that should be sending one, and "should" is not
// a control.
func TestAnAttributionFromABrowserIsChecked(t *testing.T) {
	if err := CheckAttribution(Attribution{
		ClickID: "abc123def", ClickSource: "not-a-platform",
	}); !errors.Is(err, ErrUnknownClickSource) {
		t.Fatal("an unknown platform was accepted")
	}
	if err := CheckAttribution(Attribution{
		ClickID: "no", ClickSource: "google",
	}); !errors.Is(err, ErrMalformedClickID) {
		t.Fatal("a malformed identifier was accepted")
	}
	if err := CheckAttribution(Attribution{}); !errors.Is(err, ErrNoAttribution) {
		t.Fatal("an empty attribution was accepted")
	}
	if err := CheckAttribution(Attribution{Source: "newsletter"}); err != nil {
		t.Fatalf("a campaign with no click identifier was refused: %v", err)
	}
}

func TestNormalisingDropsWhatWouldStoreNothing(t *testing.T) {
	normalised := Normalise(Settings{
		Enabled:  true,
		Counters: map[Provider]string{ProviderMetrica: "  ", ProviderGA4: " G-AB12CD34 "},
		Verifications: []Verification{
			{Name: "Yandex-Verification", Content: " a1b2c3d4e5f6 "},
			{Name: "yandex-verification", Content: "duplicate-token"},
			{Name: "", Content: "orphan"},
		},
	})
	if _, present := normalised.Counters[ProviderMetrica]; present {
		t.Fatal("an empty identifier was kept")
	}
	if normalised.Counters[ProviderGA4] != "G-AB12CD34" {
		t.Fatalf("the identifier reads %q", normalised.Counters[ProviderGA4])
	}
	if len(normalised.Verifications) != 1 {
		t.Fatalf("%d verifications survived", len(normalised.Verifications))
	}
	if normalised.Verifications[0].Name != "yandex-verification" {
		t.Fatalf("the tag reads %+v", normalised.Verifications[0])
	}
	if err := CheckSettings(normalised); err != nil {
		t.Fatalf("normalising produced something invalid: %v", err)
	}
}
