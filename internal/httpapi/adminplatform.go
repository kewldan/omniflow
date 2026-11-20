package httpapi

import (
	"net/http"
	"slices"

	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/installsettings"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountPlatform mounts the platform administration surfaces: the application
// error log, and the routes later features add beside it.
func (handlers *AdminHandlers) mountPlatform(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionSystemRead)).
		Get("/system/errors", handlers.listAppErrors)
	secure.With(handlers.requirePermission(rbac.PermissionSystemWrite)).
		Post("/system/errors/{errorID}/resolve", handlers.resolveAppError)
	secure.With(handlers.requirePermission(rbac.PermissionSystemWrite)).
		Post("/system/errors/resolve-all", handlers.resolveAllAppErrors)
	// The bot's shipped wording and its menu entries, for the override and
	// menu editors on the settings screen. Saving goes through the bot
	// section like any other setting.
	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/bot/texts", handlers.botTexts)
	// The menu layout editor's preview renders an unsaved bot section the way
	// the bot would. It changes nothing, so reading settings is enough.
	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Post("/bot/menu-preview", handlers.botMenuPreview)
}

func (handlers *AdminHandlers) botMenuPreview(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Document installsettings.Document `json:"document"`
		Locale   string                   `json:"locale"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if body.Document == nil {
		body.Document = installsettings.Document{}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"rows":    botapp.PreviewMainMenu(body.Document, body.Locale),
		"entries": botapp.MenuLayoutEntries,
	})
}

func (handlers *AdminHandlers) botTexts(writer http.ResponseWriter, _ *http.Request) {
	catalog := botapp.TextCatalog()
	keys := make([]string, 0, len(catalog))
	for key := range catalog {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	items := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		items = append(items, map[string]string{"key": key, "ru": catalog[key][0], "en": catalog[key][1]})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "menuRoutes": botapp.MenuRoutes})
}

func (handlers *AdminHandlers) listAppErrors(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	items, err := handlers.operations.AppErrors(request.Context(), query.Get("process"), query.Get("resolved") == "true")
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) resolveAppError(writer http.ResponseWriter, request *http.Request) {
	resolved, err := handlers.operations.ResolveAppError(
		request.Context(), chi.URLParam(request, "errorID"), actorFrom(request))
	handlers.respond(writer, request, resolved, err)
}

func (handlers *AdminHandlers) resolveAllAppErrors(writer http.ResponseWriter, request *http.Request) {
	count, err := handlers.operations.ResolveAllAppErrors(request.Context(), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"resolved": count}, err)
}
