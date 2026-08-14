// Package infopage turns an operator's document into a structure a browser can
// render without ever interpreting it as markup.
//
// The problem this solves: a terms page, an offer, and a privacy policy are long
// documents that need headings, lists, and links to be readable, and they are
// written by an operator and served from the origin that holds the session
// cookie. Rendering operator text as HTML — with or without a sanitiser — puts
// the whole page one sanitiser bug away from stored cross-site scripting.
//
// So the text is parsed here into typed blocks and typed inline runs, and the
// browser renders text nodes and anchors from that structure. There is no HTML
// anywhere in the path, which means there is nothing for a sanitiser to get
// wrong. The cost is that the accepted syntax is small; the benefit is that the
// failure mode of an unrecognised construct is a paragraph that reads oddly
// rather than a script that runs.
//
// The syntax, in full:
//
//	## Heading            a line beginning with two hashes
//	- item                a line beginning with a dash and a space
//	1. item               a line beginning with a number, a dot, and a space
//	blank line            ends the current block
//	[text](https://…)     an inline link, https only
//
// Everything else is a paragraph. There is no bold, no italic, no image, and no
// raw HTML, because a legal document does not need them and each one is another
// construct to get right.
package infopage

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Block kinds.
const (
	BlockHeading   = "heading"
	BlockParagraph = "paragraph"
	BlockList      = "list"
)

// Span is one run of inline content: text, optionally carrying a link.
type Span struct {
	Text string `json:"text"`
	// Href is present only for a validated https address. Nothing else ever
	// reaches a browser as a link target.
	Href string `json:"href,omitempty"`
}

// Block is one paragraph, heading, or list.
type Block struct {
	Kind string `json:"kind"`
	// Spans carries the content of a heading or paragraph.
	Spans []Span `json:"spans,omitempty"`
	// Items carries the content of a list, one entry per bullet.
	Items [][]Span `json:"items,omitempty"`
	// Ordered distinguishes a numbered list from a bulleted one.
	Ordered bool `json:"ordered,omitempty"`
}

// Document is a parsed page body.
type Document struct {
	Blocks []Block `json:"blocks"`
}

var (
	linkPattern    = regexp.MustCompile(`\[([^\]\n]{1,200})\]\((https://[^\s)]{4,500})\)`)
	orderedPattern = regexp.MustCompile(`^\s*\d{1,3}\.\s+`)
)

// Parse turns an operator's source text into blocks.
//
// It never fails. A document is a thing somebody has already written and saved;
// refusing to render it because line 400 has an unbalanced bracket would leave
// an operator with a published page they cannot see and no way to tell which
// line to fix. An unrecognised construct becomes ordinary text.
func Parse(source string) Document {
	document := Document{Blocks: []Block{}}
	var paragraph []string
	var items [][]Span
	ordered := false

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		document.Blocks = append(document.Blocks, Block{
			Kind: BlockParagraph, Spans: spans(strings.Join(paragraph, " ")),
		})
		paragraph = paragraph[:0]
	}
	flushList := func() {
		if len(items) == 0 {
			return
		}
		document.Blocks = append(document.Blocks, Block{
			Kind: BlockList, Items: items, Ordered: ordered,
		})
		items, ordered = nil, false
	}

	for _, raw := range strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			flushParagraph()
			flushList()

		case strings.HasPrefix(trimmed, "## "):
			flushParagraph()
			flushList()
			document.Blocks = append(document.Blocks, Block{
				Kind: BlockHeading, Spans: spans(strings.TrimSpace(trimmed[3:])),
			})

		case strings.HasPrefix(trimmed, "- "):
			flushParagraph()
			if ordered {
				// A bulleted line after a numbered one starts a new list rather
				// than silently joining, because the two read differently and a
				// document that mixes them meant to.
				flushList()
			}
			items = append(items, spans(strings.TrimSpace(trimmed[2:])))

		case orderedPattern.MatchString(trimmed):
			flushParagraph()
			if len(items) > 0 && !ordered {
				flushList()
			}
			ordered = true
			items = append(items, spans(orderedPattern.ReplaceAllString(trimmed, "")))

		default:
			flushList()
			paragraph = append(paragraph, trimmed)
		}
	}
	flushParagraph()
	flushList()
	return document
}

// spans splits one line into text and link runs.
func spans(line string) []Span {
	if line == "" {
		return []Span{}
	}
	matches := linkPattern.FindAllStringSubmatchIndex(line, -1)
	if len(matches) == 0 {
		return []Span{{Text: line}}
	}

	parsed := make([]Span, 0, len(matches)*2+1)
	cursor := 0
	for _, match := range matches {
		if match[0] > cursor {
			parsed = append(parsed, Span{Text: line[cursor:match[0]]})
		}
		text := line[match[2]:match[3]]
		href := line[match[4]:match[5]]
		if safeHref(href) {
			parsed = append(parsed, Span{Text: text, Href: href})
		} else {
			// A link that cannot be offered is still content somebody wrote, so
			// it renders as the text it was rather than disappearing.
			parsed = append(parsed, Span{Text: text})
		}
		cursor = match[1]
	}
	if cursor < len(line) {
		parsed = append(parsed, Span{Text: line[cursor:]})
	}
	return parsed
}

// safeHref accepts an https address and nothing else.
//
// The pattern already requires the prefix; this rejects the characters that
// would let a value break out of an attribute in any future renderer, and the
// control characters a browser strips before parsing a URL — which is how
// `java\tscript:` becomes a scheme nobody wrote.
func safeHref(href string) bool {
	if !strings.HasPrefix(href, "https://") || len(href) < 12 {
		return false
	}
	for _, character := range href {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
		if character == '"' || character == '\'' || character == '<' || character == '>' {
			return false
		}
	}
	return true
}

// PlainText renders a document back to readable text, for the surfaces that
// cannot show structure.
//
// The Telegram bot is the case: it has no way to render a block tree, and a
// terms page a customer can read in a browser and not in the chat is a document
// that exists twice.
func PlainText(document Document) string {
	var builder strings.Builder
	for index, block := range document.Blocks {
		if index > 0 {
			builder.WriteString("\n\n")
		}
		switch block.Kind {
		case BlockHeading:
			builder.WriteString(flatten(block.Spans))
		case BlockParagraph:
			builder.WriteString(flatten(block.Spans))
		case BlockList:
			for item, spans := range block.Items {
				if item > 0 {
					builder.WriteString("\n")
				}
				if block.Ordered {
					builder.WriteString(strconv.Itoa(item + 1))
					builder.WriteString(". ")
				} else {
					builder.WriteString("• ")
				}
				builder.WriteString(flatten(spans))
			}
		}
	}
	return builder.String()
}

// flatten renders spans as text, keeping a link's address beside its label so a
// plain-text reader can still follow it.
func flatten(spans []Span) string {
	var builder strings.Builder
	for _, span := range spans {
		builder.WriteString(span.Text)
		if span.Href != "" {
			builder.WriteString(" (")
			builder.WriteString(span.Href)
			builder.WriteString(")")
		}
	}
	return builder.String()
}
