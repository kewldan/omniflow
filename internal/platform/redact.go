package platform

import (
	"net/url"
	"regexp"
	"strings"
)

// Redacted is what replaces any value that must never reach a log line, an
// operator notification, or a diagnostics bundle.
const Redacted = "[redacted]"

// secretPatterns match the values Omniflow must never print. They are ordered
// from most specific to least so a subscription link is redacted as a link
// rather than partially rewritten as a bare token.
var secretPatterns = []*regexp.Regexp{
	// Telegram bot tokens: <digits>:<35 or more base64url characters>.
	regexp.MustCompile(`\b\d{6,12}:[A-Za-z0-9_-]{30,}`),
	// Bearer, Basic, and API-key style headers.
	regexp.MustCompile(`(?i)\b(bearer|basic|token|api[_-]?key|secret|password|passwd|authorization)\b\s*[:=]?\s*["']?[A-Za-z0-9._\-+/=]{8,}["']?`),
	// Provider references and card-like digit runs.
	regexp.MustCompile(`\b\d{13,19}\b`),
	// HWIDs and other long opaque identifiers in key=value form.
	regexp.MustCompile(`(?i)\b(hwid|device[_-]?id|charge[_-]?id|invoice[_-]?payload)\b\s*[:=]?\s*["']?[A-Za-z0-9._\-]{6,}["']?`),
}

// linkSchemes are the URL schemes that carry subscription access. A value using
// one of them is replaced entirely rather than truncated, because even the host
// identifies the installation a customer connects to.
var linkSchemes = regexp.MustCompile(`(?i)\b(https?|vless|vmess|trojan|ss)://[^\s"'<>]+`)

// Redact removes secrets from free text. It is deliberately aggressive: an
// over-redacted log line is a minor inconvenience, while a leaked subscription
// link or bot token is a security incident.
func Redact(value string) string {
	if value == "" {
		return value
	}
	redacted := linkSchemes.ReplaceAllString(value, Redacted)
	for _, pattern := range secretPatterns {
		redacted = pattern.ReplaceAllString(redacted, Redacted)
	}
	return redacted
}

// RedactURL keeps a URL's scheme and host but removes the path, query, and any
// embedded credentials. It is what an operator notification shows when it has to
// name an endpoint at all.
func RedactURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return Redacted
	}
	return parsed.Scheme + "://" + parsed.Host + "/" + Redacted
}

// RedactFields returns a copy of a structured payload with every value whose key
// looks sensitive replaced. Keys are matched case-insensitively.
func RedactFields(fields map[string]any) map[string]any {
	if fields == nil {
		return nil
	}
	safe := make(map[string]any, len(fields))
	for key, value := range fields {
		if sensitiveKey(key) {
			safe[key] = Redacted
			continue
		}
		switch typed := value.(type) {
		case string:
			safe[key] = Redact(typed)
		case map[string]any:
			safe[key] = RedactFields(typed)
		default:
			safe[key] = value
		}
	}
	return safe
}

var sensitiveKeyPattern = regexp.MustCompile(`(?i)(token|secret|password|passwd|authorization|signature|api[_-]?key|hwid|subscription[_-]?url|checkout[_-]?url|raw[_-]?body|payload|card|pan|cvv|iban|email|phone)`)

func sensitiveKey(key string) bool { return sensitiveKeyPattern.MatchString(key) }
