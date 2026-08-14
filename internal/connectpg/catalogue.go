// Package connectpg reads the connection guidance an operator has configured.
//
// It exists so there is exactly one implementation of "which applications do we
// recommend, and how does each import a subscription". The Telegram bot and the
// customer web panel both read through it. That was previously guaranteed by the
// table being a compiled constant; it is now guaranteed by there being one
// query, which is the same guarantee for the same reason — a customer told to
// install one application in a chat and a different one in a browser has been
// handed two products.
package connectpg

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Catalogue answers what a customer surface needs to render a connect screen.
type Catalogue struct {
	queries *dbgen.Queries
}

// New builds a reader over an existing pool.
func New(pool *pgxpool.Pool) *Catalogue {
	if pool == nil {
		return nil
	}
	return &Catalogue{queries: dbgen.New(pool)}
}

// NewFromQueries builds a reader over an existing query set, for a caller that
// already holds one.
func NewFromQueries(queries *dbgen.Queries) *Catalogue {
	if queries == nil {
		return nil
	}
	return &Catalogue{queries: queries}
}

// Platforms lists what an installation documents, in its recommendation order
// and in the reader's language.
//
// A disabled platform is absent rather than greyed out: this list is a set of
// buttons, and a button that cannot lead anywhere is worse than no button.
func (catalogue *Catalogue) Platforms(
	ctx context.Context, locale string,
) ([]commerce.ConnectPlatform, error) {
	if catalogue == nil {
		return nil, nil
	}
	rows, err := catalogue.queries.ListEnabledConnectPlatforms(ctx)
	if err != nil {
		return nil, err
	}
	platforms := make([]commerce.ConnectPlatform, 0, len(rows))
	for _, row := range rows {
		platforms = append(platforms, commerce.ConnectPlatform{
			Slug: row.Slug, Label: pick(locale, row.LabelEn, row.LabelRu),
		})
	}
	return platforms, nil
}

// Clients lists the applications documented for one platform.
//
// An unknown or disabled platform yields nothing rather than an error, because
// the caller's next move is the same either way: fall back to the first platform
// that does exist.
func (catalogue *Catalogue) Clients(
	ctx context.Context, platform, locale string,
) ([]commerce.ClientApp, error) {
	if catalogue == nil {
		return nil, nil
	}
	rows, err := catalogue.queries.ListEnabledConnectClients(
		ctx, strings.ToLower(strings.TrimSpace(platform)),
	)
	if err != nil {
		return nil, err
	}
	clients := make([]commerce.ClientApp, 0, len(rows))
	for _, row := range rows {
		clients = append(clients, commerce.ClientApp{
			Name: row.Name, Scheme: row.Scheme, DownloadURL: row.DownloadUrl.String,
			Instructions: pick(locale, row.InstructionsEn.String, row.InstructionsRu.String),
		})
	}
	return clients, nil
}

// Resolve picks the platform to render and its clients in one step.
//
// Falling back to the first documented platform is what keeps a stale link — a
// bookmark naming a platform the operator has since removed — from producing an
// empty screen. Both returned values are empty only when the installation
// documents nothing at all, which is a state an operator created and which the
// surfaces report rather than hide.
func (catalogue *Catalogue) Resolve(
	ctx context.Context, platform, locale string,
) (chosen string, platforms []commerce.ConnectPlatform, clients []commerce.ClientApp, err error) {
	platforms, err = catalogue.Platforms(ctx, locale)
	if err != nil || len(platforms) == 0 {
		return "", platforms, nil, err
	}
	chosen = strings.ToLower(strings.TrimSpace(platform))
	clients, err = catalogue.Clients(ctx, chosen, locale)
	if err != nil {
		return "", platforms, nil, err
	}
	if len(clients) == 0 {
		chosen = platforms[0].Slug
		clients, err = catalogue.Clients(ctx, chosen, locale)
		if err != nil {
			return "", platforms, nil, err
		}
	}
	return chosen, platforms, clients, nil
}

// pick chooses the Russian text for a Russian reader and the English text
// otherwise. An empty Russian value falls back to English rather than to
// nothing: a half-translated catalogue should read as English, not as a gap.
func pick(locale, english, russian string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "ru") &&
		strings.TrimSpace(russian) != "" {
		return russian
	}
	return english
}
