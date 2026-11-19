package panelpg

import "testing"

func TestValidTemplateButtons(t *testing.T) {
	t.Parallel()
	ok := []TemplateButton{
		{TextRU: "Тарифы", TextEN: "Plans", Route: "plans"},
		{TextRU: "Канал", TextEN: "Channel", URL: "https://t.me/example"},
		{TextRU: "Чат", TextEN: "Chat", URL: "tg://resolve?domain=example"},
	}
	if !validTemplateButtons(ok) {
		t.Fatal("valid buttons were refused")
	}
	for name, buttons := range map[string][]TemplateButton{
		"both targets":   {{TextRU: "a", TextEN: "a", Route: "plans", URL: "https://x"}},
		"no target":      {{TextRU: "a", TextEN: "a"}},
		"unknown route":  {{TextRU: "a", TextEN: "a", Route: "admin"}},
		"javascript url": {{TextRU: "a", TextEN: "a", URL: "javascript:alert(1)"}},
		"missing text":   {{TextRU: "", TextEN: "a", Route: "plans"}},
		"too many":       make([]TemplateButton, 7),
	} {
		if validTemplateButtons(buttons) {
			t.Errorf("%s: accepted", name)
		}
	}
}
