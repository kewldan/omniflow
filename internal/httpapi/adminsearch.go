package httpapi

import (
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelsearch"
	"github.com/omniflow/omniflow/internal/panelsearchpg"
)

// Record search for the command palette.
//
// One route, no permission of its own: every result group is governed by the
// read permission of the screen it links to, and a group the caller cannot
// read is neither queried nor mentioned in the response. Reads are not audited
// here, matching every other read in the panel; the trail records changes and
// exports, and a search is neither.

func (handlers *AdminHandlers) mountSearch(secure chi.Router) {
	if handlers.search == nil {
		return
	}
	secure.Get("/search", handlers.searchRecords)
}

func (handlers *AdminHandlers) searchRecords(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	// The palette searches as the operator types, debounced. Sixty a minute is
	// a fast typist with room to spare, and stops a script from walking the
	// identifier space through it.
	if !handlers.allow(request, "admin-search", principal.Account.ID, 60, time.Minute) {
		writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many searches. Wait a moment.")
		return
	}
	visible := panelsearch.Visible(principal.Grant)
	query, searchable := panelsearch.Parse(request.URL.Query().Get("q"))
	if !searchable || len(visible) == 0 {
		writeJSON(writer, http.StatusOK, searchPayload(panelsearchpg.Results{}, visible, query))
		return
	}
	results, err := handlers.search.Search(request.Context(), query, visible)
	if err != nil {
		handlers.logger.Error("panel search failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "search_unavailable", "Search is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, searchPayload(results, visible, query))
}

func searchPayload(results panelsearchpg.Results, visible []panelsearch.Group, query panelsearch.Query) map[string]any {
	groups := make([]map[string]any, 0, len(visible))
	for _, group := range panelsearch.Groups {
		if !slices.Contains(visible, group) {
			continue
		}
		items := make([]map[string]any, 0, panelsearch.GroupLimit)
		switch group {
		case panelsearch.GroupCustomers:
			for _, hit := range results.Customers {
				item := map[string]any{
					"id": hit.ID, "status": hit.Status, "createdAt": hit.CreatedAt.UTC(), "matchedBy": hit.MatchedBy,
				}
				if hit.TelegramID != 0 {
					item["telegramId"] = hit.TelegramID
				}
				items = append(items, item)
			}
		case panelsearch.GroupOrders:
			for _, hit := range results.Orders {
				item := map[string]any{
					"id": hit.ID, "customerId": hit.CustomerID, "state": hit.State, "operation": hit.Operation,
					"currency": hit.Currency, "totalMinor": hit.TotalMinor, "createdAt": hit.CreatedAt.UTC(),
					"matchedBy": hit.MatchedBy,
				}
				if hit.Provider != "" {
					item["provider"] = hit.Provider
				}
				items = append(items, item)
			}
		case panelsearch.GroupTickets:
			for _, hit := range results.Tickets {
				items = append(items, map[string]any{
					"id": hit.ID, "customerId": hit.CustomerID, "subject": hit.Subject, "status": hit.Status,
					"lastMessageAt": hit.LastMessageAt.UTC(), "matchedBy": hit.MatchedBy,
				})
			}
		case panelsearch.GroupGifts:
			for _, hit := range results.Gifts {
				items = append(items, map[string]any{
					"id": hit.ID, "status": hit.Status, "kind": hit.Kind, "codeHint": hit.CodeHint,
					"expiresAt": hit.ExpiresAt.UTC(), "matchedBy": "code",
				})
			}
		case panelsearch.GroupAccessCodes:
			for _, hit := range results.AccessCodes {
				items = append(items, map[string]any{
					"id": hit.ID, "status": hit.Status, "codeHint": hit.CodeHint,
					"batchId": hit.BatchID, "batchReference": hit.BatchReference, "matchedBy": "code",
				})
			}
		}
		groups = append(groups, map[string]any{"group": string(group), "items": items})
	}
	hints := []string{}
	if results.HandleNotStored || query.Handle != "" {
		hints = append(hints, "telegram_handle_not_stored")
	}
	return map[string]any{"groups": groups, "hints": hints}
}
