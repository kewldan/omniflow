package remnawave

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

// SubscriptionPage is the client catalogue of one Remnawave subscription-page
// configuration: which applications the panel's own subscription page
// recommends on each platform, and how each imports a link.
type SubscriptionPage struct {
	Name      string
	Platforms []SubscriptionPagePlatform
}

// SubscriptionPagePlatform is one platform of the page, keyed as the panel
// keys it (ios, android, windows, macos, linux, androidTV, appleTV).
type SubscriptionPagePlatform struct {
	Key   string
	Label Localized
	Apps  []SubscriptionPageApp
}

// SubscriptionPageApp is one recommended application.
type SubscriptionPageApp struct {
	Name     string
	Featured bool
	// URLScheme is the import prefix the page puts before the link, as the
	// panel stores it; it may still need validating before it is offered.
	URLScheme string
	// DownloadURL is the first external https button of the app's steps.
	DownloadURL string
	// Steps are the page's block titles and descriptions, joined per language.
	Steps Localized
}

// Localized is a text in the languages the page carries.
type Localized struct {
	EN string
	RU string
}

// SubscriptionPageConfig reads one subscription-page configuration by UUID.
// The panel's shape has moved between releases, so the decode is lenient:
// a label may be a string or a per-language object, and a field the page does
// not carry is left empty rather than failing the read.
func (client *Client) SubscriptionPageConfig(ctx context.Context, uuid string) (SubscriptionPage, error) {
	var envelope struct {
		Response struct {
			Name   string          `json:"name"`
			Config json.RawMessage `json:"config"`
		} `json:"response"`
	}
	if err := client.get(ctx, "/api/subscription-page-configs/"+url.PathEscape(strings.TrimSpace(uuid)), &envelope); err != nil {
		return SubscriptionPage{}, err
	}
	page, err := parseSubscriptionPage(envelope.Response.Config)
	page.Name = envelope.Response.Name
	return page, err
}

func parseSubscriptionPage(raw json.RawMessage) (SubscriptionPage, error) {
	// Some releases store the configuration as a JSON string.
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = json.RawMessage(encoded)
	}
	var config struct {
		Platforms map[string]json.RawMessage `json:"platforms"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return SubscriptionPage{}, ErrWebhookMalformed
	}
	keys := make([]string, 0, len(config.Platforms))
	for key := range config.Platforms {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	page := SubscriptionPage{}
	for _, key := range keys {
		var platform struct {
			DisplayName json.RawMessage   `json:"displayName"`
			Apps        []json.RawMessage `json:"apps"`
		}
		// An older page lists the apps directly under the platform key.
		if json.Unmarshal(config.Platforms[key], &platform) != nil {
			if json.Unmarshal(config.Platforms[key], &platform.Apps) != nil {
				continue
			}
		}
		parsed := SubscriptionPagePlatform{Key: key, Label: localized(platform.DisplayName)}
		for _, rawApp := range platform.Apps {
			if app, ok := parseSubscriptionPageApp(rawApp); ok {
				parsed.Apps = append(parsed.Apps, app)
			}
		}
		if len(parsed.Apps) > 0 {
			page.Platforms = append(page.Platforms, parsed)
		}
	}
	return page, nil
}

func parseSubscriptionPageApp(raw json.RawMessage) (SubscriptionPageApp, bool) {
	var app struct {
		Name       string `json:"name"`
		Featured   bool   `json:"featured"`
		IsFeatured bool   `json:"isFeatured"`
		URLScheme  string `json:"urlScheme"`
		Blocks     []struct {
			Title       json.RawMessage `json:"title"`
			Description json.RawMessage `json:"description"`
			Buttons     []struct {
				Type string          `json:"type"`
				Link string          `json:"link"`
				URL  string          `json:"url"`
				Text json.RawMessage `json:"text"`
			} `json:"buttons"`
		} `json:"blocks"`
	}
	if json.Unmarshal(raw, &app) != nil || strings.TrimSpace(app.Name) == "" {
		return SubscriptionPageApp{}, false
	}
	parsed := SubscriptionPageApp{
		Name: strings.TrimSpace(app.Name), Featured: app.Featured || app.IsFeatured,
		URLScheme: strings.TrimSpace(app.URLScheme),
	}
	var stepsEN, stepsRU []string
	for _, block := range app.Blocks {
		title, description := localized(block.Title), localized(block.Description)
		if line := joinStep(title.EN, description.EN); line != "" {
			stepsEN = append(stepsEN, line)
		}
		if line := joinStep(title.RU, description.RU); line != "" {
			stepsRU = append(stepsRU, line)
		}
		for _, button := range block.Buttons {
			link := firstNonBlank(button.Link, button.URL)
			kind := strings.ToLower(button.Type)
			if parsed.DownloadURL == "" && strings.HasPrefix(kind, "external") && strings.HasPrefix(link, "https://") {
				parsed.DownloadURL = link
			}
			// A page with no urlScheme names it on its "add subscription"
			// button instead: the part before the link placeholder.
			if parsed.URLScheme == "" && strings.EqualFold(button.Type, "subscriptionLink") {
				if prefix, _, found := strings.Cut(link, "{{SUBSCRIPTION_LINK}}"); found {
					parsed.URLScheme = prefix
				}
			}
		}
	}
	parsed.Steps = Localized{EN: strings.Join(stepsEN, "\n\n"), RU: strings.Join(stepsRU, "\n\n")}
	return parsed, true
}

func joinStep(title, description string) string {
	title, description = strings.TrimSpace(title), strings.TrimSpace(description)
	switch {
	case title != "" && description != "":
		return title + "\n" + description
	case title != "":
		return title
	}
	return description
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// localized reads a label that is either a plain string or an object keyed by
// language. A string is taken as English and Russian alike.
func localized(raw json.RawMessage) Localized {
	if len(raw) == 0 {
		return Localized{}
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return Localized{EN: plain, RU: plain}
	}
	var byLanguage map[string]string
	if json.Unmarshal(raw, &byLanguage) != nil {
		return Localized{}
	}
	text := Localized{EN: byLanguage["en"], RU: byLanguage["ru"]}
	if text.EN == "" {
		for _, value := range byLanguage {
			if value != "" {
				text.EN = value
				break
			}
		}
	}
	if text.RU == "" {
		text.RU = text.EN
	}
	return text
}
