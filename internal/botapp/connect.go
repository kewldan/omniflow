package botapp

import (
	"context"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// The connect screens read the operator's catalogue rather than a compiled
// table. These two helpers are where that read happens, so the views stay pure
// functions of what they are given and the three call sites do not each grow
// their own copy of the lookup.
//
// A catalogue read that fails degrades to an empty list rather than to an error
// screen. The customer came here to move their subscription link onto a device,
// and the link is still on the screen: losing the recommendations is worse than
// nothing and much better than losing the link.

func (app *App) connectPlatformsScreen(
	ctx context.Context, locale Locale, subscription remnawave.Subscription,
) View {
	platforms, err := app.store.ConnectPlatforms(ctx, string(locale))
	if err != nil {
		app.logger.Error("connect catalogue read failed", "error", err)
	}
	return connectPlatformsView(locale, subscription, platforms)
}

func (app *App) connectPlatformScreen(
	ctx context.Context, locale Locale, platform string, subscription remnawave.Subscription,
) View {
	platforms, err := app.store.ConnectPlatforms(ctx, string(locale))
	if err != nil {
		app.logger.Error("connect catalogue read failed", "error", err)
	}
	clients, err := app.store.ConnectClients(ctx, platform, string(locale))
	if err != nil {
		app.logger.Error("connect catalogue read failed", "error", err)
	}
	return connectPlatformView(
		locale, platform, subscription, platformLabel(platforms, platform), clients, platforms,
	)
}

// platformLabel resolves a slug to the operator's own label, falling back to the
// slug so an unlabelled platform reads as itself rather than as nothing.
func platformLabel(platforms []commerce.ConnectPlatform, slug string) string {
	for _, platform := range platforms {
		if platform.Slug == slug {
			return platform.Label
		}
	}
	return slug
}
