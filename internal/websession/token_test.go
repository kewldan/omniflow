package websession

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestNewTokenIsUniformURLSafeAndStoredOnlyAsItsDigest(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{}, 256)
	for range 256 {
		token, digest, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			t.Fatalf("token %q is not raw URL-safe base64: %v", token, err)
		}
		if len(raw) != TokenBytes {
			t.Fatalf("token carries %d bytes of entropy, want %d", len(raw), TokenBytes)
		}
		if !bytes.Equal(digest, HashToken(token)) {
			t.Fatal("the digest returned with a token is not the digest HashToken stores")
		}
		// The stored value must never be the token itself, or a database read
		// would hand out a working cookie.
		if bytes.Contains(digest, []byte(token)) || string(digest) == token {
			t.Fatal("the digest contains the token")
		}
		if _, dup := seen[token]; dup {
			t.Fatal("NewToken repeated a token")
		}
		seen[token] = struct{}{}
	}
}

func TestHashTokenIsDeterministicAndDistinguishesTokens(t *testing.T) {
	t.Parallel()
	first := HashToken("token-a")
	if len(first) != 32 {
		t.Fatalf("digest length = %d, want 32", len(first))
	}
	if !bytes.Equal(first, HashToken("token-a")) {
		t.Fatal("the same token hashed to two digests; a stored session could never resolve")
	}
	for _, other := range []string{"token-b", "token-a ", "TOKEN-A", ""} {
		if bytes.Equal(first, HashToken(other)) {
			t.Fatalf("%q collides with token-a", other)
		}
	}
}

func TestNewCSRFSecretIsFreshEachTime(t *testing.T) {
	t.Parallel()
	first, err := NewCSRFSecret()
	if err != nil {
		t.Fatalf("NewCSRFSecret: %v", err)
	}
	second, err := NewCSRFSecret()
	if err != nil {
		t.Fatalf("NewCSRFSecret: %v", err)
	}
	if len(first) != CSRFSecretBytes || len(second) != CSRFSecretBytes {
		t.Fatalf("secret lengths = %d, %d, want %d", len(first), len(second), CSRFSecretBytes)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two sessions received the same CSRF secret")
	}
}

func TestValidCSRFTokenRefusesEverythingButTheExactToken(t *testing.T) {
	t.Parallel()
	secret, err := NewCSRFSecret()
	if err != nil {
		t.Fatalf("NewCSRFSecret: %v", err)
	}
	otherSecret, err := NewCSRFSecret()
	if err != nil {
		t.Fatalf("NewCSRFSecret: %v", err)
	}
	const label = "omniflow.panel"
	token := CSRFToken(secret, label)
	if token != CSRFToken(secret, label) {
		t.Fatal("the CSRF token is not stable for one session")
	}

	cases := []struct {
		name      string
		secret    []byte
		label     string
		submitted string
		want      bool
	}{
		{"exact token", secret, label, token, true},
		{"empty submission", secret, label, "", false},
		{"no secret", nil, label, CSRFToken(nil, label), false},
		{"empty secret", []byte{}, label, CSRFToken([]byte{}, label), false},
		// A token minted for one session must not work in another.
		{"another session's token", secret, label, CSRFToken(otherSecret, label), false},
		// A token minted for the operator panel must not work on the customer
		// panel, even with the same secret.
		{"another surface's token", secret, "omniflow.account", token, false},
		{"truncated", secret, label, token[:len(token)-1], false},
		{"extended", secret, label, token + "A", false},
		{"case-folded", secret, label, swapCase(token), false},
		{"padded base64", secret, label, token + "=", false},
		{"the secret itself", secret, label, base64.RawURLEncoding.EncodeToString(secret), false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidCSRFToken(testCase.secret, testCase.label, testCase.submitted); got != testCase.want {
				t.Fatalf("ValidCSRFToken = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestCSRFTokenDoesNotRevealTheSecret(t *testing.T) {
	t.Parallel()
	secret := bytes.Repeat([]byte{0x41}, CSRFSecretBytes)
	token := CSRFToken(secret, "omniflow.panel")
	if bytes.Contains([]byte(token), secret[:8]) {
		t.Fatal("the CSRF token carries the secret in the clear")
	}
	if token == CSRFToken(secret, "omniflow.account") {
		t.Fatal("two surfaces derived the same token from one secret")
	}
}

func swapCase(value string) string {
	out := []byte(value)
	for index, character := range out {
		switch {
		case character >= 'a' && character <= 'z':
			out[index] = character - 'a' + 'A'
		case character >= 'A' && character <= 'Z':
			out[index] = character - 'A' + 'a'
		}
	}
	return string(out)
}
