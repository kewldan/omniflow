package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountServers registers the server catalogue: Omniflow's labels and sale
// limits over Remnawave internal squads. Nothing here changes a squad or a
// node in the panel (decision 0004).
func (handlers *AdminHandlers) mountServers(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionCatalogRead)).
		Get("/catalog/servers", handlers.servers)
	secure.With(handlers.requirePermission(rbac.PermissionCatalogWrite)).
		Put("/catalog/servers/{squadID}", handlers.saveServer)
	secure.With(handlers.requirePermission(rbac.PermissionCatalogWrite)).
		Delete("/catalog/servers/{squadID}", handlers.deleteServer)
}

// serverRow is one squad as the catalogue screen shows it: the catalogue
// entry, when there is one, beside the panel's own name and member count,
// read live and never stored.
type serverRow struct {
	SquadID        string          `json:"squadId"`
	PanelName      string          `json:"panelName,omitempty"`
	PanelMembers   *int            `json:"panelMembers,omitempty"`
	InPanel        bool            `json:"inPanel"`
	Catalogued     bool            `json:"catalogued"`
	Entry          *panelpg.Server `json:"entry,omitempty"`
	CapacityFilled bool            `json:"capacityFilled"`
}

func (handlers *AdminHandlers) servers(writer http.ResponseWriter, request *http.Request) {
	entries, err := handlers.operations.Servers(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	rows := make([]serverRow, 0, len(entries))
	index := make(map[string]int, len(entries))
	for _, entry := range entries {
		item := entry
		index[entry.SquadID] = len(rows)
		rows = append(rows, serverRow{
			SquadID: entry.SquadID, Catalogued: true, Entry: &item,
			CapacityFilled: entry.MaxUsers != nil && entry.LiveCustomers >= *entry.MaxUsers,
		})
	}
	reported := false
	if handlers.remnawave != nil {
		squads, squadErr := handlers.remnawave.InternalSquads(request.Context())
		if squadErr != nil {
			handlers.logger.Warn("remnawave squad listing failed", "error", squadErr)
		} else {
			reported = true
			for _, squad := range squads {
				members := squad.Members
				position, known := index[squad.UUID]
				if !known {
					position = len(rows)
					rows = append(rows, serverRow{SquadID: squad.UUID})
				}
				rows[position].PanelName, rows[position].PanelMembers, rows[position].InPanel = squad.Name, &members, true
			}
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"reported": reported, "items": rows})
}

func (handlers *AdminHandlers) saveServer(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.Server
	if !decodeJSON(writer, request, &body) {
		return
	}
	body.SquadID = chi.URLParam(request, "squadID")
	saved, err := handlers.operations.SaveServer(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, saved, err)
}

func (handlers *AdminHandlers) deleteServer(writer http.ResponseWriter, request *http.Request) {
	if err := handlers.operations.DeleteServer(request.Context(), chi.URLParam(request, "squadID"), actorFrom(request)); err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
