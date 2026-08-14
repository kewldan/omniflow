package branding

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The stylesheet Defaults claims to mirror. Read at test time rather than
// embedded, because embedding it would make this test pass against a copy.
const stylesheet = "../../packages/ui/src/styles/theme.css"

var declaration = regexp.MustCompile(`--([a-z0-9-]+):\s*(#[0-9a-fA-F]{3,8})\s*;`)

// TestDefaultsMatchTheStylesheet is the guard that makes duplicating the
// palette in Go safe.
//
// Contrast has to be computed against what a visitor sees, so the API needs the
// design's own values; Go cannot read a stylesheet at compile time, so they are
// written out in defaults.go. That is a second source of truth, and a second
// source of truth is only acceptable when something fails when the two drift.
// This is that something.
func TestDefaultsMatchTheStylesheet(t *testing.T) {
	light, dark := parseStylesheet(t)

	for mode, parsed := range map[string]map[string]string{ModeLight: light, ModeDark: dark} {
		for _, token := range Themable {
			want, declared := parsed[token]
			if !declared {
				t.Errorf(
					"%s: --%s is in Themable but is not declared in %s; either the "+
						"stylesheet dropped it or it was never a real token",
					mode, token, filepath.Base(stylesheet),
				)
				continue
			}
			if got := Defaults[mode][token]; !strings.EqualFold(got, want) {
				t.Errorf(
					"%s --%s: Defaults says %s, the stylesheet says %s. The palette "+
						"moved and defaults.go did not follow, so every contrast "+
						"check is now being run against a design nobody ships.",
					mode, token, got, want,
				)
			}
		}
	}
}

// TestShippedPaletteClearsAA asserts the design Omniflow ships passes its own
// gate, so an operator who overrides one token is not handed a warning about a
// pair they never touched.
func TestShippedPaletteClearsAA(t *testing.T) {
	shipped, err := Theme{}.Normalise()
	if err != nil {
		t.Fatalf("normalising an empty theme failed: %v", err)
	}
	for _, warning := range shipped.Check() {
		t.Errorf(
			"the shipped palette fails its own contrast check: %s on %s in %s is %.2f:1",
			warning.Foreground, warning.Background, warning.Mode, warning.Ratio,
		)
	}
}

// parseStylesheet pulls the `:root` and `.dark` blocks out of the design's own
// file. It is deliberately naive: the stylesheet is hand-written and small, and
// a CSS parser here would be a dependency that can disagree with the browser.
func parseStylesheet(t *testing.T) (light, dark map[string]string) {
	t.Helper()
	source, err := os.ReadFile(stylesheet)
	if err != nil {
		t.Fatalf("reading %s: %v", stylesheet, err)
	}
	text := string(source)

	block := func(selector string) map[string]string {
		start := strings.Index(text, selector+" {")
		if start < 0 {
			t.Fatalf("%s has no %s block", stylesheet, selector)
		}
		end := strings.Index(text[start:], "\n}")
		if end < 0 {
			t.Fatalf("%s block in %s is not closed", selector, stylesheet)
		}
		values := map[string]string{}
		for _, match := range declaration.FindAllStringSubmatch(text[start:start+end], -1) {
			values[match[1]] = strings.ToLower(match[2])
		}
		return values
	}
	return block(":root"), block(".dark")
}
