package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/rbac"
)

// liveInterval is how often the stream re-reads the counters.
const liveInterval = 3 * time.Second

// liveMaxDuration ends a stream so a browser reconnects through the session
// check now and then; EventSource reconnects on its own.
const liveMaxDuration = 10 * time.Minute

// mountLive serves the panel's live updates: a server-sent event stream that
// says when something the operator can see changed — a ticket message, a
// paid order, an application error — so open screens refresh without
// polling each list. It carries version counters, never records: the screen
// re-reads what it shows through the routes that check its permissions.
func (handlers *AdminHandlers) mountLive(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.Get("/live", handlers.liveUpdates)
}

func (handlers *AdminHandlers) liveUpdates(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeProblem(writer, request, http.StatusNotImplemented, "streaming_unsupported", "Streaming is not available")
		return
	}
	principal, _ := PrincipalFrom(request.Context())
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	// A server-sent stream outlives the server's write timeout; the
	// controller lifts it for this response only.
	_ = http.NewResponseController(writer).SetWriteDeadline(time.Time{})

	ticker := time.NewTicker(liveInterval)
	defer ticker.Stop()
	deadline := time.After(liveMaxDuration)
	previous := map[string]int64{}
	for {
		versions, err := handlers.operations.LiveVersions(request.Context())
		if err == nil {
			changed := map[string]int64{}
			for topic, permission := range map[string]rbac.Permission{
				"support": rbac.PermissionSupportRead, "finance": rbac.PermissionFinanceRead,
				"errors": rbac.PermissionSystemRead, "customers": rbac.PermissionCustomersRead,
			} {
				if principal.Grant.Allows(permission) && versions[topic] != previous[topic] {
					changed[topic] = versions[topic]
					previous[topic] = versions[topic]
				}
			}
			if len(changed) > 0 {
				payload, _ := json.Marshal(changed)
				fmt.Fprintf(writer, "event: change\ndata: %s\n\n", payload)
			} else {
				fmt.Fprint(writer, ": keep-alive\n\n")
			}
			flusher.Flush()
		}
		select {
		case <-request.Context().Done():
			return
		case <-deadline:
			return
		case <-ticker.C:
		}
	}
}
