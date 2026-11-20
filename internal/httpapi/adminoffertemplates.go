package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountOfferTemplates registers offer templates and their bulk send to a
// segment.
func (handlers *AdminHandlers) mountOfferTemplates(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).
		Get("/offers/templates", handlers.offerTemplates)
	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Put("/offers/templates", handlers.saveOfferTemplate)
		write.Post("/offers/templates/{templateID}/send", handlers.sendOfferTemplate)
	})
}

func (handlers *AdminHandlers) offerTemplates(writer http.ResponseWriter, request *http.Request) {
	templates, err := handlers.operations.OfferTemplates(request.Context())
	handlers.respond(writer, request, map[string]any{"items": templates}, err)
}

func (handlers *AdminHandlers) saveOfferTemplate(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.OfferTemplate
	if !decodeJSON(writer, request, &body) {
		return
	}
	template, err := handlers.operations.SaveOfferTemplate(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, template, err)
}

func (handlers *AdminHandlers) sendOfferTemplate(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		SegmentID string `json:"segmentId"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	offered, err := handlers.operations.SendOfferTemplate(request.Context(),
		chi.URLParam(request, "templateID"), body.SegmentID, actorFrom(request))
	handlers.respond(writer, request, map[string]any{"offered": offered}, err)
}
