package notice

import (
	"errors"
	"strings"
	"testing"
)

// The whole point of this package is that an operator writes text a customer
// receives without anybody reading it in between. What is tested here is
// therefore not "does it substitute" but "what does it refuse".

func TestAPlaceholderTheNoticeDoesNotCarryIsRefused(t *testing.T) {
	definition, ok := Lookup("expiry")
	if !ok {
		t.Fatal("the expiry notice is not overridable")
	}

	// The declared one is fine.
	if err := Check(definition, "Your access ends in {days} days."); err != nil {
		t.Fatalf("a declared placeholder was refused: %v", err)
	}

	// An undeclared one would reach every customer as literal braces, which is
	// exactly the failure that has to happen at save time instead.
	err := Check(definition, "Hello {name}, your access ends in {days} days.")
	if !errors.Is(err, ErrUnknownVariable) {
		t.Fatalf("an undeclared placeholder was accepted: %v", err)
	}
	if !strings.Contains(err.Error(), "{name}") {
		t.Fatalf("the refusal does not name the offending placeholder: %v", err)
	}

	// And a notice with no variables cannot acquire one by being typed.
	noVariables, _ := Lookup("dunning_retry")
	if err := Check(noVariables, "We could not take {amount}."); !errors.Is(
		err, ErrUnknownVariable,
	) {
		t.Fatalf("a placeholder was invented for a notice with none: %v", err)
	}
}

func TestOnlyMarkupTelegramAcceptsIsAllowed(t *testing.T) {
	definition, _ := Lookup("dunning_retry")

	for _, accepted := range []string{
		"<b>Bold</b> and <i>italic</i>.",
		`Read the <a href="https://example.test/help">guide</a>.`,
		"<code>OMNI-1234</code>",
		"Nothing at all.",
		"A price of 5 &lt; 10 is written as an entity.",
	} {
		if err := Check(definition, accepted); err != nil {
			t.Fatalf("%q was refused: %v", accepted, err)
		}
	}

	for _, refused := range []string{
		"<script>alert(1)</script>",
		"<div>Layout</div>",
		"<b>Never closed",
		"</b>Closes nothing",
		`<a href="javascript:alert(1)">tap</a>`,
		`<a href="tg://resolve?domain=x">tap</a>`,
		`<b class="x">attributes</b>`,
		// A stray angle bracket is refused here rather than by Telegram at
		// delivery time. The refusal says to write `&lt;`.
		"A price of 5 < 10 is not a tag.",
	} {
		if err := Check(definition, refused); !errors.Is(err, ErrUnsupportedMarkup) {
			t.Fatalf("%q was accepted: %v", refused, err)
		}
	}
}

// A percent sign follows a placeholder in the shipped traffic notice, which is
// the shape `fmt.Sprintf` would have choked on. Nothing here interprets it.
func TestTextThatLooksLikeAFormatVerbIsStillText(t *testing.T) {
	definition, _ := Lookup("traffic")
	if err := Check(definition, "You have used {percent}% — under 90% is fine."); err != nil {
		t.Fatalf("a percentage sign was refused: %v", err)
	}
}

func TestEveryShippedDefaultPassesItsOwnValidation(t *testing.T) {
	// A default that the validator would refuse is a default an operator cannot
	// edit and re-save, which would make "revert to the shipped wording" a
	// one-way door.
	for _, definition := range Definitions() {
		for _, locale := range Locales {
			body, ok := definition.Default[locale]
			if !ok {
				t.Fatalf("%s has no %s default", definition.Code, locale)
			}
			if err := Check(definition, body); err != nil {
				t.Fatalf("the shipped %s/%s default is invalid: %v", definition.Code, locale, err)
			}
		}
	}
}

// Every placeholder in a shipped default must be declared, or the panel would
// show an operator a variable reference that omits something the message
// already uses.
func TestEveryDefaultOnlyUsesDeclaredVariables(t *testing.T) {
	for _, definition := range Definitions() {
		declared := map[string]bool{}
		for _, variable := range definition.Variables {
			declared[variable.Name] = true
		}
		for _, locale := range Locales {
			for _, name := range Placeholders(definition.Default[locale]) {
				if !declared[name] {
					t.Fatalf("%s/%s uses {%s}, which is not declared",
						definition.Code, locale, name)
				}
			}
		}
	}
}

// Both locales of one notice must carry the same values. An English default
// that names the plan and a Russian one that does not is a notice where the
// variable reference is only true half the time.
func TestBothLocalesOfANoticeUseTheSameValues(t *testing.T) {
	for _, definition := range Definitions() {
		english := placeholderSet(definition.Default["en"])
		russian := placeholderSet(definition.Default["ru"])
		for name := range english {
			if !russian[name] {
				t.Fatalf("%s uses {%s} in English but not in Russian", definition.Code, name)
			}
		}
		for name := range russian {
			if !english[name] {
				t.Fatalf("%s uses {%s} in Russian but not in English", definition.Code, name)
			}
		}
	}
}

func TestRenderingLeavesNoPlaceholderBehind(t *testing.T) {
	definition, _ := Lookup("renewal")
	rendered := Render(definition.Default["en"], Samples(definition))
	if strings.ContainsAny(rendered, "{}") {
		t.Fatalf("a placeholder survived the render: %q", rendered)
	}
	if !strings.Contains(rendered, "Pro") {
		t.Fatalf("the sample plan is missing: %q", rendered)
	}

	// A value that was not supplied disappears rather than reaching the customer
	// as braces.
	if got := Render("Ends in {days} days.", nil); got != "Ends in  days." {
		t.Fatalf("an unsupplied value rendered as %q", got)
	}
}

func TestAnOverlongBodyIsRefusedBeforeTelegramRefusesIt(t *testing.T) {
	definition, _ := Lookup("dunning_retry")
	if err := Check(definition, strings.Repeat("a", maxBody+1)); !errors.Is(err, ErrBodyTooLong) {
		t.Fatalf("an overlong body was accepted: %v", err)
	}
	if err := Check(definition, "   "); !errors.Is(err, ErrEmptyBody) {
		t.Fatalf("a blank body was accepted: %v", err)
	}
}

func placeholderSet(body string) map[string]bool {
	set := map[string]bool{}
	for _, name := range Placeholders(body) {
		set[name] = true
	}
	return set
}
