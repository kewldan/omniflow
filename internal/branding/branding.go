// Package branding turns an operator's colour choices into the stylesheet both
// panels render under.
//
// The whole point of this package is that an operator supplies values and those
// values end up inside a <style> element on a page that also holds a session
// cookie. Everything here exists to make that safe and to make it accessible:
//
//   - Token names are an allowlist. An operator names a slot from a fixed list;
//     they never supply a property name, because a property name is where
//     arbitrary CSS would get in.
//   - Values are hex colours and nothing else. Not a keyword, not a function,
//     not a url(). The parser accepts `#rgb` and `#rrggbb`, normalises to six
//     lowercase digits, and refuses everything else.
//   - Contrast is computed, not trusted. A palette whose body text is invisible
//     against its own background is refused, and one that merely fails WCAG AA
//     is saved with a warning that names the pair.
//
// The shipped defaults are duplicated here because contrast has to be evaluated
// against the palette a visitor actually sees, which is the operator's overrides
// layered onto the design's own values. `defaults_test.go` parses
// `packages/ui/src/styles/theme.css` and fails when the two drift, so the
// duplication cannot go stale quietly.
package branding

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The two palettes. They are the modes the design ships, and an installation
// chooses which of them it offers rather than inventing a third.
const (
	ModeLight = "light"
	ModeDark  = "dark"
)

// Themable is every token an operator may set, in the order a panel renders
// them: surfaces, then the brand tones, then the quiet ones, then charts.
//
// The semantic status tones — destructive, success, warning, info — are
// deliberately absent. Their meaning is fixed by what they communicate, and an
// installation whose "destructive" is green has not been branded, it has been
// broken. That boundary is worth more than the completeness of the list.
var Themable = []string{
	"background", "foreground",
	"card", "card-foreground",
	"popover", "popover-foreground",
	"primary", "primary-foreground",
	"secondary", "secondary-foreground",
	"accent", "accent-foreground",
	"muted", "muted-foreground",
	"subtle-foreground",
	"border", "input", "ring",
	"chrome", "chrome-border",
	"chart-1", "chart-2", "chart-3",
}

func themable(token string) bool {
	return slices.Contains(Themable, token)
}

// Radius scales, as multipliers over the design's own corner radii. A scale
// rather than a set of pixel values because the design uses six related radii
// and an operator choosing six numbers would be choosing a relationship they
// cannot see.
var radiusScales = map[string]float64{
	"square": 0, "compact": 0.6, "default": 1, "rounded": 1.6,
}

// Density scales, as multipliers over Tailwind's spacing base. The range is
// narrow on purpose: every padding, gap, and inset in both panels multiplies by
// this, so a value that looked reasonable in isolation reaches a table's cell
// padding and a sheet's inset at the same time.
var densityScales = map[string]float64{
	"compact": 0.88, "default": 1, "comfortable": 1.14,
}

// Theme is what an operator saved.
//
// A nil or absent map means "use the design's values", which is different from
// an empty one only in intent; both render nothing.
type Theme struct {
	Light map[string]string `json:"light,omitempty"`
	Dark  map[string]string `json:"dark,omitempty"`

	Radius  string `json:"radius,omitempty"`
	Density string `json:"density,omitempty"`

	// AllowedThemes bounds what a visitor may switch to. An installation that
	// offers one mode has no toggle at all rather than a toggle that does
	// nothing.
	AllowedThemes []string `json:"allowedThemes,omitempty"`
	// DefaultTheme is what somebody with no stored preference gets. "system"
	// follows the operating system and is only meaningful when both modes are
	// offered.
	DefaultTheme string `json:"defaultTheme,omitempty"`
}

// Warning is one thing an operator should know about their palette.
//
// The JSON tags are load-bearing: this crosses to a screen that reads `code`,
// `text`, and `blocking`, and a struct without them would render every warning
// as an untranslated key and read `blocking` as undefined — which is exactly
// how the AI settings screen shipped an inert guard for three phases.
type Warning struct {
	Code string `json:"code"`
	// Mode says which palette the warning is about, so an operator who broke
	// only their dark palette is not sent looking through both.
	Mode string `json:"mode"`
	// Foreground and Background name the pair, because "contrast is too low" is
	// not actionable and "muted-foreground on card is 2.1:1" is.
	Foreground string  `json:"foreground"`
	Background string  `json:"background"`
	Ratio      float64 `json:"ratio"`
	// Blocking separates "this fails an accessibility standard" from "nobody
	// can read this".
	Blocking bool `json:"blocking"`
}

// Warning codes.
const (
	// WarningUnreadable is a pair below 3:1. Large text at 3:1 is the loosest
	// threshold WCAG 2.2 defines for any text at all, so below it there is no
	// reading of the standard under which the pair is usable.
	WarningUnreadable = "pair_unreadable"
	// WarningBelowAA is a pair between 3:1 and 4.5:1: legible, and failing
	// WCAG 2.2 AA for body text.
	WarningBelowAA = "pair_below_aa"
)

// Contrast thresholds, named rather than inlined because the difference between
// them is the difference between a refusal and a warning.
const (
	unreadableRatio = 3.0
	passingRatio    = 4.5
)

// Pairs checked for contrast, and why each one.
//
// They are the pairs where one token is drawn on top of another in the shipped
// components. A pair nobody renders would produce a warning an operator cannot
// act on, which trains people to ignore the warnings that matter.
var contrastPairs = []struct{ Foreground, Background string }{
	{"foreground", "background"},
	{"card-foreground", "card"},
	{"popover-foreground", "popover"},
	{"primary-foreground", "primary"},
	{"secondary-foreground", "secondary"},
	{"accent-foreground", "accent"},
	{"muted-foreground", "muted"},
	// Quiet text is drawn on the page and on cards as well as on chips, and it
	// is the tone that failed the accessibility gate in v0.9. All three
	// surfaces are checked because all three are used.
	{"muted-foreground", "background"},
	{"muted-foreground", "card"},
	{"subtle-foreground", "background"},
	{"subtle-foreground", "card"},
}

var hexPattern = regexp.MustCompile(`^#([0-9a-f]{3}|[0-9a-f]{6})$`)

// ParseColour normalises an operator's value to six lowercase hex digits.
//
// It is the only way a value enters this package, and it accepts nothing that
// is not a colour. That is what lets CSS below concatenate without escaping:
// the output alphabet cannot contain a brace, a semicolon, or the sequence that
// would close a <style> element.
func ParseColour(value string) (string, error) {
	normalised := strings.ToLower(strings.TrimSpace(value))
	if !hexPattern.MatchString(normalised) {
		return "", fmt.Errorf("branding: %q is not a #rgb or #rrggbb colour", value)
	}
	if len(normalised) == 4 {
		return fmt.Sprintf(
			"#%c%c%c%c%c%c",
			normalised[1], normalised[1], normalised[2], normalised[2],
			normalised[3], normalised[3],
		), nil
	}
	return normalised, nil
}

// Normalise returns the theme with every value canonical and every unset field
// carrying its default, so a caller never has to distinguish "absent" from
// "default" again.
func (theme Theme) Normalise() (Theme, error) {
	normalised := Theme{
		Radius:       strings.TrimSpace(theme.Radius),
		Density:      strings.TrimSpace(theme.Density),
		DefaultTheme: strings.TrimSpace(theme.DefaultTheme),
	}
	if normalised.Radius == "" {
		normalised.Radius = "default"
	}
	if normalised.Density == "" {
		normalised.Density = "default"
	}
	if normalised.DefaultTheme == "" {
		normalised.DefaultTheme = "system"
	}
	if _, ok := radiusScales[normalised.Radius]; !ok {
		return Theme{}, fmt.Errorf("branding: unknown radius %q", theme.Radius)
	}
	if _, ok := densityScales[normalised.Density]; !ok {
		return Theme{}, fmt.Errorf("branding: unknown density %q", theme.Density)
	}

	allowed, err := normaliseModes(theme.AllowedThemes)
	if err != nil {
		return Theme{}, err
	}
	normalised.AllowedThemes = allowed

	switch normalised.DefaultTheme {
	case ModeLight, ModeDark:
		if !containsMode(allowed, normalised.DefaultTheme) {
			return Theme{}, fmt.Errorf(
				"branding: default theme %q is not offered", normalised.DefaultTheme,
			)
		}
	case "system":
		// Following the operating system when only one mode is offered would
		// mean the setting silently does nothing half the time. Collapsing it
		// to the one mode that exists is the honest reading of the same intent.
		if len(allowed) == 1 {
			normalised.DefaultTheme = allowed[0]
		}
	default:
		return Theme{}, fmt.Errorf("branding: unknown default theme %q", theme.DefaultTheme)
	}

	if normalised.Light, err = normalisePalette(theme.Light); err != nil {
		return Theme{}, err
	}
	if normalised.Dark, err = normalisePalette(theme.Dark); err != nil {
		return Theme{}, err
	}
	return normalised, nil
}

func normalisePalette(palette map[string]string) (map[string]string, error) {
	if len(palette) == 0 {
		return nil, nil
	}
	normalised := make(map[string]string, len(palette))
	for token, value := range palette {
		token = strings.TrimSpace(strings.ToLower(token))
		if !themable(token) {
			return nil, fmt.Errorf("branding: %q is not a themable token", token)
		}
		colour, err := ParseColour(value)
		if err != nil {
			return nil, err
		}
		normalised[token] = colour
	}
	return normalised, nil
}

func normaliseModes(modes []string) ([]string, error) {
	if len(modes) == 0 {
		return []string{ModeLight, ModeDark}, nil
	}
	seen := map[string]bool{}
	for _, mode := range modes {
		switch strings.TrimSpace(strings.ToLower(mode)) {
		case ModeLight:
			seen[ModeLight] = true
		case ModeDark:
			seen[ModeDark] = true
		default:
			return nil, fmt.Errorf("branding: unknown theme %q", mode)
		}
	}
	allowed := make([]string, 0, 2)
	// Light first, always, so the stored order cannot make two installations
	// with the same configuration compare unequal.
	if seen[ModeLight] {
		allowed = append(allowed, ModeLight)
	}
	if seen[ModeDark] {
		allowed = append(allowed, ModeDark)
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("branding: at least one theme must be offered")
	}
	return allowed, nil
}

func containsMode(modes []string, mode string) bool {
	return slices.Contains(modes, mode)
}

// Resolved returns the palette a visitor in the given mode actually sees: the
// design's values with the operator's overrides on top.
func (theme Theme) Resolved(mode string) map[string]string {
	base := Defaults[mode]
	resolved := make(map[string]string, len(base))
	maps.Copy(resolved, base)
	overrides := theme.Light
	if mode == ModeDark {
		overrides = theme.Dark
	}
	maps.Copy(resolved, overrides)
	return resolved
}

// Check reports every contrast problem the palette has, in a stable order.
//
// A caller refuses the save when any warning is blocking and stores it
// otherwise: a brand tone that fails AA is a decision an operator is entitled
// to make and be told about, while text nobody can read is not a decision.
func (theme Theme) Check() []Warning {
	warnings := make([]Warning, 0)
	for _, mode := range []string{ModeLight, ModeDark} {
		// A mode nobody is offered cannot be seen, so its palette is not worth
		// refusing a save over.
		if len(theme.AllowedThemes) > 0 && !containsMode(theme.AllowedThemes, mode) {
			continue
		}
		resolved := theme.Resolved(mode)
		for _, pair := range contrastPairs {
			ratio := Contrast(resolved[pair.Foreground], resolved[pair.Background])
			if ratio >= passingRatio {
				continue
			}
			warning := Warning{
				Code: WarningBelowAA, Mode: mode,
				Foreground: pair.Foreground, Background: pair.Background,
				Ratio: math.Round(ratio*100) / 100,
			}
			if ratio < unreadableRatio {
				warning.Code, warning.Blocking = WarningUnreadable, true
			}
			warnings = append(warnings, warning)
		}
	}
	return warnings
}

// Blocking reports whether any warning refuses the save.
func Blocking(warnings []Warning) bool {
	for _, warning := range warnings {
		if warning.Blocking {
			return true
		}
	}
	return false
}

// Contrast is the WCAG 2.x contrast ratio between two hex colours, from 1 to
// 21. An unparseable colour yields 0, which fails every threshold — the safe
// direction for a value that should never have got this far.
func Contrast(foreground, background string) float64 {
	first, ok := luminance(foreground)
	if !ok {
		return 0
	}
	second, ok := luminance(background)
	if !ok {
		return 0
	}
	lighter, darker := math.Max(first, second), math.Min(first, second)
	return (lighter + 0.05) / (darker + 0.05)
}

func luminance(colour string) (float64, bool) {
	normalised, err := ParseColour(colour)
	if err != nil {
		return 0, false
	}
	channel := func(offset int) float64 {
		value, _ := strconv.ParseUint(normalised[offset:offset+2], 16, 8)
		linear := float64(value) / 255
		if linear <= 0.04045 {
			return linear / 12.92
		}
		return math.Pow((linear+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(1) + 0.7152*channel(3) + 0.0722*channel(5), true
}

// CSS renders the overrides as a stylesheet the panels inline in their head.
//
// Only what the operator changed is emitted. An installation that has saved
// nothing gets an empty string and no <style> element at all, which is what
// keeps the shipped design the shipped design rather than a copy of itself
// re-declared at lower specificity.
func (theme Theme) CSS() string {
	var builder strings.Builder

	root := declarations(theme.Light)
	if scale, ok := radiusScales[theme.Radius]; ok && theme.Radius != "default" {
		root = append(root, fmt.Sprintf("--radius-scale:%s", number(scale)))
	}
	if scale, ok := densityScales[theme.Density]; ok && theme.Density != "default" {
		root = append(root, fmt.Sprintf("--density-scale:%s", number(scale)))
	}
	if len(root) > 0 {
		builder.WriteString(":root{")
		builder.WriteString(strings.Join(root, ";"))
		builder.WriteString("}")
	}

	if dark := declarations(theme.Dark); len(dark) > 0 {
		builder.WriteString(".dark{")
		builder.WriteString(strings.Join(dark, ";"))
		builder.WriteString("}")
	}
	return builder.String()
}

func declarations(palette map[string]string) []string {
	if len(palette) == 0 {
		return nil
	}
	tokens := make([]string, 0, len(palette))
	for token := range palette {
		// Belt and braces over Normalise: this is the function whose output
		// lands in a page, so it refuses to emit a name it does not recognise
		// even if one somehow reached the map.
		if themable(token) {
			tokens = append(tokens, token)
		}
	}
	slices.Sort(tokens)

	declared := make([]string, 0, len(tokens))
	for _, token := range tokens {
		colour, err := ParseColour(palette[token])
		if err != nil {
			continue
		}
		declared = append(declared, "--"+token+":"+colour)
	}
	return declared
}

// number formats a scale without a trailing zero, because `--radius-scale:0.6`
// reads as a choice and `0.600000` reads as a bug.
func number(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
