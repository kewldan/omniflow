//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/customerauthpg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// A write-only secret must never follow its row to a new destination. If it
// did, anyone allowed to edit the address could point it at a server they run
// and collect the key on the next request, without ever being able to read it.
func TestAIProviderKeyDoesNotFollowANewAddress(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "ai-reentry@example.test")

	provider := panelpg.AIProvider{
		Slug: "acme", Kind: "openai_compatible", DisplayName: "Acme",
		BaseURL: "https://models.example.test/v1", Enabled: true,
	}
	if _, err := service.SaveAIProvider(ctx, provider, "original-key", actor); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Renaming keeps the key; that is what makes the form re-saveable.
	renamed := provider
	renamed.DisplayName = "Acme models"
	if _, err := service.SaveAIProvider(ctx, renamed, "", actor); err != nil {
		t.Fatalf("rename: %v", err)
	}

	moved := renamed
	moved.BaseURL = "https://collector.attacker.test/v1"
	if _, err := service.SaveAIProvider(ctx, moved, "", actor); !errors.Is(err, panelpg.ErrSecretRequired) {
		t.Fatalf("moving the address without a key: err = %v, want ErrSecretRequired", err)
	}
	otherKind := renamed
	otherKind.Kind, otherKind.BaseURL = "anthropic", ""
	if _, err := service.SaveAIProvider(ctx, otherKind, "", actor); !errors.Is(err, panelpg.ErrSecretRequired) {
		t.Fatalf("changing the kind without a key: err = %v, want ErrSecretRequired", err)
	}
	_, baseURL, key, err := service.AIProviderCredential(ctx, "acme")
	if err != nil || baseURL != provider.BaseURL || key != "original-key" {
		t.Fatalf("a refused save changed the provider: %q %q %v", baseURL, key, err)
	}

	if _, err = service.SaveAIProvider(ctx, moved, "replacement-key", actor); err != nil {
		t.Fatalf("moving the address with a new key: %v", err)
	}
	if _, _, key, err = service.AIProviderCredential(ctx, "acme"); err != nil || key != "replacement-key" {
		t.Fatalf("key after move = %q, %v", key, err)
	}

	// The storage layer holds the rule too: an upsert that changes the address
	// drops the stored key even for a caller that skipped the service check.
	if _, err = dbgen.New(harness.pool).UpsertAIProvider(ctx, dbgen.UpsertAIProviderParams{
		Slug: "acme", Kind: "openai_compatible", DisplayName: "Acme",
		BaseUrl: pgtype.Text{String: "https://elsewhere.attacker.test/v1", Valid: true}, Enabled: true,
	}); err != nil {
		t.Fatalf("raw upsert: %v", err)
	}
	var keyKept bool
	if err = harness.pool.QueryRow(ctx,
		`SELECT credentials_ciphertext IS NOT NULL FROM ai_providers WHERE slug = 'acme'`,
	).Scan(&keyKept); err != nil || keyKept {
		t.Fatalf("a raw upsert to a new address kept the key: kept=%v err=%v", keyKept, err)
	}

	// Plain HTTP and internal addresses need the process-level opt-in.
	for _, base := range []string{"http://models.example.test/v1", "https://10.0.0.5/v1"} {
		insecure := provider
		insecure.Slug, insecure.BaseURL = "local", base
		if _, err = service.SaveAIProvider(ctx, insecure, "k", actor); !errors.Is(err, panelpg.ErrValidaton) {
			t.Fatalf("base URL %s: err = %v, want ErrValidaton", base, err)
		}
	}
}

func TestBlocklistCredentialDoesNotFollowANewURL(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "blocklist-reentry@example.test")

	header := "Bearer publisher-token"
	input := panelpg.BlocklistSourceInput{
		Slug: "feed", DisplayName: "Feed", SubjectKind: "telegram_id",
		URL: "https://lists.example.test/feed.txt", AuthHeader: &header,
		Enabled: true, RefreshInterval: 3600,
	}
	saved, err := service.SaveBlocklistSource(ctx, input, actor)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	moved := input
	moved.AuthHeader = nil
	moved.URL = "https://collector.attacker.test/feed.txt"
	if _, err = service.SaveBlocklistSource(ctx, moved, actor); !errors.Is(err, panelpg.ErrSecretRequired) {
		t.Fatalf("moving the URL without a header: err = %v, want ErrSecretRequired", err)
	}
	stored, err := service.BlocklistSourceCredential(ctx, saved.ID)
	if err != nil || stored != header {
		t.Fatalf("a refused save changed the credential: %q %v", stored, err)
	}

	// Re-saving at the same URL with no header keeps it.
	same := input
	same.AuthHeader = nil
	same.DisplayName = "Renamed"
	if _, err = service.SaveBlocklistSource(ctx, same, actor); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if stored, err = service.BlocklistSourceCredential(ctx, saved.ID); err != nil || stored != header {
		t.Fatalf("a re-save at the same URL lost the credential: %q %v", stored, err)
	}
}

func TestOIDCClientSecretDoesNotFollowANewIssuer(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)

	admin := newAdminService(t, harness, time.Now)
	owner := bootstrapOwner(t, ctx, admin)
	request := adminauthpg.RequestContext{RequestID: "secret-reentry"}
	adminInput := adminauthpg.SaveOIDCProviderInput{
		Slug: "corp", DisplayName: "Corp", Issuer: "https://sso.example.test",
		DiscoveryURL: "https://sso.example.test", ClientID: "panel", ClientSecret: "s3cret",
	}
	if _, err := admin.SaveOIDCProvider(ctx, adminInput, owner.ID, request); err != nil {
		t.Fatalf("save operator provider: %v", err)
	}
	for _, change := range []func(*adminauthpg.SaveOIDCProviderInput){
		func(in *adminauthpg.SaveOIDCProviderInput) { in.Issuer = "https://attacker.test" },
		func(in *adminauthpg.SaveOIDCProviderInput) { in.DiscoveryURL = "https://attacker.test" },
		func(in *adminauthpg.SaveOIDCProviderInput) { in.ClientID = "attacker-client" },
	} {
		moved := adminInput
		moved.ClientSecret = ""
		change(&moved)
		if _, err := admin.SaveOIDCProvider(ctx, moved, owner.ID, request); !errors.Is(err, adminauthpg.ErrOIDCSecretRequired) {
			t.Fatalf("operator provider moved without a secret: err = %v, want ErrOIDCSecretRequired", err)
		}
	}
	renamed := adminInput
	renamed.ClientSecret, renamed.DisplayName = "", "Corporate"
	if saved, err := admin.SaveOIDCProvider(ctx, renamed, owner.ID, request); err != nil || !saved.HasClientSecret {
		t.Fatalf("renaming lost the secret: %+v %v", saved, err)
	}

	customers, err := customerauthpg.New(harness.pool, adminTestKey, customerauthpg.Options{})
	if err != nil {
		t.Fatalf("build customer service: %v", err)
	}
	customerInput := customerauthpg.SaveOIDCProviderInput{
		Slug: "corp", DisplayName: "Corp", Issuer: "https://sso.example.test",
		DiscoveryURL: "https://sso.example.test", ClientID: "account", ClientSecret: "s3cret",
	}
	if _, err = customers.SaveOIDCProvider(ctx, customerInput); err != nil {
		t.Fatalf("save customer provider: %v", err)
	}
	movedCustomer := customerInput
	movedCustomer.ClientSecret, movedCustomer.Issuer = "", "https://attacker.test"
	if _, err = customers.SaveOIDCProvider(ctx, movedCustomer); !errors.Is(err, customerauthpg.ErrOIDCSecretRequired) {
		t.Fatalf("customer provider moved without a secret: err = %v, want ErrOIDCSecretRequired", err)
	}
	movedCustomer.ClientSecret = "fresh"
	if saved, saveErr := customers.SaveOIDCProvider(ctx, movedCustomer); saveErr != nil || !saved.HasClientSecret {
		t.Fatalf("moving with a new secret: %+v %v", saved, saveErr)
	}
}
