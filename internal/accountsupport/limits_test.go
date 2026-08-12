package accountsupport

import (
	"errors"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/accountpg"
)

// An allowlist is only an allowlist if the answer for an unlisted type is "no".
// These are the cases where a blocklist, or a sniffing fallback, would say yes.
func TestAcceptRefusesAnythingNotOnTheAllowlist(t *testing.T) {
	limits := DefaultLimits()
	cases := []struct{ name, declared string }{
		{"an executable", "application/x-msdownload"},
		{"a script", "application/javascript"},
		{"markup that a browser would render", "text/html"},
		{"an unlabelled upload", ""},
		{"an unparsable type", "not a media type at all"},
		{"the generic fallback a sniffer would produce", "application/octet-stream"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := limits.Accept("report.bin", testCase.declared, 1024)
			if !errors.Is(err, ErrAttachmentMediaType) {
				t.Fatalf("accepted %q with %v", testCase.declared, err)
			}
		})
	}
}

// Parameters and capitalisation are transport detail, not content. Refusing over
// either would be a rule about how a browser writes a header.
func TestAcceptNormalisesTheDeclaredType(t *testing.T) {
	limits := DefaultLimits()
	accepted, err := limits.Accept("notes.txt", "TEXT/Plain; charset=UTF-8", 12)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.MediaType != "text/plain" {
		t.Fatalf("stored media type %q", accepted.MediaType)
	}
	if accepted.Kind != "document" {
		t.Fatalf("text is a %q", accepted.Kind)
	}
}

// The kind column has two values and is derived, never supplied, so it cannot
// disagree with the media type it was derived from.
func TestAcceptDerivesTheAttachmentKind(t *testing.T) {
	limits := DefaultLimits()
	accepted, err := limits.Accept("screenshot.png", "image/png", 2048)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.Kind != "photo" {
		t.Fatalf("an image is a %q", accepted.Kind)
	}
}

func TestAcceptEnforcesTheSizeLimit(t *testing.T) {
	limits := Limits{
		MaxAttachmentBytes: 1024,
		AllowedMediaTypes:  []string{"image/png"},
		MaxOpenTickets:     3,
	}
	if _, err := limits.Accept("big.png", "image/png", 1025); !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("an oversized file was accepted with %v", err)
	}
	if _, err := limits.Accept("exact.png", "image/png", 1024); err != nil {
		t.Fatalf("a file exactly at the limit was refused: %v", err)
	}
	// An empty upload is a customer mistake rather than a policy violation, so it
	// reports as invalid input and the panel can say "choose a file".
	_, err := limits.Accept("empty.png", "image/png", 0)
	if !errors.Is(err, accountpg.ErrInvalidInput) {
		t.Fatalf("an empty file reported %v", err)
	}
}

// A configured limit above what the column accepts would let an upload finish
// and then fail at the insert, which is the worst moment to discover it.
func TestNewClampsTheLimitToWhatTheSchemaStores(t *testing.T) {
	limits := Limits{
		MaxAttachmentBytes: 500 << 20,
		AllowedMediaTypes:  []string{"image/png"},
		MaxOpenTickets:     2,
	}
	service := &Service{limits: limits}
	if _, err := service.limits.Accept("huge.png", "image/png", schemaMaxAttachmentBytes+1); err == nil {
		t.Fatal("a file above the column limit was accepted")
	}
}

// The file name is display text from somebody else's machine. It must not be
// able to choose a path, and it must not be able to add a header parameter.
func TestSanitizeFileNameStripsPathsAndHeaderCharacters(t *testing.T) {
	cases := []struct{ input, want string }{
		{`../../etc/passwd`, "passwd"},
		{`C:\Users\someone\report.pdf`, "report.pdf"},
		{`in"jection.png`, "injection.png"},
		{"line\r\nbreak.txt", "linebreak.txt"},
		{"   ", ""},
		{"/", ""},
	}
	for _, testCase := range cases {
		if got := SanitizeFileName(testCase.input); got != testCase.want {
			t.Fatalf("SanitizeFileName(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
	if got := SanitizeFileName(strings.Repeat("ф", 400)); len([]rune(got)) != 200 {
		t.Fatalf("a long name kept %d characters", len([]rune(got)))
	}
}
