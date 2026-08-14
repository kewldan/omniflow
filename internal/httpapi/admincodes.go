package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountCodeBatches registers the wholesale batch surfaces.
//
// They sit behind the catalogue permissions rather than finance: a batch decides
// what is sold and at what agreed price, which is the same decision as
// publishing a plan version. The money never moves through Omniflow, so there is
// nothing here for `finance.write` to be protecting.
func (handlers *AdminHandlers) mountCodeBatches(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionCatalogRead)).Group(func(read chi.Router) {
		read.Get("/codes/batches", handlers.codeBatches)
		read.Get("/codes/batches/{batchID}/codes", handlers.batchCodes)
	})

	secure.With(handlers.requirePermission(rbac.PermissionCatalogWrite)).Group(func(write chi.Router) {
		write.Post("/codes/batches", handlers.createCodeBatch)
		write.Post("/codes/batches/{batchID}/revoke", handlers.revokeCodeBatch)
	})
}

func (handlers *AdminHandlers) codeBatches(writer http.ResponseWriter, request *http.Request) {
	batches, err := handlers.operations.CodeBatches(request.Context(), queryInt(request, "pageSize"))
	handlers.respond(writer, request, map[string]any{
		"items": batches, "maxBatchSize": panelpg.MaxBatchSize,
	}, err)
}

// batchCodes lists one batch's codes by hint and status.
//
// There is nothing redeemable in the response, and nothing in the database that
// could produce one: only the digest is stored. An operator who lost the list
// cannot get it back from here, which the screen says before they generate a
// batch rather than after.
func (handlers *AdminHandlers) batchCodes(writer http.ResponseWriter, request *http.Request) {
	codes, err := handlers.operations.BatchCodes(request.Context(), chi.URLParam(request, "batchID"))
	handlers.respond(writer, request, map[string]any{"items": codes}, err)
}

// createCodeBatch generates a batch and returns its codes.
//
// This is the only response in the API that carries a redeemable code, and it
// carries every code in the batch. It is a POST that cannot be replayed for that
// reason: asking again generates a different batch rather than returning this
// one, because there is nothing stored that could return it.
func (handlers *AdminHandlers) createCodeBatch(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.CodeBatch
	if !decodeJSON(writer, request, &body) {
		return
	}
	generated, err := handlers.operations.CreateCodeBatch(
		request.Context(), body, actorFrom(request))
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	// No-store, because the one copy of these codes is on its way to an
	// operator's screen and a cache holding them would be a second copy nobody
	// decided to make.
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusCreated, generated)
}

func (handlers *AdminHandlers) revokeCodeBatch(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	revoked, err := handlers.operations.RevokeCodeBatch(
		request.Context(), chi.URLParam(request, "batchID"),
		strings.TrimSpace(body.Reason), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"revoked": revoked}, err)
}
