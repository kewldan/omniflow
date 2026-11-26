package httpapi

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// mountConnectCatalogue registers the connection guidance surfaces.
//
// It sits under settings rather than under the catalogue because it configures
// how the product is used rather than what is sold: nothing here has a price, a
// version, or an effect on an order.
func (handlers *AdminHandlers) mountConnectCatalogue(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/settings/connect", handlers.connectCatalogue)

	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).Group(func(write chi.Router) {
		write.Put("/settings/connect/platforms", handlers.saveConnectPlatform)
		write.Delete("/settings/connect/platforms/{slug}", handlers.deleteConnectPlatform)
		write.Put("/settings/connect/clients", handlers.saveConnectClient)
		write.Delete("/settings/connect/clients/{clientID}", handlers.deleteConnectClient)
		write.Post("/settings/connect/import-remnawave", handlers.importConnectFromRemnawave)
		write.Put("/settings/connect/downloads", handlers.saveConnectDownload)
		write.Delete("/settings/connect/downloads/{downloadID}", handlers.deleteConnectDownload)
		write.Post("/settings/connect/downloads/{downloadID}/check", handlers.checkConnectMirror)
	})
}

func (handlers *AdminHandlers) connectCatalogue(writer http.ResponseWriter, request *http.Request) {
	catalogue, err := handlers.operations.ConnectCatalogue(request.Context())
	catalogue.Mirroring = handlers.clientMirroring
	handlers.respond(writer, request, catalogue, err)
}

func (handlers *AdminHandlers) saveConnectDownload(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.ConnectDownload
	if !decodeJSON(writer, request, &body) {
		return
	}
	download, err := handlers.operations.SaveConnectDownload(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, download, err)
}

func (handlers *AdminHandlers) deleteConnectDownload(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteConnectDownload(
		request.Context(), chi.URLParam(request, "downloadID"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"removed": true}, err)
}

// checkConnectMirror makes a mirror due now; the worker looks within a minute.
func (handlers *AdminHandlers) checkConnectMirror(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.CheckConnectMirror(
		request.Context(), chi.URLParam(request, "downloadID"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"queued": true}, err)
}

func (handlers *AdminHandlers) saveConnectPlatform(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.ConnectPlatform
	if !decodeJSON(writer, request, &body) {
		return
	}
	platform, err := handlers.operations.SaveConnectPlatform(
		request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, platform, err)
}

func (handlers *AdminHandlers) deleteConnectPlatform(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteConnectPlatform(
		request.Context(), chi.URLParam(request, "slug"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"removed": true}, err)
}

func (handlers *AdminHandlers) saveConnectClient(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.ConnectClient
	if !decodeJSON(writer, request, &body) {
		return
	}
	client, err := handlers.operations.SaveConnectClient(
		request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, client, err)
}

func (handlers *AdminHandlers) deleteConnectClient(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteConnectClient(
		request.Context(), chi.URLParam(request, "clientID"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"removed": true}, err)
}

// subpageUUID is the shape of a Remnawave subscription-page configuration ID.
var subpageUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// subpagePlatformSlugs maps the panel's platform keys onto this catalogue's.
var subpagePlatformSlugs = map[string]string{
	"ios": "ios", "android": "android", "windows": "windows", "macos": "macos",
	"linux": "linux", "androidtv": "android-tv", "appletv": "apple-tv",
}

// importConnectFromRemnawave copies the applications Remnawave's own
// subscription page recommends into this catalogue, so an operator who already
// curated that page does not type it in twice. It reads the panel and writes
// only Omniflow's catalogue.
func (handlers *AdminHandlers) importConnectFromRemnawave(writer http.ResponseWriter, request *http.Request) {
	if handlers.remnawave == nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "remnawave_unavailable", "Remnawave is not configured")
		return
	}
	var body struct {
		ConfigUUID string `json:"configUuid"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	body.ConfigUUID = strings.TrimSpace(body.ConfigUUID)
	if !subpageUUID.MatchString(body.ConfigUUID) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_input", "The configuration ID must be a UUID")
		return
	}
	page, err := handlers.remnawave.SubscriptionPageConfig(request.Context(), body.ConfigUUID)
	switch {
	case errors.Is(err, remnawave.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "Remnawave has no subscription page with that ID")
		return
	case err != nil:
		writeProblem(writer, request, http.StatusBadGateway, "remnawave_unavailable", "Remnawave could not be read")
		return
	}
	result, err := handlers.operations.ImportConnectCatalogue(request.Context(), importedPlatforms(page), actorFrom(request))
	handlers.respond(writer, request, result, err)
}

// importedPlatforms turns a subscription page into catalogue rows. Featured
// applications come first, because the first application of a platform is the
// one both surfaces recommend.
func importedPlatforms(page remnawave.SubscriptionPage) []panelpg.ImportedPlatform {
	platforms := make([]panelpg.ImportedPlatform, 0, len(page.Platforms))
	for _, platform := range page.Platforms {
		slug, known := subpagePlatformSlugs[strings.ToLower(platform.Key)]
		if !known {
			slug = strings.ToLower(platform.Key)
		}
		imported := panelpg.ImportedPlatform{Slug: slug, LabelEN: platform.Label.EN, LabelRU: platform.Label.RU}
		for _, featured := range []bool{true, false} {
			for _, app := range platform.Apps {
				if app.Featured != featured {
					continue
				}
				imported.Clients = append(imported.Clients, panelpg.ImportedClient{
					Name: app.Name, Scheme: app.URLScheme, DownloadURL: app.DownloadURL,
					InstructionsEN: app.Steps.EN, InstructionsRU: app.Steps.RU,
				})
			}
		}
		platforms = append(platforms, imported)
	}
	return platforms
}
