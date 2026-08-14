package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountCustomerMerge registers the account-merge surfaces.
//
// The preview needs only `customers.read`, because looking at what a merge would
// do is looking at two customer records. Performing one needs `customers.write`
// and a reason: it is irreversible, it combines two people's records, and "why"
// is the first question anybody asks about one afterwards.
func (handlers *AdminHandlers) mountCustomerMerge(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionCustomersRead)).
		Get("/customers/{customerID}/merge/preview", handlers.mergePreview)

	secure.With(handlers.requirePermission(rbac.PermissionCustomersWrite)).
		Post("/customers/{customerID}/merge", handlers.mergeCustomers)
}

// mergePreview answers what a merge would do, including why it cannot happen.
func (handlers *AdminHandlers) mergePreview(writer http.ResponseWriter, request *http.Request) {
	preview, err := handlers.operations.MergePreview(
		request.Context(), chi.URLParam(request, "customerID"), query(request, "into"))
	handlers.respond(writer, request, preview, err)
}

// mergeCustomers performs it.
//
// The path names the account being absorbed and the body names the survivor,
// which is the order the operator thinks in: they are looking at the duplicate
// and deciding where it goes. The refusals are recomputed here rather than
// trusted from the preview, because the two are separate requests and a merge
// authorised by a stale screen is the kind of thing that only goes wrong once.
func (handlers *AdminHandlers) mergeCustomers(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Into string `json:"into"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	result, err := handlers.operations.MergeCustomers(
		request.Context(), chi.URLParam(request, "customerID"), body.Into, actorFrom(request))
	handlers.respond(writer, request, result, err)
}
