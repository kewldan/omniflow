package panelpg

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/omniflow/omniflow/internal/commerce"
)

// ImportedPlatform is one platform read from another catalogue — Remnawave's
// own subscription page — in the shape this catalogue stores.
type ImportedPlatform struct {
	Slug    string
	LabelEN string
	LabelRU string
	Clients []ImportedClient
}

// ImportedClient is one application of an imported platform.
type ImportedClient struct {
	Name           string
	Scheme         string
	DownloadURL    string
	InstructionsEN string
	InstructionsRU string
}

// ConnectImport reports what an import did.
type ConnectImport struct {
	Platforms int      `json:"platforms"`
	Clients   int      `json:"clients"`
	Skipped   []string `json:"skipped"`
}

// ImportConnectCatalogue merges an imported catalogue into this one.
//
// A platform is matched by its key and an application by its platform and
// name, so a second import updates what the first one created instead of
// adding copies, and an operator's own entries with other names are left as
// they are. An application whose import scheme would not pass the catalogue's
// own rule — a page that uses a template Omniflow cannot fill, say — is skipped
// and named in the result rather than stored half-working.
func (service *Service) ImportConnectCatalogue(
	ctx context.Context, platforms []ImportedPlatform, actor Actor,
) (ConnectImport, error) {
	current, err := service.ConnectCatalogue(ctx)
	if err != nil {
		return ConnectImport{}, err
	}
	existing := map[string]string{}
	for _, client := range current.Clients {
		existing[client.Platform+"\x00"+strings.ToLower(client.Name)] = client.ID
	}
	result := ConnectImport{Skipped: []string{}}
	for order, platform := range platforms {
		slug := strings.ToLower(strings.TrimSpace(platform.Slug))
		if !platformSlugPattern.MatchString(slug) {
			result.Skipped = append(result.Skipped, platform.Slug)
			continue
		}
		label := strings.TrimSpace(platform.LabelEN)
		if label == "" {
			label = platform.Slug
		}
		if _, err = service.SaveConnectPlatform(ctx, ConnectPlatform{
			Slug: slug, LabelEN: truncateRunes(label, 40), LabelRU: truncateRunes(strings.TrimSpace(platform.LabelRU), 40),
			Enabled: true, SortOrder: int32(order * 10),
		}, actor); err != nil {
			return result, err
		}
		result.Platforms++
		for position, client := range platform.Clients {
			name := truncateRunes(strings.TrimSpace(client.Name), 60)
			scheme := commerce.NormaliseClientScheme(client.Scheme)
			if commerce.ValidateClientScheme(scheme) != nil || commerce.ValidateDownloadURL(client.DownloadURL) != nil {
				result.Skipped = append(result.Skipped, fmt.Sprintf("%s/%s", slug, name))
				continue
			}
			if _, err = service.SaveConnectClient(ctx, ConnectClient{
				ID: existing[slug+"\x00"+strings.ToLower(name)], Platform: slug, Name: name,
				Scheme: scheme, DownloadURL: strings.TrimSpace(client.DownloadURL),
				InstructionsEN: truncateRunes(client.InstructionsEN, 2000),
				InstructionsRU: truncateRunes(client.InstructionsRU, 2000),
				Enabled:        true, SortOrder: int32(position * 10),
			}, actor); err != nil {
				return result, err
			}
			result.Clients++
		}
	}
	return result, nil
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}
