package branding

// Defaults is the design's own value for every themable token, per mode.
//
// It exists so contrast can be evaluated against what a visitor sees rather
// than against the handful of tokens an operator happened to override: somebody
// who sets only `--card` has changed the contrast of every foreground drawn on
// a card, and a check that saw only their one value would report nothing.
//
// These are the values in `packages/ui/src/styles/theme.css`, duplicated
// because Go cannot read a stylesheet at compile time and this check runs in
// the API process. `defaults_test.go` parses that file and fails when the two
// disagree, which is what makes the duplication safe rather than merely small.
var Defaults = map[string]map[string]string{
	ModeLight: {
		"background":         "#fafafa",
		"foreground":         "#09090b",
		"card":               "#ffffff",
		"card-foreground":    "#09090b",
		"popover":            "#ffffff",
		"popover-foreground": "#09090b",
		"primary":            "#18181b",
		"primary-foreground": "#fafafa",

		"secondary":            "#f4f4f5",
		"secondary-foreground": "#18181b",
		"accent":               "#f4f4f5",
		"accent-foreground":    "#18181b",
		"muted":                "#f4f4f5",
		"muted-foreground":     "#52525b",
		"subtle-foreground":    "#6b6b75",

		"border":        "#e4e4e7",
		"input":         "#e4e4e7",
		"ring":          "#18181b",
		"chrome":        "#f1f1ef",
		"chrome-border": "#e4e4e7",

		"chart-1": "#2a78d6",
		"chart-2": "#eb6834",
		"chart-3": "#1baf7a",
	},
	ModeDark: {
		"background":         "#09090b",
		"foreground":         "#fafafa",
		"card":               "#18181b",
		"card-foreground":    "#fafafa",
		"popover":            "#18181b",
		"popover-foreground": "#fafafa",
		"primary":            "#fafafa",
		"primary-foreground": "#18181b",

		"secondary":            "#27272a",
		"secondary-foreground": "#fafafa",
		"accent":               "#27272a",
		"accent-foreground":    "#fafafa",
		"muted":                "#27272a",
		"muted-foreground":     "#a1a1aa",
		"subtle-foreground":    "#94949e",

		"border":        "#27272a",
		"input":         "#27272a",
		"ring":          "#d4d4d8",
		"chrome":        "#17181a",
		"chrome-border": "#242427",

		"chart-1": "#3987e5",
		"chart-2": "#d95926",
		"chart-3": "#199e70",
	},
}
