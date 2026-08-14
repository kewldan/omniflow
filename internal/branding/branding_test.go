package branding

import (
	"math"
	"strings"
	"testing"
)

func TestParseColourAcceptsOnlyColours(t *testing.T) {
	for input, want := range map[string]string{
		"#FFF":     "#ffffff",
		"#0a0":     "#00aa00",
		" #1B2C3D": "#1b2c3d",
	} {
		got, err := ParseColour(input)
		if err != nil {
			t.Errorf("ParseColour(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("ParseColour(%q) = %q, want %q", input, got, want)
		}
	}

	// Everything below is a value somebody could reasonably type into a colour
	// field, and every one of them would be a way to put something other than a
	// colour into a <style> element on a page that holds a session cookie.
	for _, input := range []string{
		"", "red", "rgb(1,2,3)", "#12345", "#gggggg", "var(--primary)",
		"url(https://example.test/x.png)", "#fff;}body{display:none",
		"#fff</style><script>alert(1)</script>",
	} {
		if _, err := ParseColour(input); err == nil {
			t.Errorf("ParseColour(%q) was accepted; it is not a colour", input)
		}
	}
}

func TestNormaliseRefusesWhatItCannotRender(t *testing.T) {
	for name, theme := range map[string]Theme{
		"unknown token":   {Light: map[string]string{"brand-colour": "#123456"}},
		"unknown radius":  {Radius: "pill"},
		"unknown density": {Density: "airy"},
		"unknown mode":    {AllowedThemes: []string{"sepia"}},
		"default not on offer": {
			AllowedThemes: []string{ModeLight}, DefaultTheme: ModeDark,
		},
		// A property name is the one thing an operator never supplies, because
		// a property name is where arbitrary CSS would get in.
		"token smuggling a declaration": {
			Light: map[string]string{"primary:red;--background": "#000000"},
		},
	} {
		if _, err := theme.Normalise(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A single offered mode collapses "system" to that mode rather than leaving a
// preference that half the visitors would find does nothing.
func TestSystemDefaultCollapsesToTheOnlyOfferedMode(t *testing.T) {
	theme, err := Theme{AllowedThemes: []string{ModeDark}, DefaultTheme: "system"}.Normalise()
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if theme.DefaultTheme != ModeDark {
		t.Fatalf("default theme is %q, want %q", theme.DefaultTheme, ModeDark)
	}
}

func TestContrastMatchesTheStandard(t *testing.T) {
	// The two reference points every WCAG implementation is checked against.
	if got := Contrast("#000000", "#ffffff"); math.Abs(got-21) > 0.01 {
		t.Errorf("black on white is %.2f:1, want 21:1", got)
	}
	if got := Contrast("#808080", "#808080"); math.Abs(got-1) > 0.01 {
		t.Errorf("a colour on itself is %.2f:1, want 1:1", got)
	}
}

// The refusal is narrow, and the narrowness is the point: an operator may ship
// a brand tone that fails AA and be told, and may not ship text nobody can read.
func TestCheckSeparatesUnreadableFromMerelyFailing(t *testing.T) {
	unreadable, err := Theme{
		// Near-white body text on the near-white page the design ships.
		Light: map[string]string{"foreground": "#f0f0f0"},
	}.Normalise()
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	warnings := unreadable.Check()
	if !Blocking(warnings) {
		t.Fatalf("body text at %v against its own background did not block", warnings)
	}

	failing, err := Theme{
		// 3.4:1 on the shipped page colour: legible, and short of AA.
		Light: map[string]string{"foreground": "#8a8a8a"},
	}.Normalise()
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	warnings = failing.Check()
	if len(warnings) == 0 {
		t.Fatal("a pair below 4.5:1 produced no warning at all")
	}
	if Blocking(warnings) {
		t.Fatalf("a legible pair below AA blocked the save: %v", warnings)
	}
	if warnings[0].Foreground != "foreground" || warnings[0].Background != "background" {
		t.Errorf("the warning names %s on %s, which is not the pair that changed",
			warnings[0].Foreground, warnings[0].Background)
	}
}

// A mode nobody is offered cannot be seen, so its palette must not refuse a
// save — otherwise a light-only installation is held to a dark palette it never
// renders.
func TestAnUnofferedModeIsNotChecked(t *testing.T) {
	theme, err := Theme{
		AllowedThemes: []string{ModeLight},
		Dark:          map[string]string{"foreground": "#0a0a0a"},
	}.Normalise()
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	for _, warning := range theme.Check() {
		if warning.Mode == ModeDark {
			t.Errorf("the dark palette was checked although it is not offered: %v", warning)
		}
	}
}

func TestCSSEmitsOnlyWhatChanged(t *testing.T) {
	unchanged, _ := Theme{}.Normalise()
	if css := unchanged.CSS(); css != "" {
		t.Errorf("an unconfigured installation emitted %q; it should ship the "+
			"design rather than a copy of it re-declared", css)
	}

	theme, err := Theme{
		Light:  map[string]string{"primary": "#123456"},
		Dark:   map[string]string{"primary": "#abcdef"},
		Radius: "square",
	}.Normalise()
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	css := theme.CSS()
	for _, want := range []string{
		":root{--primary:#123456;--radius-scale:0}",
		".dark{--primary:#abcdef}",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("CSS %q does not contain %q", css, want)
		}
	}
	if strings.Contains(css, "--background") {
		t.Errorf("CSS %q re-declares a token the operator never set", css)
	}
}
