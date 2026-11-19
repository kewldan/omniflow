package botapp

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// compactID renders a UUID as 22 base64url characters instead of 36, so a
// callback that carries one beside a provider name stays inside Telegram's
// 64-byte limit for callback data.
func compactID(id string) string {
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if err != nil || len(raw) != 16 {
		return id
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// expandCompactID reverses compactID. The value is customer-controlled, so a
// malformed one is reported rather than guessed at.
func expandCompactID(value string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 16 {
		return "", false
	}
	text := hex.EncodeToString(raw)
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:32], true
}
