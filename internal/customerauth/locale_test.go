package customerauth

import "testing"

func TestPreferredLocaleHonoursQualityAndRegionalTags(t *testing.T) {
	cases := map[string]string{
		"":                              "",
		"*":                             "",
		"de-DE,fr;q=0.8":                "",
		"ru":                            "ru",
		"ru-RU,ru;q=0.9,en-US;q=0.8":    "ru",
		"en-GB,en;q=0.9,ru;q=0.8":       "en",
		"de-DE,en;q=0.5,ru;q=0.7":       "ru",
		"ru;q=0,en;q=0.1":               "en",
		"EN_us":                         "en",
		"  ru-RU ; q=0.3 , en ; q=0.3 ": "ru",
		"fr,*;q=0.5":                    "",
	}
	for header, want := range cases {
		if got := PreferredLocale(header); got != want {
			t.Errorf("PreferredLocale(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestResolveSignUpLocalePrefersTheTelegramClientThenTheBrowser(t *testing.T) {
	cases := []struct{ telegram, accept, want string }{
		{"ru", "en-US", "ru"},
		{"en", "ru-RU", "en"},
		{"de", "ru-RU,ru;q=0.9", "ru"},
		{"", "ru", "ru"},
		{"", "fr", "en"},
		{"", "", "en"},
	}
	for _, testCase := range cases {
		if got := ResolveSignUpLocale(testCase.telegram, testCase.accept); got != testCase.want {
			t.Errorf("ResolveSignUpLocale(%q, %q) = %q, want %q",
				testCase.telegram, testCase.accept, got, testCase.want)
		}
	}
}
