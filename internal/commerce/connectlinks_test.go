package commerce

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"strings"
	"testing"
)

const sampleLink = "https://sub.example.com/api/sub/AbCdEf123"

func TestHappCrypt4HidesTheLink(t *testing.T) {
	t.Parallel()
	link, err := HappCrypt4(sampleLink)
	if err != nil {
		t.Fatal(err)
	}
	encoded, found := strings.CutPrefix(link, "happ://crypt4/")
	if !found || strings.Contains(link, "sub.example.com") {
		t.Fatalf("link = %q", link)
	}
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(sealed) != 512 {
		t.Fatalf("a 4096-bit RSA block is 512 bytes, got %d (%v)", len(sealed), err)
	}
	if _, err := HappCrypt4(strings.Repeat("x", 600)); err == nil {
		t.Fatal("a link longer than one RSA block was accepted")
	}
}

func TestIncyCrypt1RoundTrips(t *testing.T) {
	t.Parallel()
	link, err := IncyCrypt1(sampleLink, "Мой VPN")
	if err != nil {
		t.Fatal(err)
	}
	encoded, found := strings.CutPrefix(link, "incy://crypt1/")
	if !found || strings.ContainsAny(encoded, "+/=") {
		t.Fatalf("link = %q, want unpadded base64url", link)
	}
	sealed, _ := base64.RawURLEncoding.DecodeString(encoded)
	key, _ := base64.StdEncoding.DecodeString(incyKey)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	plain, err := aead.Open(nil, sealed[:12], sealed[12:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != `{"n":"Мой VPN","url":"https://sub.example.com/api/sub/AbCdEf123","v":1}` {
		t.Fatalf("plaintext = %s", plain)
	}
}

func TestDeepLinksAreEncryptedOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	happ := ClientApp{Name: "Happ", Scheme: "happ://add/"}
	incy := ClientApp{Name: "INCY", Scheme: "incy://import/"}
	other := ClientApp{Name: "Streisand", Scheme: "streisand://import/"}
	plain := ConnectSettings{}
	if plain.DeepLink(happ, sampleLink) != "happ://add/"+sampleLink {
		t.Fatal("encryption must be opt-in")
	}
	secret := ConnectSettings{HappCryptoLinks: true, IncyCryptoLinks: true}
	if !strings.HasPrefix(secret.DeepLink(happ, sampleLink), "happ://crypt4/") ||
		!strings.HasPrefix(secret.DeepLink(incy, sampleLink), "incy://crypt1/") ||
		secret.DeepLink(other, sampleLink) != "streisand://import/"+sampleLink {
		t.Fatal("only Happ and INCY links are encrypted")
	}
	if secret.DeepLink(happ, "") != "" {
		t.Fatal("no link, no button")
	}
}

func TestMirrors(t *testing.T) {
	t.Parallel()
	mirrors, err := ParseLinkMirrors("Reserve|https://Mirror.example.net;\nBackup | https://m2.example.org/ ; Same|https://mirror.example.net")
	if err != nil || len(mirrors) != 2 {
		t.Fatalf("mirrors = %+v (%v)", mirrors, err)
	}
	settings := ConnectSettings{Mirrors: mirrors}
	links := settings.MirrorLinks(sampleLink + "?x=1")
	if len(links) != 2 || links[0].URL != "https://mirror.example.net/api/sub/AbCdEf123?x=1" || links[1].Label != "Backup" {
		t.Fatalf("links = %+v", links)
	}
	for _, bad := range []string{"https://no-label.example", "L|http://plain.example", "L|https://x.example/path", "L|https://u:p@x.example"} {
		if _, err := ParseLinkMirrors(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestConnectSettingsFromEnv(t *testing.T) {
	t.Parallel()
	env := map[string]string{"APP_CONNECT_BUTTON_MODE": "Happ", "APP_CONNECT_HIDE_SUBSCRIPTION_LINK": "true"}
	settings, err := ConnectSettingsFromEnv(func(key string) string { return env[key] })
	if err != nil || settings.ButtonMode != ConnectModeHapp || !settings.HideRawLink {
		t.Fatalf("settings = %+v (%v)", settings, err)
	}
	if defaults, _ := ConnectSettingsFromEnv(func(string) string { return "" }); defaults.ButtonMode != ConnectModeGuide {
		t.Fatalf("default mode = %q", defaults.ButtonMode)
	}
	if _, err := ConnectSettingsFromEnv(func(key string) string {
		return map[string]string{"APP_CONNECT_BUTTON_MODE": "carrier-pigeon"}[key]
	}); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}
