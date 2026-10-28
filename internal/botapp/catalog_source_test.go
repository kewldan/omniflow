package botapp

import (
	"go/ast"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// catalogUse is one `text(locale, key, ...)` call found in the source.
type catalogUse struct {
	key       string
	prefix    bool
	arguments int
	spread    bool
	position  string
}

func catalogUses(t *testing.T) []catalogUse {
	t.Helper()
	fileSet, files := packageSources(t)
	uses := make([]catalogUse, 0, 512)
	for _, source := range files {
		ast.Inspect(source.file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := call.Fun.(*ast.Ident)
			if !ok || name.Name != "text" || len(call.Args) < 2 {
				return true
			}
			position := fileSet.Position(call.Pos())
			use := catalogUse{
				arguments: len(call.Args) - 2, spread: call.Ellipsis.IsValid(),
				position: filepath.Base(position.Filename) + ":" + strconv.Itoa(position.Line),
			}
			switch key := call.Args[1].(type) {
			case *ast.BasicLit:
				use.key, _ = strconv.Unquote(key.Value)
			case *ast.BinaryExpr:
				// "shop.promo." + reason: the literal head names a family of keys.
				literal, isLiteral := key.X.(*ast.BasicLit)
				if !isLiteral {
					return true
				}
				use.key, _ = strconv.Unquote(literal.Value)
				use.prefix = true
			default:
				return true
			}
			uses = append(uses, use)
			return true
		})
	}
	if len(uses) < 100 {
		t.Fatalf("found only %d catalogue uses; the source scan is not reading the package", len(uses))
	}
	return uses
}

// formatVerbs lists the verbs of a format string in order, without `%%`.
func formatVerbs(format string) []string {
	verbs := make([]string, 0, 4)
	for _, verb := range formatVerb.FindAllString(format, -1) {
		if verb != "%%" {
			verbs = append(verbs, verb[len(verb)-1:])
		}
	}
	return verbs
}

// TestEveryTextKeyExistsInBothLocales fails on a screen that would render a
// catalogue key instead of a sentence. `text` returns the key itself when it is
// missing, which is visible but only to the customer who reaches that screen:
// the settings button on the alert screen read "menu.settings" for exactly that
// reason.
func TestEveryTextKeyExistsInBothLocales(t *testing.T) {
	t.Parallel()
	for _, use := range catalogUses(t) {
		if use.prefix {
			found := false
			for key := range catalog {
				if strings.HasPrefix(key, use.key) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: no catalogue key starts with %q", use.position, use.key)
			}
			continue
		}
		message, found := catalog[use.key]
		if !found {
			t.Errorf("%s: catalogue key %q does not exist", use.position, use.key)
			continue
		}
		if use.spread {
			continue
		}
		for _, locale := range []Locale{LocaleRussian, LocaleEnglish} {
			if verbs := formatVerbs(message.get(locale)); len(verbs) != use.arguments {
				t.Errorf("%s: %q in %s takes %d values, the call passes %d",
					use.position, use.key, locale, len(verbs), use.arguments)
			}
		}
	}
}

// TestEveryCatalogueEntryIsTranslated fails on a phrase with an empty side or
// with format verbs that differ between the languages, which renders
// `%!d(string=…)` in one of them.
func TestEveryCatalogueEntryIsTranslated(t *testing.T) {
	t.Parallel()
	for key, message := range catalog {
		if strings.TrimSpace(message.ru) == "" || strings.TrimSpace(message.en) == "" {
			t.Errorf("%q is missing a translation", key)
			continue
		}
		if russian, english := formatVerbs(message.ru), formatVerbs(message.en); !slices.Equal(russian, english) {
			t.Errorf("%q has verbs %v in Russian and %v in English", key, russian, english)
		}
	}
}
