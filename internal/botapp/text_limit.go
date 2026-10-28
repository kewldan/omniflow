package botapp

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// telegramTextLimit is the most a message text may hold, in UTF-16 code
// units. Telegram counts the text after it has parsed the HTML, so a raw text
// within the limit is always accepted; one over it is refused outright, and
// the customer is left on whatever the screen showed before.
const telegramTextLimit = 4096

// ellipsis marks a text the guard shortened.
const ellipsis = "…"

// utf16Length is a string's length as Telegram counts it.
func utf16Length(value string) int {
	length := 0
	for _, symbol := range value {
		length += utf16.RuneLen(symbol)
	}
	return length
}

// fitText shortens a message text that would exceed Telegram's limit, without
// ever cutting through a tag or an entity, and closes whatever tags the cut
// left open.
//
// It is the last line of defence, applied to every message the bot sends or
// edits. A screen that can grow — a support conversation, a news post — is
// expected to bound itself and say what it left out; this only guarantees that
// one that did not still reaches the customer, shortened, instead of being
// refused whole.
func fitText(text string) string {
	if utf16Length(text) <= telegramTextLimit {
		return text
	}
	budget := telegramTextLimit - utf16Length(ellipsis)
	var (
		out     strings.Builder
		open    []string
		used    int
		closing int
	)
	out.Grow(len(text))
	for index := 0; index < len(text); {
		unit, width := nextUnit(text[index:])
		name, opens, closes := tagOf(unit)
		nextClosing := closing
		switch {
		case opens:
			nextClosing += len("</" + name + ">")
		case closes && len(open) > 0 && open[len(open)-1] == name:
			nextClosing -= len("</" + name + ">")
		}
		if used+utf16Length(unit)+nextClosing > budget {
			break
		}
		out.WriteString(unit)
		used += utf16Length(unit)
		closing = nextClosing
		switch {
		case opens:
			open = append(open, name)
		case closes && len(open) > 0 && open[len(open)-1] == name:
			open = open[:len(open)-1]
		}
		index += width
	}
	out.WriteString(ellipsis)
	for position := len(open) - 1; position >= 0; position-- {
		out.WriteString("</" + open[position] + ">")
	}
	return out.String()
}

// nextUnit returns the next indivisible piece of an HTML text: a whole tag, a
// whole entity, or one character.
func nextUnit(rest string) (string, int) {
	switch rest[0] {
	case '<':
		if end := strings.IndexByte(rest, '>'); end > 0 {
			return rest[:end+1], end + 1
		}
	case '&':
		if end := strings.IndexByte(rest, ';'); end > 0 && end <= 10 && !strings.ContainsAny(rest[1:end], " <&") {
			return rest[:end+1], end + 1
		}
	}
	_, width := utf8.DecodeRuneInString(rest)
	return rest[:width], width
}

// tagOf reads a unit as a tag: its lower-case name, and whether it opens or
// closes one. Anything that is not a tag reports neither.
func tagOf(unit string) (string, bool, bool) {
	if len(unit) < 3 || unit[0] != '<' || unit[len(unit)-1] != '>' {
		return "", false, false
	}
	inner := strings.TrimSpace(unit[1 : len(unit)-1])
	closes := strings.HasPrefix(inner, "/")
	inner = strings.TrimPrefix(inner, "/")
	name, _, _ := strings.Cut(inner, " ")
	name = strings.ToLower(name)
	if name == "" || strings.HasSuffix(inner, "/") {
		return "", false, false
	}
	return name, !closes, closes
}
