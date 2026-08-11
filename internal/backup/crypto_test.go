package backup

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("could not generate a key: %v", err)
	}
	return key
}

func roundTrip(t *testing.T, key []byte, plaintext []byte) []byte {
	t.Helper()
	encrypted := &bytes.Buffer{}
	if err := Encrypt(encrypted, bytes.NewReader(plaintext), key); err != nil {
		t.Fatalf("encryption failed: %v", err)
	}
	return encrypted.Bytes()
}

func TestEncryptDecryptRoundTripsEveryShape(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	sizes := []int{0, 1, frameSize - 1, frameSize, frameSize + 1, 2*frameSize + 17}
	for _, size := range sizes {
		plaintext := make([]byte, size)
		if _, err := rand.Read(plaintext); err != nil {
			t.Fatalf("could not generate plaintext: %v", err)
		}
		encrypted := roundTrip(t, key, plaintext)
		decrypted := &bytes.Buffer{}
		if err := Decrypt(decrypted, bytes.NewReader(encrypted), key); err != nil {
			t.Fatalf("decryption failed for %d bytes: %v", size, err)
		}
		if !bytes.Equal(decrypted.Bytes(), plaintext) {
			t.Fatalf("round trip changed the payload at %d bytes", size)
		}
	}
}

func TestDecryptRejectsAnotherKey(t *testing.T) {
	t.Parallel()
	encrypted := roundTrip(t, testKey(t), []byte("pg_dump output"))
	if err := Decrypt(io.Discard, bytes.NewReader(encrypted), testKey(t)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected a corruption error, got %v", err)
	}
}

// A truncated backup must fail loudly. Silently restoring a partial dump is the
// worst possible outcome of a backup system.
func TestDecryptRejectsATruncatedFile(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	plaintext := make([]byte, 3*frameSize)
	if _, err := rand.Read(plaintext); err != nil {
		t.Fatalf("could not generate plaintext: %v", err)
	}
	encrypted := roundTrip(t, key, plaintext)
	truncated := encrypted[:len(encrypted)-1]
	if err := Decrypt(io.Discard, bytes.NewReader(truncated), key); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected a corruption error, got %v", err)
	}
	// Dropping the final frame entirely must fail too, not decode as a shorter
	// but valid backup.
	if err := Decrypt(io.Discard, bytes.NewReader(encrypted[:len(magic)+nonceSize]), key); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected a corruption error for a frameless file, got %v", err)
	}
}

func TestDecryptRejectsATamperedFrame(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	encrypted := roundTrip(t, key, bytes.Repeat([]byte("a"), 4096))
	tampered := append([]byte(nil), encrypted...)
	tampered[len(tampered)-1] ^= 0xff
	if err := Decrypt(io.Discard, bytes.NewReader(tampered), key); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected a corruption error, got %v", err)
	}
}

func TestDecryptRejectsAForeignFile(t *testing.T) {
	t.Parallel()
	if err := Decrypt(io.Discard, bytes.NewReader([]byte("PGDMP not an omniflow backup")), testKey(t)); !errors.Is(err, ErrBadFormat) {
		t.Fatalf("expected a format error, got %v", err)
	}
}

func TestEncryptRequiresA256BitKey(t *testing.T) {
	t.Parallel()
	if err := Encrypt(io.Discard, bytes.NewReader(nil), []byte("short")); err == nil {
		t.Fatal("a short key must be refused")
	}
}

// Two backups of identical content must not produce identical ciphertext: the
// per-file nonce is what keeps one leaked file from revealing another.
func TestEachBackupUsesItsOwnNonce(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	plaintext := []byte("identical content")
	first := roundTrip(t, key, plaintext)
	second := roundTrip(t, key, plaintext)
	if bytes.Equal(first, second) {
		t.Fatal("two encryptions of the same content must differ")
	}
}
