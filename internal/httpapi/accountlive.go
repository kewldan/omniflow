package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// accountLiveInterval is how often the customer stream re-reads its versions.
// It is the delay an operator's support reply takes to appear in an open
// conversation, which is why it is shorter than the panel's.
const accountLiveInterval = 3 * time.Second

// accountLiveMaxDuration ends a stream so the browser reconnects through the
// session check: a session revoked elsewhere stops receiving updates within
// this bound. The router's request timeout ends it sooner, after a minute;
// either way EventSource reconnects on its own, and the versions it last saw
// travel back as Last-Event-ID, so nothing that changed in between is missed.
const accountLiveMaxDuration = 5 * time.Minute

// encodeLiveVersions writes versions as an event ID: `topic=version` pairs in
// a fixed order. The values are digits and dots, so the ID is header-safe.
func encodeLiveVersions(versions map[string]string) string {
	topics := make([]string, 0, len(versions))
	for topic := range versions {
		topics = append(topics, topic)
	}
	sort.Strings(topics)
	pairs := make([]string, 0, len(topics))
	for _, topic := range topics {
		pairs = append(pairs, topic+"="+versions[topic])
	}
	return strings.Join(pairs, ";")
}

// decodeLiveVersions reads an event ID back. Anything unreadable yields nil,
// which makes the stream take a fresh baseline.
func decodeLiveVersions(value string) map[string]string {
	if value == "" || len(value) > 512 {
		return nil
	}
	versions := map[string]string{}
	for _, pair := range strings.Split(value, ";") {
		topic, version, found := strings.Cut(pair, "=")
		if !found || topic == "" {
			return nil
		}
		versions[topic] = version
	}
	return versions
}

// mountLive serves the customer panel's live updates: a server-sent event
// stream that says which areas of the signed-in customer's own panel changed —
// a support reply, an order or payment moving, a subscription being
// provisioned, renewed or paused, a wallet entry — so open screens refresh at
// once. Like the operator panel's stream it carries versions, never records,
// and every version is about this customer alone; the screen re-reads what it
// shows through its ordinary routes. Decision 0015 records why this is SSE.
func (handlers *AccountHandlers) mountLive(secure chi.Router) {
	if handlers.account == nil {
		return
	}
	secure.Get("/live", handlers.liveUpdates)
}

func (handlers *AccountHandlers) liveUpdates(writer http.ResponseWriter, request *http.Request) {
	principal, ok := CustomerFrom(request.Context())
	if !ok {
		writeProblem(writer, request, http.StatusUnauthorized, "unauthenticated", "Sign in first")
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeProblem(writer, request, http.StatusNotImplemented, "streaming_unsupported", "Streaming is not available")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(writer).SetWriteDeadline(time.Time{})

	ticker := time.NewTicker(accountLiveInterval)
	defer ticker.Stop()
	deadline := time.After(accountLiveMaxDuration)
	// A reconnecting browser names the versions it last saw; a fresh one
	// takes the first read as its baseline, since the screen already holds
	// what it just loaded.
	previous := decodeLiveVersions(request.Header.Get("Last-Event-ID"))
	for {
		versions, err := handlers.account.LiveVersions(request.Context(), principal.Customer.ID)
		if err == nil {
			changed := map[string]string{}
			baseline := previous == nil
			for topic, version := range versions {
				if !baseline && previous[topic] != version {
					changed[topic] = version
				}
			}
			previous = versions
			switch {
			case baseline:
				fmt.Fprintf(writer, "id: %s\nevent: ready\ndata: {}\n\n", encodeLiveVersions(versions))
			case len(changed) > 0:
				payload, _ := json.Marshal(changed)
				fmt.Fprintf(writer, "id: %s\nevent: change\ndata: %s\n\n", encodeLiveVersions(versions), payload)
			default:
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
