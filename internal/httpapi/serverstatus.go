package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// NodeLister is the one Remnawave read the status page needs.
type NodeLister interface {
	Nodes(ctx context.Context) ([]remnawave.Node, error)
}

// ServerStatusHandler publishes which servers are up, for customers deciding
// whether a problem is theirs or the service's.
//
// It is read-only and opt-in. It shows a node's name, its country label, and
// whether the panel reports it connected — never an address, a traffic figure,
// or a user count — and nothing is stored: the answer is the panel's, cached
// for a minute so a busy page cannot turn into load on Remnawave.
type ServerStatusHandler struct {
	panel  NodeLister
	logger *slog.Logger
	ttl    time.Duration

	mu      sync.Mutex
	cached  []statusNode
	fetched time.Time
}

type statusNode struct {
	Name        string `json:"name"`
	CountryCode string `json:"countryCode,omitempty"`
	Online      bool   `json:"online"`
}

// NewServerStatusHandler returns nil unless APP_SERVER_STATUS_ENABLED is true.
func NewServerStatusHandler(panel NodeLister, logger *slog.Logger, getenv func(string) string) *ServerStatusHandler {
	enabled, _ := strconv.ParseBool(strings.TrimSpace(getenv("APP_SERVER_STATUS_ENABLED")))
	if !enabled || panel == nil {
		return nil
	}
	return &ServerStatusHandler{panel: panel, logger: logger, ttl: time.Minute}
}

// Mount registers GET /v1/status/servers.
func (handler *ServerStatusHandler) Mount(router chi.Router) {
	router.Get("/v1/status/servers", handler.servers)
}

func (handler *ServerStatusHandler) servers(writer http.ResponseWriter, request *http.Request) {
	nodes, checkedAt, err := handler.read(request.Context())
	if err != nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "status_unavailable", "Server status is unavailable right now")
		return
	}
	writer.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(writer, http.StatusOK, map[string]any{
		"servers": nodes, "checkedAt": checkedAt.UTC().Format(time.RFC3339),
	})
}

func (handler *ServerStatusHandler) read(ctx context.Context) ([]statusNode, time.Time, error) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.cached != nil && time.Since(handler.fetched) < handler.ttl {
		return handler.cached, handler.fetched, nil
	}
	nodes, err := handler.panel.Nodes(ctx)
	if err != nil {
		handler.logger.Warn("server status read failed", "error", err)
		return nil, time.Time{}, err
	}
	projected := make([]statusNode, 0, len(nodes))
	for _, node := range nodes {
		// A node the operator disabled is not a server customers use; it is
		// left off rather than shown as down.
		if node.IsDisabled {
			continue
		}
		projected = append(projected, statusNode{Name: node.Name, CountryCode: node.CountryCode, Online: node.IsConnected})
	}
	sort.SliceStable(projected, func(i, j int) bool { return projected[i].Name < projected[j].Name })
	handler.cached, handler.fetched = projected, time.Now()
	return projected, handler.fetched, nil
}
