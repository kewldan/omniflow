package panelpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/aigovernance"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/mcp"
)

// Associated-data labels for the settings and connection secrets this file
// seals. Each column gets its own label, so a ciphertext moved between them
// fails to open rather than decrypting somewhere it does not belong.
const (
	SecretSettingsSection = "panel.settings_section"
	SecretAIProvider      = "panel.ai_provider"
	SecretMCPServer       = "panel.mcp_server"
)

// SettingSection is one screen's worth of configuration.
//
// The document is whatever that screen saves, validated by the handler that
// owns it rather than by a schema here: the sections have little in common and
// a shared shape would be a lowest common denominator that fits none of them.
type SettingSection struct {
	Section string `json:"section"`
	// Document never contains a secret. That is what makes returning it safe,
	// and it is enforced by the query rather than by a filter — the secret
	// column is not selected.
	Document json.RawMessage `json:"document"`
	// SecretConfigured says whether a credential exists without saying what it
	// is. "Configured" is the only safe rendering of a secret.
	SecretConfigured bool      `json:"secretConfigured"`
	Version          int32     `json:"version"`
	UpdatedAt        time.Time `json:"updatedAt"`
	UpdatedBy        string    `json:"updatedBy,omitempty"`
}

// Sections an owner can configure. The list is compiled in so the settings
// screen renders every one an installation could have, rather than only the
// ones somebody has already saved.
var settingSections = map[string]bool{
	"branding": true, "remnawave": true, "telegram": true, "operator_group": true,
	"required_channels": true, "maintenance": true, "notifications": true,
	"telemetry": true, "backup": true, "security": true, "ai": true, "mcp": true,
}

// SettingSections reads every section without its secrets.
func (service *Service) SettingSections(ctx context.Context) ([]SettingSection, error) {
	queries := service.queries()
	rows, err := queries.ListSettingSections(ctx)
	if err != nil {
		return nil, err
	}
	present, err := queries.SettingSecretsPresent(ctx)
	if err != nil {
		return nil, err
	}
	configured := make(map[string]bool, len(present))
	for _, row := range present {
		configured[row.Section] = row.Configured
	}

	sections := make([]SettingSection, 0, len(rows))
	for _, row := range rows {
		sections = append(sections, SettingSection{
			Section: row.Section, Document: row.Document, Version: row.Version,
			SecretConfigured: configured[row.Section],
			UpdatedAt:        row.UpdatedAt.Time, UpdatedBy: uuidString(row.UpdatedBy),
		})
	}
	return sections, nil
}

// SettingSection reads one section.
func (service *Service) SettingSection(
	ctx context.Context, section string,
) (SettingSection, error) {
	if !settingSections[section] {
		return SettingSection{}, ErrValidaton
	}
	row, err := service.queries().GetSettingSection(ctx, section)
	if err != nil {
		return SettingSection{}, notFound(err)
	}
	present, err := service.queries().SettingSecretsPresent(ctx)
	if err != nil {
		return SettingSection{}, err
	}
	configured := false
	for _, candidate := range present {
		if candidate.Section == section {
			configured = candidate.Configured
		}
	}
	return SettingSection{
		Section: row.Section, Document: row.Document, Version: row.Version,
		SecretConfigured: configured,
		UpdatedAt:        row.UpdatedAt.Time, UpdatedBy: uuidString(row.UpdatedBy),
	}, nil
}

// SaveSettingSection stores a section's document and, optionally, its secrets.
//
// The expected version turns two panel tabs saving the same screen into a
// conflict rather than a silent overwrite. Secrets are a separate parameter
// that is only written when present, which is what lets the panel show a
// credential as "configured" and never send it back.
func (service *Service) SaveSettingSection(
	ctx context.Context, section string, document json.RawMessage,
	expectedVersion int32, secrets map[string]string, actor Actor,
) (SettingSection, error) {
	if !settingSections[section] {
		return SettingSection{}, ErrValidaton
	}
	if !json.Valid(document) {
		return SettingSection{}, ErrValidaton
	}
	if len(document) == 0 {
		document = json.RawMessage("{}")
	}

	var saved SettingSection
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.SaveSettingSection(ctx, dbgen.SaveSettingSectionParams{
			Document: document, UpdatedBy: optionalUUID(actor.AdminID),
			Section: section, ExpectedVersion: expectedVersion,
		})
		if err != nil {
			// No row means the version did not match. Reporting a conflict is
			// what lets the panel re-read and show the operator what changed,
			// instead of quietly discarding somebody's edit.
			return conflicted(err)
		}

		if len(secrets) > 0 {
			encoded, err := json.Marshal(secrets)
			if err != nil {
				return err
			}
			sealed, err := service.sealSecret(string(encoded), SecretSettingsSection+"."+section)
			if err != nil {
				return err
			}
			if err := queries.SaveSettingSecrets(ctx, dbgen.SaveSettingSecretsParams{
				SecretsCiphertext: sealed, UpdatedBy: optionalUUID(actor.AdminID),
				Section: section,
			}); err != nil {
				return err
			}
		}

		saved = SettingSection{
			Section: row.Section, Document: row.Document, Version: row.Version,
			SecretConfigured: len(secrets) > 0,
			UpdatedAt:        row.UpdatedAt.Time, UpdatedBy: uuidString(row.UpdatedBy),
		}
		return appendAudit(ctx, queries, actor.audit(
			"settings.section.saved", "settings", "installation_setting", section,
			// The metadata records which secret names were rotated, never their
			// values: "somebody changed the bot token on Tuesday" is the
			// auditable fact.
			map[string]any{"version": row.Version, "secrets": secretNames(secrets)},
		))
	})
	if err != nil {
		return SettingSection{}, err
	}
	return saved, nil
}

// SettingSecrets opens a section's sealed values.
//
// It is exported for the processes that need a credential — the bot needs its
// token, the reconciler needs the Remnawave one — and it is never called by a
// handler that answers an operator.
func (service *Service) SettingSecrets(
	ctx context.Context, section string,
) (map[string]string, error) {
	ciphertext, err := service.queries().GetSettingSecrets(ctx, section)
	if err != nil {
		return nil, notFound(err)
	}
	if len(ciphertext) == 0 {
		return map[string]string{}, nil
	}
	plaintext, err := service.OpenSecret(ciphertext, SecretSettingsSection+"."+section)
	if err != nil {
		return nil, err
	}
	secrets := map[string]string{}
	if err := json.Unmarshal([]byte(plaintext), &secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}

func secretNames(secrets map[string]string) []string {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	return names
}

// AIProvider is one approved model backend as the panel sees it.
type AIProvider struct {
	Slug           string    `json:"slug"`
	Kind           string    `json:"kind"`
	DisplayName    string    `json:"displayName"`
	BaseURL        string    `json:"baseUrl,omitempty"`
	Enabled        bool      `json:"enabled"`
	ZeroRetention  bool      `json:"zeroRetention"`
	TrainsOnData   bool      `json:"trainsOnData"`
	RetentionNote  string    `json:"retentionNote,omitempty"`
	DataRegion     string    `json:"dataRegion,omitempty"`
	KeyConfigured  bool      `json:"keyConfigured"`
	LastCheckedAt  time.Time `json:"lastCheckedAt,omitzero"`
	LastCheckOK    bool      `json:"lastCheckOk"`
	LastCheckError string    `json:"lastCheckError,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// AIProviders lists what an owner has approved.
func (service *Service) AIProviders(ctx context.Context) ([]AIProvider, error) {
	rows, err := service.queries().ListAIProviders(ctx)
	if err != nil {
		return nil, err
	}
	providers := make([]AIProvider, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, AIProvider{
			Slug: row.Slug, Kind: row.Kind, DisplayName: row.DisplayName,
			BaseURL: row.BaseUrl.String, Enabled: row.Enabled,
			ZeroRetention: row.ZeroRetention, TrainsOnData: row.TrainsOnData,
			RetentionNote: row.RetentionNotice.String, DataRegion: row.DataRegion.String,
			KeyConfigured: row.CredentialConfigured,
			LastCheckedAt: row.LastCheckedAt.Time, LastCheckOK: row.LastCheckOk.Bool,
			LastCheckError: row.LastCheckDetail.String, UpdatedAt: row.UpdatedAt.Time,
		})
	}
	return providers, nil
}

// SaveAIProvider stores a provider and, when one is supplied, its key.
//
// The key is write-only: it is never returned, and an omitted key leaves the
// stored one alone so an owner editing a display name does not have to re-paste
// their credential.
func (service *Service) SaveAIProvider(
	ctx context.Context, provider AIProvider, apiKey string, actor Actor,
) (AIProvider, error) {
	if strings.TrimSpace(provider.Slug) == "" || strings.TrimSpace(provider.DisplayName) == "" {
		return AIProvider{}, ErrValidaton
	}
	switch provider.Kind {
	case "openai_compatible", "anthropic", "gemini":
	default:
		return AIProvider{}, ErrValidaton
	}
	// An OpenAI-compatible provider without an address cannot be reached, and
	// the whole point of that adapter is that the owner chooses the address.
	if provider.Kind == "openai_compatible" && strings.TrimSpace(provider.BaseURL) == "" {
		return AIProvider{}, ErrValidaton
	}

	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, err := queries.UpsertAIProvider(ctx, dbgen.UpsertAIProviderParams{
			Slug: provider.Slug, Kind: provider.Kind, DisplayName: provider.DisplayName,
			BaseUrl: optionalText(provider.BaseURL), Enabled: provider.Enabled,
			ZeroRetention: provider.ZeroRetention, TrainsOnData: provider.TrainsOnData,
			RetentionNotice: optionalText(provider.RetentionNote),
			DataRegion:      optionalText(provider.DataRegion),
			UpdatedBy:       optionalUUID(actor.AdminID),
		}); err != nil {
			return err
		}
		if strings.TrimSpace(apiKey) != "" {
			sealed, err := service.sealSecret(apiKey, SecretAIProvider+"."+provider.Slug)
			if err != nil {
				return err
			}
			if err := queries.SetAIProviderCredentials(ctx, dbgen.SetAIProviderCredentialsParams{
				CredentialsCiphertext: sealed, UpdatedBy: optionalUUID(actor.AdminID),
				Slug: provider.Slug,
			}); err != nil {
				return err
			}
		}
		return appendAudit(ctx, queries, actor.audit(
			"ai.provider.saved", "settings", "ai_provider", provider.Slug,
			map[string]any{
				"enabled": provider.Enabled, "kind": provider.Kind,
				"keyRotated": strings.TrimSpace(apiKey) != "",
			},
		))
	})
	if err != nil {
		return AIProvider{}, err
	}
	return service.aiProvider(ctx, provider.Slug)
}

func (service *Service) aiProvider(ctx context.Context, slug string) (AIProvider, error) {
	providers, err := service.AIProviders(ctx)
	if err != nil {
		return AIProvider{}, err
	}
	for _, provider := range providers {
		if provider.Slug == slug {
			return provider, nil
		}
	}
	return AIProvider{}, ErrNotFound
}

// AIProviderCredential opens a provider's key for the process that calls it.
func (service *Service) AIProviderCredential(
	ctx context.Context, slug string,
) (kind, baseURL, key string, err error) {
	row, err := service.queries().GetAIProviderCredentials(ctx, slug)
	if err != nil {
		return "", "", "", notFound(err)
	}
	key, err = service.OpenSecret(row.CredentialsCiphertext, SecretAIProvider+"."+slug)
	if err != nil {
		return "", "", "", err
	}
	return row.Kind, row.BaseUrl.String, key, nil
}

// RecordAIProviderCheck stores the outcome of a connection test, so "is this
// configured correctly?" is answerable without spending a real request.
func (service *Service) RecordAIProviderCheck(
	ctx context.Context, slug string, ok bool, detail string,
) error {
	return service.queries().RecordAIProviderCheck(ctx, dbgen.RecordAIProviderCheckParams{
		Ok: pgtype.Bool{Bool: ok, Valid: true}, Detail: optionalText(detail), Slug: slug,
	})
}

// DeleteAIProvider removes a provider. A feature still pointing at it is
// refused by the foreign key, which is the right answer: deleting a provider
// out from under an enabled feature would turn it off without saying so.
func (service *Service) DeleteAIProvider(ctx context.Context, slug string, actor Actor) error {
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if err := queries.DeleteAIProvider(ctx, slug); err != nil {
			return err
		}
		return appendAudit(ctx, queries, actor.audit(
			"ai.provider.deleted", "settings", "ai_provider", slug, nil,
		))
	})
}

// AIFeature is one feature's configuration with the warnings that apply to it.
type AIFeature struct {
	Feature  string `json:"feature"`
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	Temperature  *float64 `json:"temperature,omitempty"`
	MaxTokens    *int32   `json:"maxTokens,omitempty"`
	TimeoutMS    *int32   `json:"timeoutMs,omitempty"`
	BudgetTokens *int64   `json:"budgetTokens,omitempty"`
	BudgetWindow *int32   `json:"budgetWindowSeconds,omitempty"`
	BudgetCost   *int64   `json:"budgetCostMinor,omitempty"`

	RetainPrompts bool  `json:"retainPrompts"`
	RetainOutputs bool  `json:"retainOutputs"`
	RetentionDays int32 `json:"retentionDays"`

	// Warnings are computed rather than stored, so they cannot go stale against
	// the provider row.
	Warnings  []aigovernance.Warning `json:"warnings"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

// AIFeatures reads every feature with its warnings.
func (service *Service) AIFeatures(ctx context.Context) ([]AIFeature, error) {
	rows, err := service.queries().ListAIFeatures(ctx)
	if err != nil {
		return nil, err
	}
	providers, err := service.AIProviders(ctx)
	if err != nil {
		return nil, err
	}
	indexed := make(map[string]aigovernance.Provider, len(providers))
	for _, provider := range providers {
		indexed[provider.Slug] = aigovernance.Provider{
			Slug: provider.Slug, Kind: provider.Kind, Enabled: provider.Enabled,
			ZeroRetention: provider.ZeroRetention, TrainsOnData: provider.TrainsOnData,
			RetentionNote: provider.RetentionNote, DataRegion: provider.DataRegion,
			LastCheckOK: provider.LastCheckOK, LastCheckedAt: provider.LastCheckedAt,
			LastCheckError: provider.LastCheckError,
		}
	}

	features := make([]AIFeature, 0, len(rows))
	for _, row := range rows {
		feature := AIFeature{
			Feature: row.Feature, Enabled: row.Enabled,
			Provider: row.ProviderSlug.String, Model: row.Model.String,
			RetainPrompts: row.RetainPrompts, RetainOutputs: row.RetainOutputs,
			RetentionDays: row.RetentionDays, UpdatedAt: row.UpdatedAt.Time,
		}
		if row.Temperature.Valid {
			value, err := row.Temperature.Float64Value()
			if err == nil {
				temperature := value.Float64
				feature.Temperature = &temperature
			}
		}
		feature.MaxTokens = int32Pointer(row.MaxTokens)
		feature.TimeoutMS = int32Pointer(row.TimeoutMs)
		feature.BudgetTokens = int64Pointer(row.BudgetTokens)
		feature.BudgetWindow = int32Pointer(row.BudgetWindowSeconds)
		feature.BudgetCost = int64Pointer(row.BudgetCostMinor)
		feature.Warnings = aigovernance.Warnings(
			aigovernance.Feature{
				Name: row.Feature, Enabled: row.Enabled,
				Provider: row.ProviderSlug.String, Model: row.Model.String,
				RetainPrompts: row.RetainPrompts, RetainOutputs: row.RetainOutputs,
				RetentionDays: int(row.RetentionDays),
			},
			indexed[row.ProviderSlug.String],
		)
		features = append(features, feature)
	}
	return features, nil
}

// ConfigureAIFeature stores one feature's settings.
//
// A blocking warning refuses the save rather than being displayed alongside a
// feature that is now on and broken.
func (service *Service) ConfigureAIFeature(
	ctx context.Context, feature AIFeature, actor Actor,
) (AIFeature, error) {
	if feature.Enabled && (feature.Provider == "" || feature.Model == "") {
		return AIFeature{}, ErrValidaton
	}
	if feature.RetentionDays < 0 || feature.RetentionDays > 3650 {
		return AIFeature{}, ErrValidaton
	}

	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, err := queries.ConfigureAIFeature(ctx, dbgen.ConfigureAIFeatureParams{
			Enabled:             feature.Enabled,
			ProviderSlug:        optionalText(feature.Provider),
			Model:               optionalText(feature.Model),
			Temperature:         numericPointer(feature.Temperature),
			MaxTokens:           optionalInt4(feature.MaxTokens),
			TimeoutMs:           optionalInt4(feature.TimeoutMS),
			BudgetTokens:        optionalInt8(feature.BudgetTokens),
			BudgetWindowSeconds: optionalInt4(feature.BudgetWindow),
			BudgetCostMinor:     optionalInt8(feature.BudgetCost),
			RetainPrompts:       feature.RetainPrompts,
			RetainOutputs:       feature.RetainOutputs,
			RetentionDays:       feature.RetentionDays,
			UpdatedBy:           optionalUUID(actor.AdminID),
			Feature:             feature.Feature,
		}); err != nil {
			return notFound(err)
		}
		return appendAudit(ctx, queries, actor.audit(
			"ai.feature.configured", "settings", "ai_feature", feature.Feature,
			map[string]any{
				"enabled": feature.Enabled, "provider": feature.Provider,
				"model": feature.Model, "retainPrompts": feature.RetainPrompts,
				"retentionDays": feature.RetentionDays,
			},
		))
	})
	if err != nil {
		return AIFeature{}, err
	}

	features, err := service.AIFeatures(ctx)
	if err != nil {
		return AIFeature{}, err
	}
	for _, saved := range features {
		if saved.Feature == feature.Feature {
			return saved, nil
		}
	}
	return AIFeature{}, ErrNotFound
}

// AIPolicy assembles the runtime policy from what is stored.
//
// It is built per request rather than cached because an owner disabling a
// feature expects the next click to respect it, and the cost is two indexed
// reads on a path that is about to make a model call.
func (service *Service) AIPolicy(ctx context.Context) (*aigovernance.Policy, error) {
	features, err := service.queries().ListAIFeatures(ctx)
	if err != nil {
		return nil, err
	}
	configured := make([]aigovernance.Feature, 0, len(features))
	for _, row := range features {
		configured = append(configured, aigovernance.Feature{
			Name: row.Feature, Enabled: row.Enabled,
			Provider: row.ProviderSlug.String, Model: row.Model.String,
			RetainPrompts: row.RetainPrompts, RetainOutputs: row.RetainOutputs,
			RetentionDays: int(row.RetentionDays),
		})
	}

	limits, err := service.queries().ListAIUsageLimits(ctx)
	if err != nil {
		return nil, err
	}
	ceilings := make([]aigovernance.Limit, 0, len(limits))
	for _, row := range limits {
		ceilings = append(ceilings, aigovernance.Limit{
			Scope: row.Scope, Ref: row.ScopeRef.String, Feature: row.Feature.String,
			Window:       time.Duration(row.WindowSeconds) * time.Second,
			MaxRequests:  int64(row.MaxRequests.Int32),
			MaxTokens:    row.MaxTokens.Int64,
			MaxCostMinor: row.MaxCostMinor.Int64,
		})
	}
	return aigovernance.NewPolicy(configured, ceilings, service), nil
}

// Spent answers what a scope has used, satisfying aigovernance.UsageReader.
func (service *Service) Spent(
	ctx context.Context, scope, ref, feature string, window time.Duration,
) (aigovernance.Spend, error) {
	params := dbgen.AIUsageForWindowParams{WindowSeconds: window.Seconds()}
	switch scope {
	case aigovernance.ScopeOperator:
		params.OperatorID = optionalUUID(ref)
	case aigovernance.ScopeRole:
		params.OperatorRole = optionalText(ref)
	case aigovernance.ScopeFeature:
		params.Feature = optionalText(feature)
	}
	row, err := service.queries().AIUsageForWindow(ctx, params)
	if err != nil {
		return aigovernance.Spend{}, err
	}
	return aigovernance.Spend{
		Requests: row.Requests, Tokens: row.Tokens, CostMinor: row.CostMinor,
	}, nil
}

// AIUsageRecord is one model request's metadata. There is no prompt field and
// no output field, because there is no column to put them in.
type AIUsageRecord struct {
	Feature      string
	Task         string
	Provider     string
	Model        string
	OperatorID   string
	OperatorRole string

	InputTokens  int32
	OutputTokens int32
	LatencyMS    int32
	CostMinor    int64
	Currency     string

	Outcome   string
	ErrorCode string
	// Redaction is a count per category, so "what left?" is answerable without
	// keeping what left.
	Redaction map[string]int
}

// RecordAIUsage stores one request's metadata.
func (service *Service) RecordAIUsage(ctx context.Context, record AIUsageRecord) error {
	summary := []byte("{}")
	if len(record.Redaction) > 0 {
		encoded, err := json.Marshal(record.Redaction)
		if err != nil {
			return err
		}
		summary = encoded
	}
	return service.queries().RecordAIUsage(ctx, dbgen.RecordAIUsageParams{
		Feature: record.Feature, Task: record.Task,
		ProviderSlug: record.Provider, Model: record.Model,
		OperatorID:   optionalUUID(record.OperatorID),
		OperatorRole: optionalText(record.OperatorRole),
		InputTokens:  record.InputTokens, OutputTokens: record.OutputTokens,
		LatencyMs: record.LatencyMS, EstimatedCostMinor: record.CostMinor,
		Currency: optionalText(record.Currency), Outcome: record.Outcome,
		ErrorCode: optionalText(record.ErrorCode), RedactionSummary: summary,
	})
}

// AIUsageRow is one line of the cost and performance report.
type AIUsageRow struct {
	Feature       string `json:"feature"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Requests      int64  `json:"requests"`
	InputTokens   int64  `json:"inputTokens"`
	OutputTokens  int64  `json:"outputTokens"`
	CostMinor     int64  `json:"estimatedCostMinor"`
	MeanLatencyMS int64  `json:"meanLatencyMs"`
	P95LatencyMS  int64  `json:"p95LatencyMs"`
	Failures      int64  `json:"failures"`
}

// AIUsageReport reads token, request, latency, error, and estimated-cost
// figures. No prompt content appears in it because none is stored.
func (service *Service) AIUsageReport(
	ctx context.Context, since, until time.Time,
) ([]AIUsageRow, error) {
	rows, err := service.queries().AIUsageReport(ctx, dbgen.AIUsageReportParams{
		Since: timestamp(since), Until: timestamp(until),
	})
	if err != nil {
		return nil, err
	}
	report := make([]AIUsageRow, 0, len(rows))
	for _, row := range rows {
		report = append(report, AIUsageRow{
			Feature: row.Feature, Provider: row.ProviderSlug, Model: row.Model,
			Requests: row.Requests, InputTokens: row.InputTokens,
			OutputTokens: row.OutputTokens, CostMinor: row.CostMinor,
			MeanLatencyMS: row.MeanLatencyMs, P95LatencyMS: row.P95LatencyMs,
			Failures: row.Failures,
		})
	}
	return report, nil
}

// AIUsageLimit is one configured ceiling.
type AIUsageLimit struct {
	ID            string `json:"id,omitempty"`
	Scope         string `json:"scope"`
	Ref           string `json:"ref,omitempty"`
	Feature       string `json:"feature,omitempty"`
	WindowSeconds int32  `json:"windowSeconds"`
	MaxRequests   *int32 `json:"maxRequests,omitempty"`
	MaxTokens     *int64 `json:"maxTokens,omitempty"`
	MaxCostMinor  *int64 `json:"maxCostMinor,omitempty"`
}

// AIUsageLimits lists the ceilings an owner has set.
func (service *Service) AIUsageLimits(ctx context.Context) ([]AIUsageLimit, error) {
	rows, err := service.queries().ListAIUsageLimits(ctx)
	if err != nil {
		return nil, err
	}
	limits := make([]AIUsageLimit, 0, len(rows))
	for _, row := range rows {
		limits = append(limits, AIUsageLimit{
			ID: uuidString(row.ID), Scope: row.Scope, Ref: row.ScopeRef.String,
			Feature: row.Feature.String, WindowSeconds: row.WindowSeconds,
			MaxRequests:  int32Pointer(row.MaxRequests),
			MaxTokens:    int64Pointer(row.MaxTokens),
			MaxCostMinor: int64Pointer(row.MaxCostMinor),
		})
	}
	return limits, nil
}

// SaveAIUsageLimit stores one ceiling.
func (service *Service) SaveAIUsageLimit(
	ctx context.Context, limit AIUsageLimit, actor Actor,
) (AIUsageLimit, error) {
	if limit.WindowSeconds <= 0 {
		return AIUsageLimit{}, ErrValidaton
	}
	// A limit that bounds nothing is a row that does nothing and reads as
	// protection, which is worse than no row.
	if limit.MaxRequests == nil && limit.MaxTokens == nil && limit.MaxCostMinor == nil {
		return AIUsageLimit{}, ErrValidaton
	}

	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, err := queries.UpsertAIUsageLimit(ctx, dbgen.UpsertAIUsageLimitParams{
			Scope: limit.Scope, ScopeRef: optionalText(limit.Ref),
			Feature: optionalText(limit.Feature), WindowSeconds: limit.WindowSeconds,
			MaxRequests: optionalInt4(limit.MaxRequests), MaxTokens: optionalInt8(limit.MaxTokens),
			MaxCostMinor: optionalInt8(limit.MaxCostMinor),
			UpdatedBy:    optionalUUID(actor.AdminID),
		}); err != nil {
			return err
		}
		return appendAudit(ctx, queries, actor.audit(
			"ai.limit.saved", "settings", "ai_usage_limit", limit.Scope+"/"+limit.Ref,
			map[string]any{"feature": limit.Feature, "windowSeconds": limit.WindowSeconds},
		))
	})
	return limit, err
}

// DeleteAIUsageLimit removes one ceiling.
func (service *Service) DeleteAIUsageLimit(ctx context.Context, id string, actor Actor) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return ErrValidaton
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if err := queries.DeleteAIUsageLimit(ctx, parsed); err != nil {
			return err
		}
		return appendAudit(ctx, queries, actor.audit(
			"ai.limit.deleted", "settings", "ai_usage_limit", id, nil,
		))
	})
}

// AIDecision records that a person acted on something a model produced.
type AIDecision struct {
	SubjectType   string `json:"subjectType"`
	SubjectID     string `json:"subjectId"`
	Feature       string `json:"feature"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	PolicyVersion string `json:"policyVersion,omitempty"`
	OperatorID    string `json:"operatorId"`
	// Disposition separates accepted from edited, because an operator who
	// rewrote half of a draft made a different decision from one who pressed
	// send.
	Disposition   string    `json:"disposition"`
	Consequential bool      `json:"consequential"`
	Summary       string    `json:"summary,omitempty"`
	OccurredAt    time.Time `json:"occurredAt,omitzero"`
	OperatorEmail string    `json:"operatorEmail,omitempty"`
}

// RecordAIDecision stores one. A generated draft nobody sent is not a decision,
// so callers record this at the moment a person acts, not when a model answers.
func (service *Service) RecordAIDecision(ctx context.Context, decision AIDecision) error {
	operator, err := parseUUID(decision.OperatorID)
	if err != nil {
		return ErrValidaton
	}
	switch decision.Disposition {
	case "accepted", "edited", "rejected":
	default:
		return ErrValidaton
	}
	_, err = service.queries().RecordAIDecision(ctx, dbgen.RecordAIDecisionParams{
		SubjectType: decision.SubjectType, SubjectID: decision.SubjectID,
		Feature: decision.Feature, ProviderSlug: decision.Provider, Model: decision.Model,
		PolicyVersion: optionalText(decision.PolicyVersion), OperatorID: operator,
		Disposition: decision.Disposition, Consequential: decision.Consequential,
		Summary: optionalText(decision.Summary),
	})
	return err
}

// ExportAIDecisions answers "which of these were shaped by a model?".
func (service *Service) ExportAIDecisions(
	ctx context.Context, since, until time.Time, consequentialOnly bool, pageSizeRequested int32,
) ([]AIDecision, error) {
	rows, err := service.queries().ExportAIDecisions(ctx, dbgen.ExportAIDecisionsParams{
		Since: timestamp(since), Until: timestamp(until),
		ConsequentialOnly: consequentialOnly, PageSize: pageSize(pageSizeRequested),
	})
	if err != nil {
		return nil, err
	}
	decisions := make([]AIDecision, 0, len(rows))
	for _, row := range rows {
		decisions = append(decisions, AIDecision{
			SubjectType: row.SubjectType, SubjectID: row.SubjectID, Feature: row.Feature,
			Provider: row.ProviderSlug, Model: row.Model,
			PolicyVersion: row.PolicyVersion.String, OperatorID: uuidString(row.OperatorID),
			Disposition: row.Disposition, Consequential: row.Consequential,
			Summary: row.Summary.String, OccurredAt: row.OccurredAt.Time,
			OperatorEmail: row.OperatorEmail,
		})
	}
	return decisions, nil
}

// MCPServerConfig is one registered connection as the panel sees it.
type MCPServerConfig struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"displayName"`
	Endpoint    string `json:"endpoint"`
	Enabled     bool   `json:"enabled"`

	AllowedHosts        []string `json:"allowedHosts"`
	AllowPrivateNetwork bool     `json:"allowPrivateNetwork"`

	TimeoutMS          int32  `json:"timeoutMs"`
	MaxResponseBytes   int64  `json:"maxResponseBytes"`
	MaxCallsPerRequest int32  `json:"maxCallsPerRequest"`
	MaxDepth           int32  `json:"maxDepth"`
	CostLimitMinor     *int64 `json:"costLimitMinor,omitempty"`

	CredentialConfigured bool            `json:"credentialConfigured"`
	ProtocolVersion      string          `json:"protocolVersion,omitempty"`
	ServerName           string          `json:"serverName,omitempty"`
	ServerVersion        string          `json:"serverVersion,omitempty"`
	Capabilities         json.RawMessage `json:"capabilities,omitempty"`
	DiscoveredAt         time.Time       `json:"discoveredAt,omitzero"`

	LastCheckedAt       time.Time `json:"lastCheckedAt,omitzero"`
	LastCheckOK         bool      `json:"lastCheckOk"`
	LastCheckError      string    `json:"lastCheckError,omitempty"`
	ConsecutiveFailures int32     `json:"consecutiveFailures"`
}

// MCPServers lists the registry. The credential column is not selected, so
// listing cannot leak one.
func (service *Service) MCPServers(ctx context.Context) ([]MCPServerConfig, error) {
	rows, err := service.queries().ListMCPServers(ctx)
	if err != nil {
		return nil, err
	}
	servers := make([]MCPServerConfig, 0, len(rows))
	for _, row := range rows {
		servers = append(servers, MCPServerConfig{
			Slug: row.Slug, DisplayName: row.DisplayName, Endpoint: row.Endpoint,
			Enabled: row.Enabled, AllowedHosts: row.AllowedHosts,
			AllowPrivateNetwork: row.AllowPrivateNetwork,
			TimeoutMS:           row.TimeoutMs, MaxResponseBytes: row.MaxResponseBytes,
			MaxCallsPerRequest: row.MaxCallsPerRequest, MaxDepth: row.MaxDepth,
			CostLimitMinor:       int64Pointer(row.CostLimitMinor),
			CredentialConfigured: row.CredentialConfigured,
			ProtocolVersion:      row.ProtocolVersion.String,
			ServerName:           row.ServerName.String, ServerVersion: row.ServerVersion.String,
			Capabilities: row.Capabilities, DiscoveredAt: row.DiscoveredAt.Time,
			LastCheckedAt: row.LastCheckedAt.Time, LastCheckOK: row.LastCheckOk.Bool,
			LastCheckError:      row.LastCheckDetail.String,
			ConsecutiveFailures: row.ConsecutiveFailures,
		})
	}
	return servers, nil
}

// SaveMCPServer registers or updates a connection.
//
// The Go-side validation runs first so an operator gets a message naming the
// field, and the table's constraint stands behind it so a script cannot bypass
// the rule.
func (service *Service) SaveMCPServer(
	ctx context.Context, config MCPServerConfig, credential string, actor Actor,
) (MCPServerConfig, error) {
	candidate := mcp.Server{
		Slug: config.Slug, Name: config.DisplayName, Endpoint: config.Endpoint,
		Enabled: config.Enabled, AllowedHosts: config.AllowedHosts,
		AllowPrivateNetwork: config.AllowPrivateNetwork,
	}.Normalise()
	if err := candidate.Validate(); err != nil {
		return MCPServerConfig{}, fmt.Errorf("%w: %s", ErrValidaton, err.Error())
	}

	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, err := queries.UpsertMCPServer(ctx, dbgen.UpsertMCPServerParams{
			Slug: config.Slug, DisplayName: config.DisplayName, Endpoint: config.Endpoint,
			Enabled: config.Enabled, AllowedHosts: config.AllowedHosts,
			AllowPrivateNetwork: config.AllowPrivateNetwork,
			TimeoutMs:           orDefaultInt32(config.TimeoutMS, 20000),
			MaxResponseBytes:    orDefaultInt64(config.MaxResponseBytes, mcp.DefaultMaxResponseBytes),
			MaxCallsPerRequest:  orDefaultInt32(config.MaxCallsPerRequest, mcp.DefaultMaxCallsPerRequest),
			MaxDepth:            orDefaultInt32(config.MaxDepth, mcp.DefaultMaxDepth),
			CostLimitMinor:      optionalInt8(config.CostLimitMinor),
			UpdatedBy:           optionalUUID(actor.AdminID),
		}); err != nil {
			return err
		}
		if strings.TrimSpace(credential) != "" {
			sealed, err := service.sealSecret(credential, SecretMCPServer+"."+config.Slug)
			if err != nil {
				return err
			}
			if err := queries.SetMCPServerCredentials(ctx, dbgen.SetMCPServerCredentialsParams{
				CredentialsCiphertext: sealed, UpdatedBy: optionalUUID(actor.AdminID),
				Slug: config.Slug,
			}); err != nil {
				return err
			}
		}
		return appendAudit(ctx, queries, actor.audit(
			"mcp.server.saved", "settings", "mcp_server", config.Slug,
			map[string]any{
				"enabled": config.Enabled, "endpoint": config.Endpoint,
				"credentialRotated": strings.TrimSpace(credential) != "",
			},
		))
	})
	if err != nil {
		return MCPServerConfig{}, err
	}
	return service.mcpServer(ctx, config.Slug)
}

func (service *Service) mcpServer(ctx context.Context, slug string) (MCPServerConfig, error) {
	servers, err := service.MCPServers(ctx)
	if err != nil {
		return MCPServerConfig{}, err
	}
	for _, server := range servers {
		if server.Slug == slug {
			return server, nil
		}
	}
	return MCPServerConfig{}, ErrNotFound
}

// MCPServerCredential opens a connection's token for the process that calls it.
func (service *Service) MCPServerCredential(ctx context.Context, slug string) (string, error) {
	ciphertext, err := service.queries().GetMCPServerCredentials(ctx, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return service.OpenSecret(ciphertext, SecretMCPServer+"."+slug)
}

// DeleteMCPServer removes a connection and, by cascade, its tools.
func (service *Service) DeleteMCPServer(ctx context.Context, slug string, actor Actor) error {
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if err := queries.DeleteMCPServer(ctx, slug); err != nil {
			return err
		}
		return appendAudit(ctx, queries, actor.audit(
			"mcp.server.deleted", "settings", "mcp_server", slug, nil,
		))
	})
}

// MCPToolPolicy is one tool's discovered metadata and the owner's decisions
// about it.
type MCPToolPolicy struct {
	Server      string `json:"server"`
	Tool        string `json:"tool"`
	Enabled     bool   `json:"enabled"`
	Permission  string `json:"permission"`
	Writes      bool   `json:"writes"`
	Description string `json:"description,omitempty"`

	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	// SchemaUsable reports whether this build can enforce the declared schema. A
	// tool whose schema cannot be enforced is shown with the reason rather than
	// hidden, because "why is this not available?" is the owner's next question.
	SchemaUsable  bool   `json:"schemaUsable"`
	SchemaProblem string `json:"schemaProblem,omitempty"`
}

// MCPTools lists a server's tools.
func (service *Service) MCPTools(ctx context.Context, slug string) ([]MCPToolPolicy, error) {
	rows, err := service.queries().ListMCPTools(ctx, slug)
	if err != nil {
		return nil, err
	}
	return mcpToolsFrom(rows), nil
}

// EnabledMCPTools lists what the installation actually exposes.
func (service *Service) EnabledMCPTools(ctx context.Context) ([]MCPToolPolicy, error) {
	rows, err := service.queries().ListEnabledMCPTools(ctx)
	if err != nil {
		return nil, err
	}
	return mcpToolsFrom(rows), nil
}

func mcpToolsFrom(rows []dbgen.McpTool) []MCPToolPolicy {
	tools := make([]MCPToolPolicy, 0, len(rows))
	for _, row := range rows {
		tools = append(tools, MCPToolPolicy{
			Server: row.ServerSlug, Tool: row.ToolName, Enabled: row.Enabled,
			Permission: row.Permission, Writes: row.Writes,
			Description: row.Description.String,
			InputSchema: row.InputSchema, OutputSchema: row.OutputSchema,
			SchemaUsable: row.SchemaUsable, SchemaProblem: row.SchemaProblem.String,
		})
	}
	return tools
}

// RecordDiscoveredTool stores what a server advertised.
//
// It deliberately leaves enablement, permission, and the write flag alone: a
// rediscovery that re-enabled a tool an owner switched off would make discovery
// a privilege escalation.
func (service *Service) RecordDiscoveredTool(
	ctx context.Context, tool MCPToolPolicy,
) error {
	permission := tool.Permission
	if permission == "" {
		// A newly discovered tool needs some permission to satisfy the column,
		// and the safest placeholder is the narrowest one an installation has.
		// It is disabled anyway until an owner chooses.
		permission = "settings.write"
	}
	_, err := service.queries().RecordDiscoveredMCPTool(ctx, dbgen.RecordDiscoveredMCPToolParams{
		ServerSlug: tool.Server, ToolName: tool.Tool, Permission: permission,
		Writes: tool.Writes, InputSchema: tool.InputSchema, OutputSchema: tool.OutputSchema,
		Description: optionalText(tool.Description), SchemaUsable: tool.SchemaUsable,
		SchemaProblem: optionalText(tool.SchemaProblem),
	})
	return err
}

// SetMCPToolPolicy stores the owner's decisions about one tool.
func (service *Service) SetMCPToolPolicy(
	ctx context.Context, tool MCPToolPolicy, actor Actor,
) (MCPToolPolicy, error) {
	if strings.TrimSpace(tool.Permission) == "" {
		// A tool with no permission would be reachable by anyone who can reach
		// the assistant.
		return MCPToolPolicy{}, ErrValidaton
	}

	var saved MCPToolPolicy
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.SetMCPToolPolicy(ctx, dbgen.SetMCPToolPolicyParams{
			Enabled: tool.Enabled, Permission: tool.Permission, Writes: tool.Writes,
			ServerSlug: tool.Server, ToolName: tool.Tool,
		})
		if err != nil {
			return notFound(err)
		}
		saved = mcpToolsFrom([]dbgen.McpTool{row})[0]
		return appendAudit(ctx, queries, actor.audit(
			"mcp.tool.policy", "settings", "mcp_tool", tool.Server+"/"+tool.Tool,
			map[string]any{
				"enabled": tool.Enabled, "permission": tool.Permission, "writes": tool.Writes,
			},
		))
	})
	if err != nil {
		return MCPToolPolicy{}, err
	}
	return saved, nil
}

// RecordMCPDiscovery stores a successful capability refresh.
func (service *Service) RecordMCPDiscovery(
	ctx context.Context, slug string, info mcp.ServerInfo,
) error {
	capabilities := []byte("{}")
	if len(info.Capabilities) > 0 {
		encoded, err := json.Marshal(info.Capabilities)
		if err == nil {
			capabilities = encoded
		}
	}
	return service.queries().RecordMCPDiscovery(ctx, dbgen.RecordMCPDiscoveryParams{
		ProtocolVersion: optionalText(info.ProtocolVersion),
		ServerName:      optionalText(info.Name), ServerVersion: optionalText(info.Version),
		Capabilities: capabilities, Slug: slug,
	})
}

// RecordMCPHealth stores a reachability check.
func (service *Service) RecordMCPHealth(
	ctx context.Context, slug string, ok bool, detail string,
) error {
	return service.queries().RecordMCPHealth(ctx, dbgen.RecordMCPHealthParams{
		Ok: pgtype.Bool{Bool: ok, Valid: true}, Detail: optionalText(detail), Slug: slug,
	})
}

// Record stores one MCP event, satisfying mcp.AuditSink.
//
// Refusals arrive here alongside successes, because an audit trail that only
// records what happened cannot answer "did anyone try?".
func (service *Service) Record(ctx context.Context, event mcp.Event) error {
	arguments := []byte("{}")
	if len(event.Arguments) > 0 {
		encoded, err := json.Marshal(event.Arguments)
		if err == nil {
			arguments = encoded
		}
	}
	findings := event.Findings
	if findings == nil {
		findings = []string{}
	}
	return service.queries().RecordMCPEvent(ctx, dbgen.RecordMCPEventParams{
		Kind: event.Kind, ServerSlug: optionalText(event.Server),
		ToolName: optionalText(event.Tool), OperatorID: optionalUUID(event.Operator),
		Arguments: arguments, Confirmed: event.Confirmed,
		Reason: optionalText(event.Reason), Outcome: event.Outcome,
		Detail: optionalText(event.Detail), ResponseBytes: event.Bytes,
		DurationMs: int32(event.Duration.Milliseconds()), Findings: findings,
	})
}

// MCPEvent is one audited moment as the panel reads it.
type MCPEvent struct {
	ID         string          `json:"id"`
	OccurredAt time.Time       `json:"occurredAt"`
	Kind       string          `json:"kind"`
	Server     string          `json:"server,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Operator   string          `json:"operatorId,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Confirmed  bool            `json:"confirmed"`
	Reason     string          `json:"reason,omitempty"`
	Outcome    string          `json:"outcome"`
	Detail     string          `json:"detail,omitempty"`
	Bytes      int64           `json:"responseBytes"`
	DurationMS int32           `json:"durationMs"`
	Findings   []string        `json:"findings"`
}

// MCPEventPage is a keyset page of the audit trail.
type MCPEventPage struct {
	Items      []MCPEvent `json:"items"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

// MCPEvents reads the audit trail.
func (service *Service) MCPEvents(
	ctx context.Context, server, outcome, cursor string, size int32,
) (MCPEventPage, error) {
	position := DecodeCursor(cursor)
	limit := pageSize(size)
	rows, err := service.queries().ListMCPEvents(ctx, dbgen.ListMCPEventsParams{
		ServerSlug: optionalText(server), Outcome: optionalText(outcome),
		CursorAt: position.timestamp(), CursorID: position.uuid(),
		PageSize: limit + 1,
	})
	if err != nil {
		return MCPEventPage{}, err
	}

	page := MCPEventPage{Items: make([]MCPEvent, 0, len(rows))}
	for index, row := range rows {
		if int32(index) == limit {
			last := page.Items[len(page.Items)-1]
			page.NextCursor = EncodeCursor(last.OccurredAt, last.ID)
			break
		}
		page.Items = append(page.Items, MCPEvent{
			ID: uuidString(row.ID), OccurredAt: row.OccurredAt.Time, Kind: row.Kind,
			Server: row.ServerSlug.String, Tool: row.ToolName.String,
			Operator: uuidString(row.OperatorID), Arguments: row.Arguments,
			Confirmed: row.Confirmed, Reason: row.Reason.String, Outcome: row.Outcome,
			Detail: row.Detail.String, Bytes: row.ResponseBytes,
			DurationMS: row.DurationMs, Findings: row.Findings,
		})
	}
	return page, nil
}

// Helpers for the nullable columns this file reads and writes.

func int32Pointer(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	result := value.Int32
	return &result
}

func int64Pointer(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func numericPointer(value *float64) pgtype.Numeric {
	if value == nil {
		return pgtype.Numeric{}
	}
	var numeric pgtype.Numeric
	if err := numeric.Scan(fmt.Sprintf("%.2f", *value)); err != nil {
		return pgtype.Numeric{}
	}
	return numeric
}

func orDefaultInt32(value, fallback int32) int32 {
	if value <= 0 {
		return fallback
	}
	return value
}

func orDefaultInt64(value, fallback int64) int64 {
	if value <= 0 {
		return fallback
	}
	return value
}
