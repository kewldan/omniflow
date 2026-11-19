package commerce

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Connect button modes: what the bot's Connect button leads to.
const (
	// ConnectModeGuide is the platform picker with per-app instructions.
	ConnectModeGuide = "guide"
	// ConnectModeMiniApp opens the customer panel's connect screen as a
	// Telegram Mini App.
	ConnectModeMiniApp = "miniapp"
	// ConnectModeLink hands over the subscription page itself.
	ConnectModeLink = "link"
	// ConnectModeHapp imports into Happ in one tap.
	ConnectModeHapp = "happ"
)

// ConnectSettings are the operator's choices about how a subscription link is
// handed to a customer.
type ConnectSettings struct {
	// HappCryptoLinks turns `happ://add/<link>` into `happ://crypt4/<…>`: the
	// link encrypted with Happ's public key, which only the Happ application
	// can read. The subscription link then never appears in a button's
	// address, where anything that reads links on the way can see it.
	HappCryptoLinks bool
	// IncyCryptoLinks does the same for INCY: `incy://crypt1/<…>`.
	IncyCryptoLinks bool
	// IncyProvider is the provider name INCY shows for the imported profile.
	IncyProvider string
	// HideRawLink keeps the bare subscription link off every customer
	// surface; only the import buttons and the QR code remain.
	HideRawLink bool
	// Mirrors are alternative origins that serve the same subscription, for
	// a customer whose network blocks the main one.
	Mirrors []LinkMirror
	// ButtonMode is one of the ConnectMode* constants.
	ButtonMode string
	// RevokeDisabled stops customers from re-issuing their own link
	// (APP_SUBSCRIPTION_REVOKE_ENABLED=false). Stored inverted so the zero
	// value keeps re-issue available, as it always was.
	RevokeDisabled bool
	// RevokeCooldown is the least time between two re-issues of one
	// subscription's link (APP_SUBSCRIPTION_REVOKE_COOLDOWN_MINUTES).
	RevokeCooldown time.Duration
}

// RevokeAllowed reports whether customers may re-issue their link at all.
func (settings ConnectSettings) RevokeAllowed() bool { return !settings.RevokeDisabled }

// LinkMirror is one alternative origin for the subscription link.
type LinkMirror struct {
	Label  string `json:"label"`
	Origin string `json:"-"`
}

// ConnectSettingsFromEnv reads APP_CONNECT_* and APP_SUBSCRIPTION_MIRRORS.
func ConnectSettingsFromEnv(getenv func(string) string) (ConnectSettings, error) {
	flag := func(key string) (bool, error) {
		raw := strings.TrimSpace(getenv(key))
		if raw == "" {
			return false, nil
		}
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return false, fmt.Errorf("%s must be true or false", key)
		}
		return value, nil
	}
	var settings ConnectSettings
	var err error
	if settings.HappCryptoLinks, err = flag("APP_CONNECT_HAPP_CRYPTO_LINKS"); err != nil {
		return ConnectSettings{}, err
	}
	if settings.IncyCryptoLinks, err = flag("APP_CONNECT_INCY_CRYPTO_LINKS"); err != nil {
		return ConnectSettings{}, err
	}
	if settings.HideRawLink, err = flag("APP_CONNECT_HIDE_SUBSCRIPTION_LINK"); err != nil {
		return ConnectSettings{}, err
	}
	settings.IncyProvider = strings.TrimSpace(getenv("APP_CONNECT_INCY_PROVIDER_NAME"))
	if len(settings.IncyProvider) > 128 {
		settings.IncyProvider = settings.IncyProvider[:128]
	}
	settings.ButtonMode = strings.ToLower(strings.TrimSpace(getenv("APP_CONNECT_BUTTON_MODE")))
	switch settings.ButtonMode {
	case "":
		settings.ButtonMode = ConnectModeGuide
	case ConnectModeGuide, ConnectModeMiniApp, ConnectModeLink, ConnectModeHapp:
	default:
		return ConnectSettings{}, fmt.Errorf("APP_CONNECT_BUTTON_MODE %q is not one of guide, miniapp, link, happ", settings.ButtonMode)
	}
	if settings.Mirrors, err = ParseLinkMirrors(getenv("APP_SUBSCRIPTION_MIRRORS")); err != nil {
		return ConnectSettings{}, err
	}
	if raw := strings.TrimSpace(getenv("APP_SUBSCRIPTION_REVOKE_ENABLED")); raw != "" {
		enabled, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return ConnectSettings{}, errors.New("APP_SUBSCRIPTION_REVOKE_ENABLED must be true or false")
		}
		settings.RevokeDisabled = !enabled
	}
	if raw := strings.TrimSpace(getenv("APP_SUBSCRIPTION_REVOKE_COOLDOWN_MINUTES")); raw != "" {
		minutes, parseErr := strconv.Atoi(raw)
		if parseErr != nil || minutes < 0 || minutes > 7*24*60 {
			return ConnectSettings{}, errors.New("APP_SUBSCRIPTION_REVOKE_COOLDOWN_MINUTES must be 0 to 10080")
		}
		settings.RevokeCooldown = time.Duration(minutes) * time.Minute
	}
	return settings, nil
}

// ParseLinkMirrors reads `Label|https://mirror.example` entries separated by
// semicolons or newlines — Bedolaga's SUBSCRIPTION_MIRROR_LINKS format. Each
// target must be a bare HTTPS origin: a mirror replaces only the scheme and
// host of the link, so a path would silently point at something else.
func ParseLinkMirrors(raw string) ([]LinkMirror, error) {
	var mirrors []LinkMirror
	seen := map[string]bool{}
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == '\n' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		label, target, found := strings.Cut(entry, "|")
		label, target = strings.TrimSpace(label), strings.TrimSpace(target)
		if !found || label == "" || len([]rune(label)) > 64 {
			return nil, fmt.Errorf("APP_SUBSCRIPTION_MIRRORS entry %q must be Label|https://host", entry)
		}
		parsed, err := url.Parse(target)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
			strings.Trim(parsed.Path, "/") != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("APP_SUBSCRIPTION_MIRRORS target %q must be an https origin with no path", target)
		}
		origin := "https://" + strings.ToLower(parsed.Host)
		if seen[origin] {
			continue
		}
		seen[origin] = true
		mirrors = append(mirrors, LinkMirror{Label: label, Origin: origin})
		if len(mirrors) > 100 {
			return nil, errors.New("APP_SUBSCRIPTION_MIRRORS lists more than 100 mirrors")
		}
	}
	return mirrors, nil
}

// MirrorLink is the subscription link served from a mirror's origin, or empty
// when the link cannot be rewritten or already lives there.
func (mirror LinkMirror) MirrorLink(subscriptionURL string) string {
	parsed, err := url.Parse(subscriptionURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	target, err := url.Parse(mirror.Origin)
	if err != nil || strings.EqualFold(parsed.Host, target.Host) {
		return ""
	}
	parsed.Scheme, parsed.Host = target.Scheme, target.Host
	return parsed.String()
}

// MirrorLinks lists every mirror's version of the link.
func (settings ConnectSettings) MirrorLinks(subscriptionURL string) []MirroredLink {
	links := make([]MirroredLink, 0, len(settings.Mirrors))
	for _, mirror := range settings.Mirrors {
		if link := mirror.MirrorLink(subscriptionURL); link != "" {
			links = append(links, MirroredLink{Label: mirror.Label, URL: link})
		}
	}
	return links
}

// MirroredLink is the subscription link on one mirror.
type MirroredLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// DeepLink builds the import link for one client, encrypted when the
// operator asked for it and the client supports it. Encryption that fails
// falls back to the plain link: a customer without a working button is worse
// off than one whose link is readable.
func (settings ConnectSettings) DeepLink(app ClientApp, subscriptionURL string) string {
	plain := app.DeepLink(subscriptionURL)
	if plain == "" {
		return ""
	}
	scheme := strings.ToLower(app.Scheme)
	switch {
	case settings.HappCryptoLinks && strings.HasPrefix(scheme, "happ://"):
		if encrypted, err := HappCrypt4(subscriptionURL); err == nil {
			return encrypted
		}
	case settings.IncyCryptoLinks && (strings.HasPrefix(scheme, "incy://import") || strings.HasPrefix(scheme, "incy://add")):
		if encrypted, err := IncyCrypt1(subscriptionURL, settings.IncyProvider); err == nil {
			return encrypted
		}
	}
	return plain
}

// HappLink is the one-tap Happ import for the Happ connect mode.
func (settings ConnectSettings) HappLink(subscriptionURL string) string {
	return settings.DeepLink(ClientApp{Name: "Happ", Scheme: "happ://add/"}, subscriptionURL)
}

// happPublicKey is Happ's crypt4 key: the same one Remnawave's own
// subscription page encrypts with (@kastov/cryptohapp, RSA PKCS#1 v1.5). It is
// public; only the Happ application holds the private half.
const happPublicKey = `-----BEGIN PUBLIC KEY-----
MIICIjANBgkqhkiG9w0BAQEFAAOCAg8AMIICCgKCAgEA3UZ0M3L4K+WjM3vkbQnz
ozHg/cRbEXvQ6i4A8RVN4OM3rK9kU01FdjyoIgywve8OEKsFnVwERZAQZ1Trv60B
hmaM76QQEE+EUlIOL9EpwKWGtTL5lYC1sT9XJMNP3/CI0gP5wwQI88cY/xedpOEB
W72EmOOShHUm/b/3m+HPmqwc4ugKj5zWV5SyiT829aFA5DxSjmIIFBAms7DafmSq
LFTYIQL5cShDY2u+/sqyAw9yZIOoqW2TFIgIHhLPWek/ocDU7zyOrlu1E0SmcQQb
LFqHq02fsnH6IcqTv3N5Adb/CkZDDQ6HvQVBmqbKZKf7ZdXkqsc/Zw27xhG7OfXC
tUmWsiL7zA+KoTd3avyOh93Q9ju4UQsHthL3Gs4vECYOCS9dsXXSHEY/1ngU/hjO
WFF8QEE/rYV6nA4PTyUvo5RsctSQL/9DJX7XNh3zngvif8LsCN2MPvx6X+zLouBX
zgBkQ9DFfZAGLWf9TR7KVjZC/3NsuUCDoAOcpmN8pENBbeB0puiKMMWSvll36+2M
YR1Xs0MgT8Y9TwhE2+TnnTJOhzmHi/BxiUlY/w2E0s4ax9GHAmX0wyF4zeV7kDkc
vHuEdc0d7vDmdw0oqCqWj0Xwq86HfORu6tm1A8uRATjb4SzjTKclKuoElVAVa5Jo
oh/uZMozC65SmDw+N5p6Su8CAwEAAQ==
-----END PUBLIC KEY-----`

// HappCrypt4 encrypts a subscription link into `happ://crypt4/<base64>`.
func HappCrypt4(subscriptionURL string) (string, error) {
	block, _ := pem.Decode([]byte(happPublicKey))
	if block == nil {
		return "", errors.New("the Happ key does not decode")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", err
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return "", errors.New("the Happ key is not RSA")
	}
	if len(subscriptionURL) > key.Size()-11 {
		return "", errors.New("the link is too long for crypt4")
	}
	sealed, err := rsa.EncryptPKCS1v15(rand.Reader, key, []byte(subscriptionURL))
	if err != nil {
		return "", err
	}
	return "happ://crypt4/" + base64.StdEncoding.EncodeToString(sealed), nil
}

// incyKey is K1 of @incy/link-encoder 1.3.0, shared by every INCY provider and
// built into the client — obfuscation against link scanners, as INCY's own
// documentation says, not a secret. incyKeyFingerprint is the package's
// published SHA-256 of it.
const (
	incyKey            = "9tQOoMioiZ18aC0Jug1BZd/is91F5rs+JcsjPPAMJGI="
	incyKeyFingerprint = "b6bf708471cc90043232967660aade86a50b4e57929db2e53c5fa34db624c08c"
)

// IncyCrypt1 encrypts a subscription link into `incy://crypt1/<base64url>`:
// AES-256-GCM over the compact sorted JSON `{"n":…,"url":…,"v":1}`, with the
// 12-byte nonce prepended to the sealed text.
func IncyCrypt1(subscriptionURL, provider string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(incyKey)
	if err != nil {
		return "", err
	}
	if sum := sha256.Sum256(key); hex.EncodeToString(sum[:]) != incyKeyFingerprint {
		return "", errors.New("the INCY key does not match its fingerprint")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := incyPayload(subscriptionURL, provider)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, plaintext, nil)
	return "incy://crypt1/" + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// incyPayload renders INCY's sortedCompactJson: keys in order, no spaces,
// non-ASCII left as it is.
func incyPayload(subscriptionURL, provider string) ([]byte, error) {
	encode := func(value any) (string, error) {
		builder := &strings.Builder{}
		encoder := json.NewEncoder(builder)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(value); err != nil {
			return "", err
		}
		return strings.TrimSuffix(builder.String(), "\n"), nil
	}
	parts := make([]string, 0, 3)
	if provider != "" {
		name, err := encode(provider)
		if err != nil {
			return nil, err
		}
		parts = append(parts, `"n":`+name)
	}
	link, err := encode(subscriptionURL)
	if err != nil {
		return nil, err
	}
	parts = append(parts, `"url":`+link, `"v":1`)
	return []byte("{" + strings.Join(parts, ",") + "}"), nil
}
