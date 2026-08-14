package panelpg

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// The connection catalogue as an operator edits it.
//
// The customer surfaces read only enabled rows through internal/connectpg; this
// side reads everything, because a disabled row is exactly what an operator came
// here to turn back on.

// ConnectPlatform is one platform in the operator's view of the catalogue.
type ConnectPlatform struct {
	Slug      string    `json:"slug"`
	LabelEN   string    `json:"labelEn"`
	LabelRU   string    `json:"labelRu"`
	Enabled   bool      `json:"enabled"`
	SortOrder int32     `json:"sortOrder"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
}

// ConnectClient is one recommended application.
type ConnectClient struct {
	ID       string `json:"id,omitempty"`
	Platform string `json:"platform"`
	Name     string `json:"name"`
	// Scheme is concatenated with the subscription link to build the deep link
	// a customer presses. It is validated in internal/commerce, which is where
	// the rule about what may appear in a link lives.
	Scheme         string    `json:"scheme"`
	DownloadURL    string    `json:"downloadUrl,omitempty"`
	InstructionsEN string    `json:"instructionsEn,omitempty"`
	InstructionsRU string    `json:"instructionsRu,omitempty"`
	Enabled        bool      `json:"enabled"`
	SortOrder      int32     `json:"sortOrder"`
	UpdatedAt      time.Time `json:"updatedAt"`
	UpdatedBy      string    `json:"updatedBy,omitempty"`
}

// ConnectCatalogue is the whole screen in one read.
//
// Platforms and clients arrive together because neither is meaningful alone: a
// client names a platform, and a platform with no client documents nothing.
type ConnectCatalogue struct {
	Platforms []ConnectPlatform `json:"platforms"`
	Clients   []ConnectClient   `json:"clients"`
}

var platformSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,30}$`)

// ConnectCatalogue reads everything an operator may edit.
func (service *Service) ConnectCatalogue(ctx context.Context) (ConnectCatalogue, error) {
	platformRows, err := service.queries().ListConnectPlatforms(ctx)
	if err != nil {
		return ConnectCatalogue{}, err
	}
	clientRows, err := service.queries().ListConnectClients(ctx)
	if err != nil {
		return ConnectCatalogue{}, err
	}

	catalogue := ConnectCatalogue{
		Platforms: make([]ConnectPlatform, 0, len(platformRows)),
		Clients:   make([]ConnectClient, 0, len(clientRows)),
	}
	for _, row := range platformRows {
		catalogue.Platforms = append(catalogue.Platforms, ConnectPlatform{
			Slug: row.Slug, LabelEN: row.LabelEn, LabelRU: row.LabelRu,
			Enabled: row.Enabled, SortOrder: row.SortOrder,
			UpdatedAt: row.UpdatedAt.Time, UpdatedBy: uuidString(row.UpdatedBy),
		})
	}
	for _, row := range clientRows {
		catalogue.Clients = append(catalogue.Clients, ConnectClient{
			ID: uuidString(row.ID), Platform: row.PlatformSlug, Name: row.Name,
			Scheme: row.Scheme, DownloadURL: row.DownloadUrl.String,
			InstructionsEN: row.InstructionsEn.String, InstructionsRU: row.InstructionsRu.String,
			Enabled: row.Enabled, SortOrder: row.SortOrder,
			UpdatedAt: row.UpdatedAt.Time, UpdatedBy: uuidString(row.UpdatedBy),
		})
	}
	return catalogue, nil
}

// SaveConnectPlatform creates or updates one platform.
func (service *Service) SaveConnectPlatform(
	ctx context.Context, platform ConnectPlatform, actor Actor,
) (ConnectPlatform, error) {
	platform.Slug = strings.ToLower(strings.TrimSpace(platform.Slug))
	platform.LabelEN = strings.TrimSpace(platform.LabelEN)
	platform.LabelRU = strings.TrimSpace(platform.LabelRU)

	if !platformSlugPattern.MatchString(platform.Slug) {
		return ConnectPlatform{}, wrapValidation(fmt.Errorf(
			"%q is not a platform key; use lowercase letters, digits, and dashes", platform.Slug,
		))
	}
	if platform.LabelEN == "" {
		return ConnectPlatform{}, wrapValidation(fmt.Errorf("an English label is required"))
	}
	// A missing Russian label falls back to the English one at read time rather
	// than rendering as a gap, and storing that fallback makes it visible on
	// the screen instead of hiding it in the reader.
	if platform.LabelRU == "" {
		platform.LabelRU = platform.LabelEN
	}

	var saved ConnectPlatform
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.UpsertConnectPlatform(ctx, dbgen.UpsertConnectPlatformParams{
			Slug: platform.Slug, LabelEn: platform.LabelEN, LabelRu: platform.LabelRU,
			Enabled: platform.Enabled, SortOrder: platform.SortOrder,
			UpdatedBy: optionalUUID(actor.AdminID),
		})
		if err != nil {
			return err
		}
		saved = ConnectPlatform{
			Slug: row.Slug, LabelEN: row.LabelEn, LabelRU: row.LabelRu,
			Enabled: row.Enabled, SortOrder: row.SortOrder,
			UpdatedAt: row.UpdatedAt.Time, UpdatedBy: uuidString(row.UpdatedBy),
		}
		return appendAudit(ctx, queries, actor.audit(
			"connect.platform.saved", "configuration", "connect_platform", platform.Slug,
			map[string]any{"enabled": platform.Enabled, "label": platform.LabelEN},
		))
	})
	if err != nil {
		return ConnectPlatform{}, err
	}
	return saved, nil
}

// DeleteConnectPlatform removes a platform and, by cascade, its clients.
//
// Deleting rather than disabling is offered because a platform an installation
// does not sell to is noise on the operator's screen forever. The cascade is
// deliberate and is why the panel confirms: the clients under it are guidance
// for a platform nobody can choose any more.
func (service *Service) DeleteConnectPlatform(
	ctx context.Context, slug string, actor Actor,
) error {
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, err := queries.DeleteConnectPlatform(ctx, strings.ToLower(strings.TrimSpace(slug)))
		if err != nil {
			return err
		}
		if removed == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit(
			"connect.platform.deleted", "configuration", "connect_platform", slug, nil,
		))
	})
}

// SaveConnectClient creates or updates one recommended application.
func (service *Service) SaveConnectClient(
	ctx context.Context, client ConnectClient, actor Actor,
) (ConnectClient, error) {
	client.Platform = strings.ToLower(strings.TrimSpace(client.Platform))
	client.Name = strings.TrimSpace(client.Name)
	client.Scheme = commerce.NormaliseClientScheme(client.Scheme)
	client.DownloadURL = strings.TrimSpace(client.DownloadURL)

	if client.Platform == "" || client.Name == "" {
		return ConnectClient{}, wrapValidation(fmt.Errorf("a platform and a name are required"))
	}
	// The scheme rule lives in internal/commerce because it is a property of
	// what may appear in a link, not of this table. The table carries the same
	// rule as a constraint, so a script writing directly is refused too.
	if err := commerce.ValidateClientScheme(client.Scheme); err != nil {
		return ConnectClient{}, wrapValidation(err)
	}
	if err := commerce.ValidateDownloadURL(client.DownloadURL); err != nil {
		return ConnectClient{}, wrapValidation(err)
	}

	var saved ConnectClient
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.UpsertConnectClient(ctx, dbgen.UpsertConnectClientParams{
			ID: optionalUUID(client.ID), PlatformSlug: client.Platform, Name: client.Name,
			Scheme: client.Scheme, DownloadUrl: optionalText(client.DownloadURL),
			InstructionsEn: optionalText(strings.TrimSpace(client.InstructionsEN)),
			InstructionsRu: optionalText(strings.TrimSpace(client.InstructionsRU)),
			Enabled:        client.Enabled, SortOrder: client.SortOrder,
			UpdatedBy: optionalUUID(actor.AdminID),
		})
		if err != nil {
			// A platform that does not exist and a duplicate name on one
			// platform are both refusals an operator can act on, and both
			// arrive as constraint violations rather than as a Go check.
			return conflicted(err)
		}
		saved = ConnectClient{
			ID: uuidString(row.ID), Platform: row.PlatformSlug, Name: row.Name,
			Scheme: row.Scheme, DownloadURL: row.DownloadUrl.String,
			InstructionsEN: row.InstructionsEn.String, InstructionsRU: row.InstructionsRu.String,
			Enabled: row.Enabled, SortOrder: row.SortOrder,
			UpdatedAt: row.UpdatedAt.Time, UpdatedBy: uuidString(row.UpdatedBy),
		}
		return appendAudit(ctx, queries, actor.audit(
			"connect.client.saved", "configuration", "connect_client",
			client.Platform+"/"+client.Name,
			map[string]any{
				"enabled": client.Enabled, "scheme": client.Scheme,
				"hasInstructions": strings.TrimSpace(client.InstructionsEN) != "",
			},
		))
	})
	if err != nil {
		return ConnectClient{}, err
	}
	return saved, nil
}

// DeleteConnectClient removes one recommendation.
func (service *Service) DeleteConnectClient(ctx context.Context, id string, actor Actor) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return ErrValidaton
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, err := queries.DeleteConnectClient(ctx, parsed)
		if err != nil {
			return err
		}
		if removed == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit(
			"connect.client.deleted", "configuration", "connect_client", id, nil,
		))
	})
}
