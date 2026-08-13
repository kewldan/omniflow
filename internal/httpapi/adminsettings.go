package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/aigateway"
	"github.com/omniflow/omniflow/internal/aigovernance"
	"github.com/omniflow/omniflow/internal/airuntime"
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

		read.Get("/settings/telemetry/preview", handlers.telemetryPreview)
		read.Get("/settings/backups", handlers.backupHistory)

		read.Get("/settings/mcp/servers", handlers.mcpServers)
		read.Get("/settings/mcp/servers/{slug}/tools", handlers.mcpTools)
		read.Get("/settings/mcp/events", handlers.mcpEvents)
	})

	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).Group(func(write chi.Router) {
		write.Put("/settings/{section}", handlers.saveSettingSection)

		write.Put("/settings/ai/providers", handlers.saveAIProvider)
		write.Delete("/settings/ai/providers/{slug}", handlers.deleteAIProvider)
		// Testing a provider sits behind the write permission rather than the
		// read one. It opens a sealed credential and spends the installation's
		// money, and neither is something a read of the settings screen should
		// be able to do.
		write.Post("/settings/ai/providers/{slug}/test", handlers.testAIProvider)
		write.Put("/settings/ai/features/{feature}", handlers.configureAIFeature)
		write.Put("/settings/ai/limits", handlers.saveAILimit)
		write.Delete("/settings/ai/limits/{limitID}", handlers.deleteAILimit)

		write.Put("/settings/mcp/servers", handlers.saveMCPServer)
		write.Delete("/settings/mcp/servers/{slug}", handlers.deleteMCPServer)
		write.Put("/settings/mcp/servers/{slug}/tools/{tool}", handlers.setMCPToolPolicy)
	})

	// The diagnostics bundle is a system artefact rather than a setting: it
	// describes the running installation, and the people who need it are the
	// people already looking at system health.
	secure.With(handlers.requirePermission(rbac.PermissionSystemRead)).Group(func(read chi.Router) {
		read.Get("/settings/diagnostics", handlers.diagnostics)
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

// testAIProvider makes one real request against a stored credential.
//
// It exists because "is this configured correctly?" had no answer. The column
// that holds the result, the query that writes it, and the panel line that
// renders it all shipped together, and nothing ever called them, so every
// provider read *Never connection-tested* for as long as it was configured.
//
// A test that could not be attempted and a test that ran and failed are
// different answers. The first is a 4xx and stores nothing — recording a check
// against a provider with no key would put a failure on the row when the truth
// is that nothing was tried. The second is a 200 carrying `ok: false`, because
// the test did happen and its outcome is exactly what the operator asked for.
func (handlers *AdminHandlers) testAIProvider(writer http.ResponseWriter, request *http.Request) {
	if handlers.ai == nil {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That record does not exist")
		return
	}
	slug := chi.URLParam(request, "slug")

	// A model call costs money, so the button is bounded per operator. The limit
	// is generous enough for the paste-test-fix loop an owner actually runs and
	// small enough that a stuck retry cannot spend an afternoon's budget.
	if !handlers.allow(request, "ai-provider-test", actorFrom(request).AdminID, 30, time.Hour) {
		writeProblem(
			writer, request, http.StatusTooManyRequests,
			"rate_limited", "Too many connection tests. Try again shortly",
		)
		return
	}

	var body struct {
		// Model is optional. An owner testing a provider some feature already
		// points at should not have to repeat what that feature named.
		Model string `json:"model"`
	}
	if request.ContentLength > 0 && !decodeJSON(writer, request, &body) {
		return
	}

	detail, err := handlers.ai.Probe(request.Context(), slug, body.Model)
	switch {
	case errors.Is(err, panelpg.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That provider is not registered")
		return
	case errors.Is(err, airuntime.ErrProviderUnconfigured):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"provider_unconfigured", "Store an API key for this provider before testing it",
		)
		return
	case errors.Is(err, airuntime.ErrModelUnknown):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"model_required", "No feature names a model for this provider yet. Name one to test with",
		)
		return
	case errors.Is(err, airuntime.ErrProviderKindUnknown):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"provider_kind_unknown", "This build has no adapter for that provider kind",
		)
		return
	}

	succeeded := err == nil
	if !succeeded {
		detail = probeFailure(err)
		handlers.logger.Warn("ai provider connection test failed", "provider", slug, "error", err)
	}
	if recordErr := handlers.operations.RecordAIProviderCheck(
		request.Context(), slug, succeeded, detail, actorFrom(request),
	); recordErr != nil {
		handlers.operationsError(writer, request, recordErr)
		return
	}

	provider, err := handlers.operations.AIProviders(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	// The whole row goes back rather than a bare verdict, so the screen renders
	// the stored timestamp it will show on the next load instead of a local one
	// that could disagree with it.
	for _, candidate := range provider {
		if candidate.Slug == slug {
			writeJSON(writer, http.StatusOK, candidate)
			return
		}
	}
	writeProblem(writer, request, http.StatusNotFound, "not_found", "That provider is not registered")
}

// probeFailure turns a failed test into a sentence safe to store and show.
//
// The provider's own message is never used, for the reason the adapters already
// refuse to propagate it: it can echo the prompt back. Neither is the transport
// error, and that one is less obvious — a URL error carries the address it
// dialled, and the Gemini adapter carries the API key in the query string, so
// storing a raw dial failure would write a live credential into the audit and
// onto a screen.
func probeFailure(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the provider did not answer within " + probeTimeoutText
	case errors.Is(err, aigateway.ErrProviderRejected):
		// Constructed by the adapter and carries a status code and nothing else.
		return err.Error()
	default:
		return "the provider could not be reached"
	}
}

// probeTimeoutText renders the probe's own bound for an operator. It is written
// out rather than derived so the sentence reads as English.
const probeTimeoutText = "15 seconds"

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

func (handlers *AdminHandlers) diagnostics(writer http.ResponseWriter, request *http.Request) {
	bundle, err := handlers.operations.Diagnostics(request.Context(), handlers.version)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	// The update line is attached here rather than assembled in panelpg, which
	// makes no network requests. The checker answers from cache most of the
	// time and reports its own failures, so a feed that is down slows this
	// response by at most one bounded attempt and never fails it.
	if handlers.updates != nil {
		status := handlers.updates.Status(request.Context())
		bundle.Update = &status
	}
	writeJSON(writer, http.StatusOK, bundle)
}

func (handlers *AdminHandlers) telemetryPreview(writer http.ResponseWriter, request *http.Request) {
	// The enablement and endpoint come from the settings section rather than
	// from process configuration, so the preview describes what this
	// installation would send rather than what the binary was started with.
	section, err := handlers.operations.SettingSection(request.Context(), "telemetry")
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	var configured struct {
		Enabled  bool   `json:"enabled"`
		Endpoint string `json:"endpoint"`
	}
	_ = json.Unmarshal(section.Document, &configured)

	preview, err := handlers.operations.TelemetryPreview(
		request.Context(), configured.Enabled, configured.Endpoint, handlers.version)
	handlers.respond(writer, request, preview, err)
}

func (handlers *AdminHandlers) backupHistory(writer http.ResponseWriter, request *http.Request) {
	backups, restores, err := handlers.operations.BackupHistory(
		request.Context(), queryInt(request, "limit"))
	// A backup nobody has ever restored is a backup nobody knows works, so the
	// restore history is returned alongside rather than on a separate screen.
	handlers.respond(writer, request, map[string]any{
		"backups": backups, "restores": restores,
	}, err)
}
