//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/mcp"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// time24h is the window the per-scope spend check uses in these tests.
const time24h = 24 * time.Hour

// nowMinus and nowPlus bracket a report window around the moment of the test,
// so a run near midnight does not land the rows outside the range.
func nowMinus(t *testing.T, hours int) time.Time {
	t.Helper()
	return time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
}

func nowPlus(t *testing.T, hours int) time.Time {
	t.Helper()
	return time.Now().UTC().Add(time.Duration(hours) * time.Hour)
}

// Settings, AI governance, and the MCP registry against a real database.
//
// These are the properties that only a real PostgreSQL can prove: the version
// guard is an UPDATE predicate, the secret column is excluded by the query
// rather than by a Go filter, and the constraint that refuses a plaintext MCP
// endpoint lives in the table. A mock would agree with whatever the Go code
// believed.

// A save that does not match the version an operator was shown is a conflict,
// not an overwrite. This is the property that keeps two panel tabs from
// silently discarding each other's work.
func TestSettingSectionSaveIsGuardedByItsVersion(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "settings-version@example.test")

	first, err := service.SaveSettingSection(ctx, "branding",
		json.RawMessage(`{"serviceName":"Omniflow"}`), 1, nil, actor)
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	if first.Version != 2 {
		t.Fatalf("the version did not advance: %d", first.Version)
	}

	// A second operator still holding version 1 is refused rather than winning.
	if _, err := service.SaveSettingSection(ctx, "branding",
		json.RawMessage(`{"serviceName":"Something else"}`), 1, nil, actor,
	); !errors.Is(err, panelpg.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	current, err := service.SettingSection(ctx, "branding")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(current.Document), "Omniflow") {
		t.Fatalf("the losing save overwrote the winning one: %s", current.Document)
	}
}

// A secret is never in anything a request handler returns, and that is enforced
// by the query not selecting the column rather than by a filter somebody has to
// remember.
func TestSettingSecretsAreWriteOnly(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "settings-secret@example.test")

	token := strings.Join([]string{"rw", "integration", "value"}, "-")
	saved, err := service.SaveSettingSection(ctx, "remnawave",
		json.RawMessage(`{"baseUrl":"https://panel.example.test"}`), 1,
		map[string]string{"token": token}, actor)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !saved.SecretConfigured {
		t.Fatal("a stored secret was not reported as configured")
	}

	sections, err := service.SettingSections(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, section := range sections {
		if strings.Contains(string(section.Document), token) {
			t.Fatalf("a secret appeared in a listed document: %s", section.Section)
		}
	}
	one, err := service.SettingSection(ctx, "remnawave")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(one.Document), token) {
		t.Fatal("a secret appeared in the section document")
	}

	// The process that needs the credential can still open it.
	secrets, err := service.SettingSecrets(ctx, "remnawave")
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}
	if secrets["token"] != token {
		t.Fatalf("the sealed value did not round-trip: %q", secrets["token"])
	}

	// A later save that sends no secrets leaves the stored one alone, which is
	// what makes rotation deliberate rather than a side effect of an edit.
	if _, err := service.SaveSettingSection(ctx, "remnawave",
		json.RawMessage(`{"baseUrl":"https://moved.example.test"}`), 2, nil, actor,
	); err != nil {
		t.Fatalf("second save: %v", err)
	}
	after, err := service.SettingSecrets(ctx, "remnawave")
	if err != nil {
		t.Fatalf("open secrets after edit: %v", err)
	}
	if after["token"] != token {
		t.Fatal("editing the document cleared the stored credential")
	}
}

// An enabled feature with no provider cannot exist: the table refuses it, and
// so does the service, so an operator gets a message rather than a constraint
// violation.
func TestAnAIFeatureCannotBeEnabledWithoutAProvider(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "ai-feature@example.test")

	if _, err := service.ConfigureAIFeature(ctx, panelpg.AIFeature{
		Feature: "support_reply", Enabled: true, RetentionDays: 30,
	}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("expected ErrValidaton, got %v", err)
	}

	if _, err := service.SaveAIProvider(ctx, panelpg.AIProvider{
		Slug: "acme", Kind: "anthropic", DisplayName: "Acme", Enabled: true,
	}, "an-api-key", actor); err != nil {
		t.Fatalf("save provider: %v", err)
	}
	saved, err := service.ConfigureAIFeature(ctx, panelpg.AIFeature{
		Feature: "support_reply", Enabled: true, Provider: "acme", Model: "acme-1",
		RetainOutputs: true, RetentionDays: 30,
	}, actor)
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !saved.Enabled || saved.Provider != "acme" {
		t.Fatalf("the feature was not configured: %+v", saved)
	}
}

// A provider key is never returned, and the panel listing says only that one
// exists.
func TestAIProviderKeysAreWriteOnly(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "ai-provider@example.test")

	key := strings.Join([]string{"ak", "integration", "value"}, "-")
	if _, err := service.SaveAIProvider(ctx, panelpg.AIProvider{
		Slug: "acme", Kind: "openai_compatible", DisplayName: "Acme",
		BaseURL: "https://models.example.test/v1", Enabled: true,
	}, key, actor); err != nil {
		t.Fatalf("save: %v", err)
	}

	providers, err := service.AIProviders(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(providers) != 1 || !providers[0].KeyConfigured {
		t.Fatalf("the key was not reported as configured: %+v", providers)
	}

	_, _, opened, err := service.AIProviderCredential(ctx, "acme")
	if err != nil {
		t.Fatalf("open credential: %v", err)
	}
	if opened != key {
		t.Fatalf("the sealed key did not round-trip: %q", opened)
	}
}

// Usage is recorded without content, and the report reads back what was spent.
func TestAIUsageIsRecordedAndReportedWithoutContent(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)

	for range 3 {
		if err := service.RecordAIUsage(ctx, panelpg.AIUsageRecord{
			Feature: "support_reply", Task: "reply_suggest", Provider: "acme",
			Model: "acme-1", InputTokens: 100, OutputTokens: 50, LatencyMS: 250,
			CostMinor: 12, Currency: "USD", Outcome: "succeeded",
			Redaction: map[string]int{"email": 1},
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	spend, err := service.Spent(ctx, "feature", "", "support_reply", time24h)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if spend.Requests != 3 || spend.Tokens != 450 || spend.CostMinor != 36 {
		t.Fatalf("the spend does not add up: %+v", spend)
	}

	report, err := service.AIUsageReport(ctx, nowMinus(t, 1), nowPlus(t, 1))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(report) != 1 || report[0].Requests != 3 {
		t.Fatalf("the report is wrong: %+v", report)
	}
	if report[0].MeanLatencyMS != 250 {
		t.Fatalf("latency was not aggregated: %+v", report[0])
	}
}

// The table refuses a plaintext endpoint as well as the Go validation, so a
// migration script cannot register one either.
func TestMCPRegistrationRefusesPlaintextAndUnmappedTools(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "mcp@example.test")

	if _, err := service.SaveMCPServer(ctx, panelpg.MCPServerConfig{
		Slug: "acme", DisplayName: "Acme", Endpoint: "http://mcp.example.test/rpc",
	}, "", actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a plaintext endpoint was accepted: %v", err)
	}

	saved, err := service.SaveMCPServer(ctx, panelpg.MCPServerConfig{
		Slug: "acme", DisplayName: "Acme", Endpoint: "https://mcp.example.test/rpc",
		Enabled: true,
	}, "a-bearer-token", actor)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !saved.CredentialConfigured {
		t.Fatal("the credential was not reported as configured")
	}
	// The limits come back normalised, so the panel and the client agree about
	// what this connection is bounded by.
	if saved.MaxCallsPerRequest != mcp.DefaultMaxCallsPerRequest ||
		saved.MaxDepth != mcp.DefaultMaxDepth {
		t.Fatalf("the limits were not normalised: %+v", saved)
	}

	// A discovered tool arrives disabled. Discovery is not authorisation.
	if err := service.RecordDiscoveredTool(ctx, panelpg.MCPToolPolicy{
		Server: "acme", Tool: "search-orders", Writes: false,
		Description: "searches orders", SchemaUsable: true,
	}); err != nil {
		t.Fatalf("record tool: %v", err)
	}
	tools, err := service.MCPTools(ctx, "acme")
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Enabled {
		t.Fatalf("a discovered tool was enabled: %+v", tools)
	}
	if enabled, err := service.EnabledMCPTools(ctx); err != nil || len(enabled) != 0 {
		t.Fatalf("a disabled tool was exposed: %+v %v", enabled, err)
	}

	// An owner enables it explicitly, with a permission.
	if _, err := service.SetMCPToolPolicy(ctx, panelpg.MCPToolPolicy{
		Server: "acme", Tool: "search-orders", Enabled: true,
		Permission: "finance.read", Writes: false,
	}, actor); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	enabled, err := service.EnabledMCPTools(ctx)
	if err != nil || len(enabled) != 1 {
		t.Fatalf("the enabled tool was not exposed: %+v %v", enabled, err)
	}

	// A rediscovery refreshes the description and leaves the owner's decision
	// alone: otherwise discovery would be a way to re-enable a tool somebody
	// switched off.
	if err := service.RecordDiscoveredTool(ctx, panelpg.MCPToolPolicy{
		Server: "acme", Tool: "search-orders", Writes: true,
		Description: "searches orders, now differently", SchemaUsable: true,
	}); err != nil {
		t.Fatalf("rediscover: %v", err)
	}
	after, err := service.MCPTools(ctx, "acme")
	if err != nil {
		t.Fatalf("list after rediscovery: %v", err)
	}
	if !after[0].Enabled || after[0].Permission != "finance.read" || after[0].Writes {
		t.Fatalf("rediscovery changed the owner's decision: %+v", after[0])
	}
	if !strings.Contains(after[0].Description, "now differently") {
		t.Fatalf("rediscovery did not refresh the description: %+v", after[0])
	}
}

// Refusals are recorded alongside successes, because a trail that only records
// what happened cannot answer whether anyone tried.
func TestMCPEventsRecordRefusalsToo(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)

	for _, event := range []mcp.Event{
		{Kind: mcp.EventToolCall, Server: "acme", Tool: "refund", Outcome: "refused",
			Detail: "missing finance.write"},
		{Kind: mcp.EventToolCall, Server: "acme", Tool: "search", Outcome: "allowed",
			Findings: []string{mcp.FindingInstructionOverride}},
	} {
		if err := service.Record(ctx, event); err != nil {
			t.Fatalf("record event: %v", err)
		}
	}

	page, err := service.MCPEvents(ctx, "acme", "", "", 25)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("expected both events, got %d", len(page.Items))
	}

	refused, err := service.MCPEvents(ctx, "acme", "refused", "", 25)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(refused.Items) != 1 || refused.Items[0].Tool != "refund" {
		t.Fatalf("the refusal was not filterable: %+v", refused.Items)
	}
	// The injection findings survive, so a strange answer later has a record of
	// the material that produced it.
	for _, item := range page.Items {
		if item.Tool == "search" && len(item.Findings) != 1 {
			t.Fatalf("the findings were lost: %+v", item)
		}
	}
}

// The referral programme refuses to be enabled while paying nothing: the
// invites still go out, so it is worse than having no scheme.
func TestAReferralProgrammeMustPaySomebodyToBeEnabled(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "referral@example.test")

	if _, err := service.SaveReferralProgram(ctx, panelpg.ReferralProgram{
		Enabled: true, Currency: "USD", Qualification: "first_paid_order",
		AttributionValidityDays: 90,
	}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a programme that pays nothing was enabled: %v", err)
	}

	saved, err := service.SaveReferralProgram(ctx, panelpg.ReferralProgram{
		Enabled: true, Currency: "USD", Qualification: "first_paid_order",
		InviterRewardMinor: 500, AttributionValidityDays: 90,
	}, actor)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !saved.Enabled || saved.InviterRewardMinor != 500 {
		t.Fatalf("the programme was not stored: %+v", saved)
	}

	// An installation that has never configured one reads as disabled rather
	// than as a missing row, so the settings screen has something to render.
	fresh := newHarness(t)
	empty, err := newOperations(t, fresh).ReferralProgram(ctx)
	if err != nil {
		t.Fatalf("read unconfigured: %v", err)
	}
	if empty.Enabled {
		t.Fatal("an unconfigured programme reported as enabled")
	}
}

// The diagnostics bundle is assembled from an allowlist, so it carries no
// document contents and no credential.
func TestDiagnosticsCarryNoSecretsOrDocuments(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "diagnostics@example.test")

	token := strings.Join([]string{"diag", "secret", "value"}, "-")
	if _, err := service.SaveSettingSection(ctx, "telegram",
		json.RawMessage(`{"botUsername":"omniflow_bot"}`), 1,
		map[string]string{"botToken": token}, actor); err != nil {
		t.Fatalf("save: %v", err)
	}

	bundle, err := service.Diagnostics(ctx, "test")
	if err != nil {
		t.Fatalf("diagnostics: %v", err)
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(encoded), token) {
		t.Fatal("a credential reached the diagnostics bundle")
	}
	if strings.Contains(string(encoded), "omniflow_bot") {
		t.Fatal("a section document reached the diagnostics bundle")
	}

	found := false
	for _, section := range bundle.Settings {
		if section.Section == "telegram" {
			found = true
			if !section.HasSecret || !section.Configured {
				t.Fatalf("the section status is wrong: %+v", section)
			}
		}
	}
	if !found {
		t.Fatal("the configured section is missing from the bundle")
	}
	if bundle.Counts["operators"] < 1 {
		t.Fatalf("the counts were not gathered: %+v", bundle.Counts)
	}
}
