package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/branding"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountTheme registers the palette and the brand images.
//
// Reading and writing split the same way every other settings surface does. The
// images are a write rather than a settings document because they are bytes,
// and they carry their own routes so a save of the palette does not have to
// carry a quarter of a megabyte it did not change.
func (handlers *AdminHandlers) mountTheme(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).Group(func(read chi.Router) {
		read.Get("/settings/theme", handlers.theme)
		read.Get("/settings/theme/assets", handlers.brandingAssets)
	})

	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).Group(func(write chi.Router) {
		write.Put("/settings/theme", handlers.saveTheme)
		write.Put("/settings/theme/assets/{kind}", handlers.saveBrandingAsset)
		write.Delete("/settings/theme/assets/{kind}", handlers.deleteBrandingAsset)
	})
}

// mountPublicBranding registers the surface both panels read before they paint.
//
// It sits outside `/v1/panel` because it has to be readable with no session at
// all: a sign-in screen is the first page anybody sees, and it is the page an
// installation most wants to carry its own mark. It carries the service name,
// the palette, and the addresses of the images — and nothing else, because
// every field here is readable by anyone who can reach the installation.
//
// The route lives with the panel handlers because that is where the settings
// service is attached. An installation with no panel has no theme to publish
// and gets no route, which the web application handles by rendering the shipped
// design.
func (handlers *AdminHandlers) mountPublicBranding(router chi.Router) {
	if handlers.operations == nil {
		return
	}
	router.Get("/v1/branding", handlers.publicBranding)
	router.Get("/v1/branding/assets/{kind}", handlers.publicBrandingAsset)
}

func (handlers *AdminHandlers) theme(writer http.ResponseWriter, request *http.Request) {
	settings, err := handlers.operations.Theme(request.Context())
	handlers.respond(writer, request, settings, err)
}

func (handlers *AdminHandlers) saveTheme(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Theme branding.Theme `json:"theme"`
		// Version is what the operator's screen was showing, so two tabs saving
		// the palette conflict rather than one silently winning.
		Version int32 `json:"version"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	settings, err := handlers.operations.SaveTheme(
		request.Context(), body.Theme, body.Version, actorFrom(request))
	handlers.respond(writer, request, settings, err)
}

func (handlers *AdminHandlers) brandingAssets(writer http.ResponseWriter, request *http.Request) {
	assets, err := handlers.operations.BrandingAssets(request.Context())
	handlers.respond(writer, request, map[string]any{
		"items": assets, "kinds": panelpg.BrandingAssetKinds,
		"contentTypes": panelpg.BrandingAssetTypes,
		"maxBytes":     panelpg.MaxBrandingAssetBytes,
	}, err)
}

// saveBrandingAsset takes the image as the request body rather than as a
// multipart form or a base64 field.
//
// A multipart parse buffers the part before anything can check its size, and a
// base64 field inflates a quarter of a megabyte into a third of one inside a
// JSON document that also has to be parsed. The raw body is bounded before a
// byte is read, and the declared type is checked against the bytes themselves
// in the service.
func (handlers *AdminHandlers) saveBrandingAsset(writer http.ResponseWriter, request *http.Request) {
	contentType, _, _ := strings.Cut(request.Header.Get("Content-Type"), ";")
	contentType = strings.TrimSpace(strings.ToLower(contentType))

	limited := http.MaxBytesReader(writer, request.Body, panelpg.MaxBrandingAssetBytes+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		writeProblem(
			writer, request, http.StatusRequestEntityTooLarge, "asset_too_large",
			"The image must be at most "+
				strconv.Itoa(panelpg.MaxBrandingAssetBytes/1024)+" KB",
		)
		return
	}

	asset, err := handlers.operations.SaveBrandingAsset(
		request.Context(), chi.URLParam(request, "kind"), contentType, content,
		actorFrom(request))
	handlers.respond(writer, request, asset, err)
}

func (handlers *AdminHandlers) deleteBrandingAsset(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteBrandingAsset(
		request.Context(), chi.URLParam(request, "kind"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"removed": true}, err)
}

// publicBranding answers what an installation looks like.
func (handlers *AdminHandlers) publicBranding(writer http.ResponseWriter, request *http.Request) {
	context := request.Context()
	settings, err := handlers.operations.Theme(context)
	if err != nil {
		handlers.respond(writer, request, nil, err)
		return
	}
	assets, err := handlers.operations.BrandingAssets(context)
	if err != nil {
		handlers.respond(writer, request, nil, err)
		return
	}

	// The service name comes from the branding section, which is a separate
	// document. A section that has never been saved is not an error here: the
	// application falls back to its own name.
	name := ""
	if section, err := handlers.operations.SettingSection(context, "branding"); err == nil {
		var document struct {
			ServiceName string `json:"serviceName"`
		}
		if json.Unmarshal(section.Document, &document) == nil {
			name = strings.TrimSpace(document.ServiceName)
		}
	}

	// Each address carries the image's own checksum, so a browser may cache the
	// bytes indefinitely and still see a new logo the moment one is uploaded.
	addresses := make(map[string]string, len(assets))
	for _, asset := range assets {
		addresses[asset.Kind] = "/v1/branding/assets/" + asset.Kind + "?v=" + asset.Checksum
	}

	// A minute is long enough that a burst of page loads reads one row, and
	// short enough that an operator adjusting a colour sees it without being
	// told to clear anything.
	writer.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(writer, http.StatusOK, map[string]any{
		"serviceName":   name,
		"css":           settings.CSS,
		"radius":        settings.Theme.Radius,
		"density":       settings.Theme.Density,
		"allowedThemes": settings.Theme.AllowedThemes,
		"defaultTheme":  settings.Theme.DefaultTheme,
		"assets":        addresses,
	})
}

// publicBrandingAsset serves one image.
func (handlers *AdminHandlers) publicBrandingAsset(writer http.ResponseWriter, request *http.Request) {
	contentType, content, checksum, err := handlers.operations.BrandingAssetContent(
		request.Context(), chi.URLParam(request, "kind"))
	if err != nil {
		handlers.respond(writer, request, nil, err)
		return
	}

	tag := `"` + checksum + `"`
	writer.Header().Set("ETag", tag)
	writer.Header().Set("Content-Type", contentType)
	// The bytes at a given checksum never change, so a client that asked for
	// this exact address may keep the answer. A client that asked without the
	// query parameter revalidates, which the ETag makes one cheap request.
	if request.URL.Query().Get("v") == checksum {
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		writer.Header().Set("Cache-Control", "public, max-age=60")
	}
	// A logo is not a document, and nothing about it should be interpreted as
	// one if a content type is ever wrong.
	writer.Header().Set("X-Content-Type-Options", "nosniff")

	if match := request.Header.Get("If-None-Match"); match != "" && strings.Contains(match, checksum) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}
