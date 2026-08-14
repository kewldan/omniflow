package infopage

import (
	"strings"
	"testing"
)

func TestParseProducesBlocks(t *testing.T) {
	document := Parse(`## Terms of service

These terms apply to everybody.
They continue on a second line.

- One thing
- Another thing

1. First step
2. Second step

Read the [privacy policy](https://example.test/privacy) as well.`)

	kinds := make([]string, 0, len(document.Blocks))
	for _, block := range document.Blocks {
		kinds = append(kinds, block.Kind)
	}
	want := []string{
		BlockHeading, BlockParagraph, BlockList, BlockList, BlockParagraph,
	}
	if len(kinds) != len(want) {
		t.Fatalf("blocks are %v, want %v", kinds, want)
	}
	for index, kind := range want {
		if kinds[index] != kind {
			t.Fatalf("blocks are %v, want %v", kinds, want)
		}
	}

	// Two consecutive lines are one paragraph: a document written with a hard
	// wrap must not render as a line per source line.
	if got := flatten(document.Blocks[1].Spans); got != "These terms apply to everybody. They continue on a second line." {
		t.Errorf("paragraph is %q", got)
	}
	// A bulleted list and a numbered list are separate blocks, because they read
	// differently and a document that used both meant to.
	if document.Blocks[2].Ordered || !document.Blocks[3].Ordered {
		t.Errorf("list ordering is %v then %v", document.Blocks[2].Ordered, document.Blocks[3].Ordered)
	}
	if len(document.Blocks[3].Items) != 2 {
		t.Errorf("the numbered list has %d items", len(document.Blocks[3].Items))
	}

	// The link is a span with an href, and the text around it survives.
	final := document.Blocks[4].Spans
	if len(final) != 3 || final[1].Href != "https://example.test/privacy" {
		t.Fatalf("link spans are %+v", final)
	}
	if final[1].Text != "privacy policy" {
		t.Errorf("link text is %q", final[1].Text)
	}
}

// This is the test the whole package exists for. Operator text is served from
// the origin that holds the session cookie, and nothing an operator types may
// become markup or a script-bearing link.
func TestNothingBecomesMarkupOrAnUnsafeLink(t *testing.T) {
	document := Parse(strings.Join([]string{
		`<script>alert(1)</script>`,
		``,
		`## <img src=x onerror=alert(1)>`,
		``,
		`[click](javascript:alert(1))`,
		`[click](http://insecure.test)`,
		`[click](https://example.test" onmouseover="alert(1))`,
		`[click](java` + "\t" + `script://x)`,
	}, "\n"))

	for _, block := range document.Blocks {
		for _, span := range append(block.Spans, flattenItems(block.Items)...) {
			// Angle brackets survive as text — that is correct and safe,
			// because the browser renders a text node. What must never happen
			// is a span carrying a link target that is not https.
			if span.Href != "" && !strings.HasPrefix(span.Href, "https://") {
				t.Errorf("a non-https target reached a span: %q", span.Href)
			}
			if strings.ContainsAny(span.Href, `"'<> `) {
				t.Errorf("a link target carries a character that could break out: %q", span.Href)
			}
		}
	}

	// Nothing vanishes. A link the syntax does not accept stays as the literal
	// text the operator typed — `[click](javascript:alert(1))`, brackets and
	// all — which looks wrong on the page and is exactly what should happen:
	// visible text is a text node, and the operator can see what to fix.
	//
	// The safety property is the one asserted above — no span carries a target
	// that is not https — and not the absence of a string. `javascript:` still
	// appears in the output here, as characters somebody typed, in the same way
	// the word "script" does.
	rendered := PlainText(document)
	if !strings.Contains(rendered, "[click](javascript:alert(1))") {
		t.Errorf("a refused link lost its text instead of staying literal: %q", rendered)
	}
	if strings.Count(rendered, "click") != 4 {
		t.Errorf("a refused link lost its text: %q", rendered)
	}
}

// A malformed document must render rather than fail. An operator with a
// published page they cannot see and no line number is worse off than one with
// a paragraph that reads oddly.
func TestParseNeverRefuses(t *testing.T) {
	for _, source := range []string{
		"", "   ", "\n\n\n", "## ", "- ", "1. ",
		"[unclosed](https://example.test", "[](https://example.test)",
		strings.Repeat("word ", 5000),
	} {
		document := Parse(source)
		for _, block := range document.Blocks {
			if block.Kind == "" {
				t.Errorf("a block with no kind came out of %q", source[:min(len(source), 20)])
			}
		}
	}
}

func TestPlainTextKeepsLinkTargets(t *testing.T) {
	// The bot cannot render a block tree, and a customer who can read the terms
	// in a browser and not in the chat has one document that exists twice.
	rendered := PlainText(Parse("See [our policy](https://example.test/p)."))
	if !strings.Contains(rendered, "https://example.test/p") {
		t.Fatalf("a plain-text reader cannot follow the link: %q", rendered)
	}
}

func flattenItems(items [][]Span) []Span {
	all := make([]Span, 0)
	for _, item := range items {
		all = append(all, item...)
	}
	return all
}
