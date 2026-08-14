package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountContent registers the information-page surfaces.
//
// They sit behind the marketing permissions, beside news, because they are the
// same capability: publishing words to customers. A support operator who may
// answer a ticket has no business rewriting the terms of service.
func (handlers *AdminHandlers) mountContent(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).Group(func(read chi.Router) {
		read.Get("/content/pages", handlers.infoPages)
		read.Get("/content/pages/{slug}", handlers.infoPage)
	})

	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Put("/content/pages", handlers.saveInfoPage)
		write.Post("/content/pages/{slug}/publication", handlers.setInfoPagePublication)
		write.Delete("/content/pages/{slug}", handlers.deleteInfoPage)
	})
}

// mountPublicContent registers the reader's surface.
//
// It is public and unauthenticated, which is the entire point of the feature: a
// payment provider's reviewer and an application store's reviewer both have to
// be able to open the offer and the privacy policy without an account, and a
// customer deciding whether to become one has to be able to read the terms
// before they do.
//
// It carries no personal data by construction — there is nothing on these pages
// but what an operator published for everybody.
func (handlers *AdminHandlers) mountPublicContent(router chi.Router) {
	if handlers.operations == nil {
		return
	}
	router.Get("/v1/pages", handlers.publicInfoPages)
	router.Get("/v1/pages/{slug}", handlers.publicInfoPage)
}

func (handlers *AdminHandlers) infoPages(writer http.ResponseWriter, request *http.Request) {
	pages, err := handlers.operations.InfoPages(request.Context())
	handlers.respond(writer, request, map[string]any{
		"items": pages, "kinds": panelpg.InfoPageKinds,
	}, err)
}

func (handlers *AdminHandlers) infoPage(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.InfoPage(request.Context(), chi.URLParam(request, "slug"))
	handlers.respond(writer, request, page, err)
}

func (handlers *AdminHandlers) saveInfoPage(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.InfoPage
	if !decodeJSON(writer, request, &body) {
		return
	}
	page, err := handlers.operations.SaveInfoPage(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, page, err)
}

func (handlers *AdminHandlers) setInfoPagePublication(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Published bool `json:"published"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	page, err := handlers.operations.SetInfoPagePublication(
		request.Context(), chi.URLParam(request, "slug"), body.Published, actorFrom(request))
	handlers.respond(writer, request, page, err)
}

func (handlers *AdminHandlers) deleteInfoPage(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteInfoPage(
		request.Context(), chi.URLParam(request, "slug"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"removed": true}, err)
}

func (handlers *AdminHandlers) publicInfoPages(writer http.ResponseWriter, request *http.Request) {
	pages, err := handlers.operations.PublicInfoPages(
		request.Context(), readerLocale(request))
	// A published document changes rarely and is read by anonymous visitors and
	// by review bots, so a short shared cache is worth having. Five minutes is
	// long enough to matter and short enough that publishing a correction feels
	// immediate.
	writer.Header().Set("Cache-Control", "public, max-age=300")
	handlers.respond(writer, request, map[string]any{"items": pages}, err)
}

func (handlers *AdminHandlers) publicInfoPage(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.PublicInfoPage(
		request.Context(), chi.URLParam(request, "slug"), readerLocale(request))
	writer.Header().Set("Cache-Control", "public, max-age=300")
	handlers.respond(writer, request, page, err)
}

// readerLocale takes the language from the query, falling back to the
// Accept-Language header.
//
// The query wins because a link somebody sends to a colleague has to carry the
// language it was read in; the header is what makes an address with no
// parameter answer in the reader's own language rather than always in English.
func readerLocale(request *http.Request) string {
	if locale := query(request, "locale"); locale != "" {
		return locale
	}
	for _, candidate := range strings.Split(request.Header.Get("Accept-Language"), ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(candidate), ";")
		if strings.HasPrefix(strings.ToLower(tag), "ru") {
			return "ru"
		}
		if strings.HasPrefix(strings.ToLower(tag), "en") {
			return "en"
		}
	}
	return "en"
}
