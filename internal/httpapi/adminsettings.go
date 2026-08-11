package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/aigovernance"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountSettings registers the installation configuration surfaces.
//
// Reading and writing are separate permissions throughout, because reading a
// setting is often part of doing a job — a support operator needs to know the
// quiet hours — and changing one is always an owner-level act.
//
// Every write here goes through the same version guard and produces the same
// kind of audit entry, so "who changed the Remnawave token and when?" has one
// answer regardless of which screen did it.
func (handlers *AdminHandlers) mountSettings(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).Group(func(read chi.Router) {
		read.Get("/settings", handlers.settingSections)
		read.Get("/settings/{section}", handlers.settingSection)

		read.Get("/settings/ai/providers", handlers.aiProviders)
		read.Get("/settings/ai/features", handlers.aiFeatures)
		read.Get("/settings/ai/limits", handlers.aiLimits)
		read.Get("/settings/ai/usage", handlers.aiUsage)

		read.Get("/settings/mcp/servers", handlers.mcpServers)
		read.Get("/settings/mcp/servers/{slug}/tools", handlers.mcpTools)
		read.Get("/settings/mcp/events", handlers.mcpEvents)
	})

	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).Group(func(write chi.Router) {
		write.Put("/settings/{section}", handlers.saveSettingSection)

		write.Put("/settings/ai/providers", handlers.saveAIProvider)
		write.Delete("/settings/ai/providers/{slug}", handlers.deleteAIProvider)
		write.Put("/settings/ai/features/{feature}", handlers.configureAIFeature)
		write.Put("/settings/ai/limits", handlers.saveAILimit)
		write.Delete("/settings/ai/limits/{limitID}", handlers.deleteAILimit)

		write.Put("/settings/mcp/servers", handlers.saveMCPServer)
		write.Delete("/settings/mcp/servers/{slug}", handlers.deleteMCPServer)
		write.Put("/settings/mcp/servers/{slug}/tools/{tool}", handlers.setMCPToolPolicy)
	})

	// The AI decision export identifies consequential decisions a model shaped.
	// It sits behind the audit export permission rather than the settings one:
	// it is an audit artefact, and the people who need it are the people who
	// already export audit.
	secure.With(handlers.requirePermission(rbac.PermissionAuditExport)).Group(func(read chi.Router) {
		read.Get("/settings/ai/decisions", handlers.aiDecisions)
	})
}

func (handlers *AdminHandlers) settingSections(writer http.ResponseWriter, request *http.Request) {
	sections, err := handlers.operations.SettingSections(request.Context())
	handlers.respond(writer, request, map[string]any{"items": sections}, err)
}

func (handlers *AdminHandlers) settingSection(writer http.ResponseWriter, request *http.Request) {
	section, err := handlers.operations.SettingSection(
		request.Context(), chi.URLParam(request, "section"))
	handlers.respond(writer, request, section, err)
}

func (handlers *AdminHandlers) saveSettingSection(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Document json.RawMessage `json:"document"`
		// Version is what the operator's screen was showing. A save that does
		// not match is a conflict rather than an overwrite.
		Version int32 `json:"version"`
		// Secrets are write-only. An absent map leaves the stored credential
		// alone, which is what lets the panel render "configured" and never
		// round-trip the value.
		Secrets map[string]string `json:"secrets"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	section, err := handlers.operations.SaveSettingSection(
		request.Context(), chi.URLParam(request, "section"),
		body.Document, body.Version, body.Secrets, actorFrom(request))
	handlers.respond(writer, request, section, err)
}

func (handlers *AdminHandlers) aiProviders(writer http.ResponseWriter, request *http.Request) {
	providers, err := handlers.operations.AIProviders(request.Context())
	handlers.respond(writer, request, map[string]any{"items": providers}, err)
}

func (handlers *AdminHandlers) saveAIProvider(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		panelpg.AIProvider
		// APIKey is accepted and never returned. It is a separate field from the
		// embedded provider so the response type physically cannot carry it.
		APIKey string `json:"apiKey"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	provider, err := handlers.operations.SaveAIProvider(
		request.Context(), body.AIProvider, body.APIKey, actorFrom(request))
	handlers.respond(writer, request, provider, err)
}

func (handlers *AdminHandlers) deleteAIProvider(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteAIProvider(
		request.Context(), chi.URLParam(request, "slug"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"deleted": true}, err)
}

func (handlers *AdminHandlers) aiFeatures(writer http.ResponseWriter, request *http.Request) {
	features, err := handlers.operations.AIFeatures(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	// The complete list of features is published alongside what is configured,
	// so a screen renders every switch an installation could have rather than
	// only the ones somebody has already touched.
	writeJSON(writer, http.StatusOK, map[string]any{
		"items": features, "available": aigovernance.All(),
	})
}

func (handlers *AdminHandlers) configureAIFeature(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.AIFeature
	if !decodeJSON(writer, request, &body) {
		return
	}
	body.Feature = chi.URLParam(request, "feature")
	feature, err := handlers.operations.ConfigureAIFeature(
		request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, feature, err)
}

func (handlers *AdminHandlers) aiLimits(writer http.ResponseWriter, request *http.Request) {
	limits, err := handlers.operations.AIUsageLimits(request.Context())
	handlers.respond(writer, request, map[string]any{"items": limits}, err)
}

func (handlers *AdminHandlers) saveAILimit(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.AIUsageLimit
	if !decodeJSON(writer, request, &body) {
		return
	}
	limit, err := handlers.operations.SaveAIUsageLimit(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, limit, err)
}

func (handlers *AdminHandlers) deleteAILimit(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteAIUsageLimit(
		request.Context(), chi.URLParam(request, "limitID"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"deleted": true}, err)
}

// defaultReportWindow is what a usage report covers when the caller names no
// range. Thirty days is long enough to show a trend and short enough that the
// query stays on the index.
const defaultReportWindow = 30 * 24 * time.Hour

func (handlers *AdminHandlers) aiUsage(writer http.ResponseWriter, request *http.Request) {
	until := time.Now().UTC()
	if parsed := queryTime(request, "until"); parsed != nil {
		until = *parsed
	}
	since := until.Add(-defaultReportWindow)
	if parsed := queryTime(request, "since"); parsed != nil {
		since = *parsed
	}

	report, err := handlers.operations.AIUsageReport(request.Context(), since, until)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	// The window is published beside the numbers, because a figure whose period
	// the reader has to guess is a figure they will misread.
	writeJSON(writer, http.StatusOK, map[string]any{
		"since": since, "until": until, "items": report,
	})
}

func (handlers *AdminHandlers) aiDecisions(writer http.ResponseWriter, request *http.Request) {
	until := time.Now().UTC()
	if parsed := queryTime(request, "until"); parsed != nil {
		until = *parsed
	}
	since := until.Add(-defaultReportWindow)
	if parsed := queryTime(request, "since"); parsed != nil {
		since = *parsed
	}

	decisions, err := handlers.operations.ExportAIDecisions(
		request.Context(), since, until,
		query(request, "consequentialOnly") == "true", queryInt(request, "pageSize"))
	handlers.respond(writer, request, map[string]any{
		"since": since, "until": until, "items": decisions,
	}, err)
}

func (handlers *AdminHandlers) mcpServers(writer http.ResponseWriter, request *http.Request) {
	servers, err := handlers.operations.MCPServers(request.Context())
	handlers.respond(writer, request, map[string]any{"items": servers}, err)
}

func (handlers *AdminHandlers) saveMCPServer(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		panelpg.MCPServerConfig
		// Credential is write-only for the same reason an API key is.
		Credential string `json:"credential"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	server, err := handlers.operations.SaveMCPServer(
		request.Context(), body.MCPServerConfig, body.Credential, actorFrom(request))
	handlers.respond(writer, request, server, err)
}

func (handlers *AdminHandlers) deleteMCPServer(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteMCPServer(
		request.Context(), chi.URLParam(request, "slug"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"deleted": true}, err)
}

func (handlers *AdminHandlers) mcpTools(writer http.ResponseWriter, request *http.Request) {
	tools, err := handlers.operations.MCPTools(request.Context(), chi.URLParam(request, "slug"))
	handlers.respond(writer, request, map[string]any{"items": tools}, err)
}

func (handlers *AdminHandlers) setMCPToolPolicy(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Enabled    bool   `json:"enabled"`
		Permission string `json:"permission"`
		Writes     bool   `json:"writes"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	// An owner cannot map a tool to a permission that does not exist: a typo
	// would otherwise produce a tool nobody can use, which reads as a bug rather
	// than as a mistake.
	if !rbac.Known(rbac.Permission(strings.TrimSpace(body.Permission))) {
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"unknown_permission", "That is not a permission this installation defines",
		)
		return
	}

	tool, err := handlers.operations.SetMCPToolPolicy(request.Context(), panelpg.MCPToolPolicy{
		Server: chi.URLParam(request, "slug"), Tool: chi.URLParam(request, "tool"),
		Enabled: body.Enabled, Permission: strings.TrimSpace(body.Permission),
		Writes: body.Writes,
	}, actorFrom(request))
	handlers.respond(writer, request, tool, err)
}

func (handlers *AdminHandlers) mcpEvents(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.MCPEvents(
		request.Context(), query(request, "server"), query(request, "outcome"),
		query(request, "cursor"), queryInt(request, "pageSize"))
	handlers.respond(writer, request, page, err)
}
