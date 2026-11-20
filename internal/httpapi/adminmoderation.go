package httpapi

import (
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountModeration mounts bans by list, ban message templates, and the
// bot-blocker scan and clean-up.
func (handlers *AdminHandlers) mountModeration(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionCustomersRead)).Group(func(read chi.Router) {
		read.Get("/moderation/blockers", handlers.blockerState)
		read.Get("/moderation/ban-templates", handlers.banTemplates)
	})
	secure.With(handlers.requirePermission(rbac.PermissionCustomersWrite)).Group(func(write chi.Router) {
		write.Post("/moderation/bans", handlers.banCustomers)
		write.Post("/moderation/blocker-scan", handlers.requestBlockerScan)
		write.Post("/moderation/blockers/clean", handlers.cleanBlockers)
	})
}

func (handlers *AdminHandlers) blockerState(writer http.ResponseWriter, request *http.Request) {
	state, err := handlers.operations.BlockerState(request.Context())
	handlers.respond(writer, request, state, err)
}

func (handlers *AdminHandlers) banTemplates(writer http.ResponseWriter, request *http.Request) {
	keys := make([]string, 0, len(panelpg.BanTemplates))
	for key := range panelpg.BanTemplates {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	items := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		items = append(items, map[string]string{"key": key, "text": panelpg.BanTemplates[key]})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handlers *AdminHandlers) banCustomers(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.BanInput
	if !decodeJSON(writer, request, &body) {
		return
	}
	results, err := handlers.operations.BanCustomers(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, map[string]any{"items": results}, err)
}

func (handlers *AdminHandlers) requestBlockerScan(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.RequestBlockerScan(request.Context(), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"requested": true}, err)
}

func (handlers *AdminHandlers) cleanBlockers(writer http.ResponseWriter, request *http.Request) {
	cleaned, err := handlers.operations.CleanBlockers(request.Context(), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"suspended": cleaned}, err)
}
