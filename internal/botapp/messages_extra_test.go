package botapp

import (
	"regexp"
	"slices"
	"testing"
)

var verbPattern = regexp.MustCompile(`%[0-9]*[a-z]`)

// Every extra translation names a key the English catalogue has and keeps its
// placeholders in the same order, or fmt would print the wrong value.
func TestExtraCatalogMatchesEnglish(t *testing.T) {
	for locale, messages := range extraCatalog {
		for key, translated := range messages {
			english, ok := catalog[key]
			if !ok {
				t.Errorf("%s: %s is not an English key", locale, key)
				continue
			}
			if !slices.Equal(verbPattern.FindAllString(english.en, -1), verbPattern.FindAllString(translated, -1)) {
				t.Errorf("%s: %s has different placeholders", locale, key)
			}
		}
	}
}

func TestExtraLanguagesFollowThePolicy(t *testing.T) {
	t.Cleanup(func() { SetLanguagePolicy("en", nil) })
	SetLanguagePolicy("en", nil)
	if got := localeFrom("uk"); got != LocaleEnglish {
		t.Fatalf("Ukrainian without being offered = %s", got)
	}
	SetLanguagePolicy("en", []string{"en", "uk", "fa"})
	if got := localeFrom("uk-UA"); got != LocaleUkrainian {
		t.Fatalf("Ukrainian offered = %s", got)
	}
	if got := localeFrom("zh-CN"); got != LocaleEnglish {
		t.Fatalf("Chinese not offered = %s", got)
	}
	if text(LocalePersian, "menu.settings") != "⚙️ تنظیمات" || text(LocalePersian, "menu.unavail") != text(LocaleEnglish, "menu.unavail") {
		t.Fatal("Persian lookup or English fallback is wrong")
	}
}
