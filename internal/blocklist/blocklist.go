// Package blocklist normalises customer identifiers, fingerprints them, and
// parses the lists an operator subscribes to.
//
// Two decisions shape everything here.
//
// Omniflow never stores a readable copy of somebody else's list. An entry is
// kept as the SHA-256 of its normalised value, salted by the kind it
// identifies. That is enough for an exact-match lookup — which is the only kind
// of lookup a blocklist needs — and not enough to reconstruct the list, publish
// it onward, or correlate it with another installation's.
//
// A match is evidence, not a verdict. This package answers "does this
// identifier appear on a list an operator subscribed to". It has no opinion
// about what should happen next, and nothing in it can suspend an account. The
// panel shows the match, an operator decides, and their decision and reason go
// into the audit trail.
package blocklist

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

// Subject kinds match the database check constraint on
// `blocklist_sources.subject_kind`.
const (
	SubjectTelegramID = "telegram_id"
	SubjectEmail      = "email"
	SubjectUsername   = "username"
)

// Decisions an operator may record against a match.
const (
	DecisionAllowed = "allowed"
	DecisionBlocked = "blocked"
)

var (
	// ErrUnknownSubject reports a kind outside the supported set.
	ErrUnknownSubject = errors.New("unknown blocklist subject kind")
	// ErrEmptyValue reports an identifier that normalises to nothing.
	ErrEmptyValue = errors.New("blocklist value is empty")
	// ErrTooManyEntries reports a list larger than the configured ceiling.
	ErrTooManyEntries = errors.New("blocklist source exceeds the entry limit")
)

// MaxEntries bounds a single refresh.
//
// A source that suddenly publishes millions of rows is far more likely to be
// misconfigured, redirected, or compromised than genuinely that large, and
// importing it wholesale would replace a working list with garbage inside one
// transaction.
const MaxEntries = 500_000

// Normalize reduces an identifier to the one form a fingerprint is taken over.
//
// Normalisation is per kind because the equivalences differ: Telegram
// identifiers are decimal integers, usernames are case-insensitive and may
// arrive with a leading @ or as a t.me link, and addresses are case-insensitive
// in the domain. The local part of an address is deliberately left alone —
// lowercasing it or stripping dots is a provider-specific convention, and
// applying it universally would make two genuinely different addresses collide.
func Normalize(kind, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrEmptyValue
	}

	switch kind {
	case SubjectTelegramID:
		// Accept whatever a list publishes and keep the canonical decimal form,
		// so "0012345" and "12345" cannot become two entries.
		parsed, err := strconv.ParseInt(strings.TrimPrefix(value, "#"), 10, 64)
		if err != nil || parsed <= 0 {
			return "", ErrEmptyValue
		}
		return strconv.FormatInt(parsed, 10), nil

	case SubjectUsername:
		value = strings.TrimPrefix(value, "https://")
		value = strings.TrimPrefix(value, "t.me/")
		value = strings.TrimPrefix(value, "@")
		value = strings.ToLower(value)
		if value == "" {
			return "", ErrEmptyValue
		}
		return value, nil

	case SubjectEmail:
		at := strings.LastIndex(value, "@")
		if at <= 0 || at == len(value)-1 {
			return "", ErrEmptyValue
		}
		return value[:at] + "@" + strings.ToLower(value[at+1:]), nil

	default:
		return "", ErrUnknownSubject
	}
}

// Fingerprint returns the digest stored for an identifier.
//
// The kind is part of the pre-image, so a username that happens to read like a
// numeric identifier cannot match an entry on a Telegram-ID list.
func Fingerprint(kind, value string) ([]byte, error) {
	normalized, err := Normalize(kind, value)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(kind + ":" + normalized))
	return digest[:], nil
}

// Entry is one imported list row.
type Entry struct {
	Fingerprint []byte
	// ReasonCode is the publisher's own classification, kept as an opaque short
	// token so the panel can show why a list flagged something. It is never
	// interpreted, and a list that supplies none simply has an empty one.
	ReasonCode string
}

// Parse reads a refreshed source body into entries.
//
// Two shapes are accepted because they are the two shapes list publishers
// actually use: a JSON array of objects or bare strings, and a plain-text file
// of one value per line with `#` comments. Deciding between them by the first
// non-space byte rather than by a declared content type means a source served
// with a careless `text/plain` still imports correctly.
//
// A value that does not normalise is skipped rather than failing the refresh:
// one malformed line in a third-party list must not leave an operator with a
// stale blocklist and no explanation.
func Parse(kind string, body io.Reader) ([]Entry, int, error) {
	if _, err := Normalize(kind, "probe"); err != nil && errors.Is(err, ErrUnknownSubject) {
		return nil, 0, ErrUnknownSubject
	}

	buffered := bufio.NewReader(body)
	leading, err := peekFirstToken(buffered)
	if err != nil {
		return nil, 0, err
	}
	if leading == '[' {
		return parseJSON(kind, buffered)
	}
	return parseLines(kind, buffered)
}

// peekFirstToken returns the first non-whitespace byte without consuming it. An
// empty body reports zero, which the line parser handles as "no entries".
func peekFirstToken(reader *bufio.Reader) (byte, error) {
	for offset := 1; offset <= 512; offset++ {
		window, err := reader.Peek(offset)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, nil
			}
			return 0, err
		}
		candidate := window[offset-1]
		if candidate != ' ' && candidate != '\t' && candidate != '\n' && candidate != '\r' {
			return candidate, nil
		}
	}
	return 0, nil
}

// jsonEntry accepts both shapes a JSON list uses: an object naming the value
// and an optional reason, or a bare string.
type jsonEntry struct {
	Value      string `json:"value"`
	ID         string `json:"id"`
	Username   string `json:"username"`
	Email      string `json:"email"`
	ReasonCode string `json:"reason"`
}

func parseJSON(kind string, body io.Reader) ([]Entry, int, error) {
	var raw []json.RawMessage
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return nil, 0, err
	}
	if len(raw) > MaxEntries {
		return nil, 0, ErrTooManyEntries
	}

	entries := make([]Entry, 0, len(raw))
	skipped := 0
	for _, item := range raw {
		value, reason := "", ""
		var bare string
		if err := json.Unmarshal(item, &bare); err == nil {
			value = bare
		} else {
			var object jsonEntry
			if err := json.Unmarshal(item, &object); err != nil {
				skipped++
				continue
			}
			value = firstNonEmpty(object.Value, object.ID, object.Username, object.Email)
			reason = object.ReasonCode
		}

		fingerprint, err := Fingerprint(kind, value)
		if err != nil {
			skipped++
			continue
		}
		entries = append(entries, Entry{Fingerprint: fingerprint, ReasonCode: truncateReason(reason)})
	}
	return entries, skipped, nil
}

func parseLines(kind string, body io.Reader) ([]Entry, int, error) {
	scanner := bufio.NewScanner(body)
	// Long enough for any legitimate identifier plus a reason, short enough that
	// a source streaming one enormous line cannot exhaust memory.
	scanner.Buffer(make([]byte, 0, 4096), 64*1024)

	entries := make([]Entry, 0, 256)
	skipped := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		value, reason := line, ""
		// "value<TAB>reason" and "value,reason" are both common; anything after
		// the first separator is the publisher's own classification.
		if index := strings.IndexAny(line, "\t,;"); index > 0 {
			value, reason = strings.TrimSpace(line[:index]), strings.TrimSpace(line[index+1:])
		}

		fingerprint, err := Fingerprint(kind, value)
		if err != nil {
			skipped++
			continue
		}
		entries = append(entries, Entry{Fingerprint: fingerprint, ReasonCode: truncateReason(reason)})
		if len(entries) > MaxEntries {
			return nil, 0, ErrTooManyEntries
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	return entries, skipped, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// truncateReason bounds the publisher's classification to what the column
// accepts, so a source with a verbose reason field cannot fail the whole
// refresh on a length check.
func truncateReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > 64 {
		return reason[:64]
	}
	return reason
}
