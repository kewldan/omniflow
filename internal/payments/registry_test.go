package payments

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The slug list lives in three places that cannot import each other: the
// builder registry, the database check constraints, and the OpenAPI enum.
// A provider present in one and missing in another fails at checkout or at
// the first INSERT, so this pins them together.
func TestProviderSlugsAgreeAcrossRegistrySchemaAndContract(t *testing.T) {
	t.Parallel()
	want := slices.Clone(Slugs())
	sort.Strings(want)
	if len(slices.Compact(slices.Clone(want))) != len(want) {
		t.Fatalf("duplicate slug in %v", want)
	}

	contract, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`PaymentProvider: \{type: string, enum: \[([^\]]*)\]\}`).FindSubmatch(contract)
	if match == nil {
		t.Fatal("PaymentProvider enum not found in api/openapi.yaml")
	}
	assertSameSlugs(t, "OpenAPI enum", splitList(string(match[1]), ""), want)

	schema, err := os.ReadFile(filepath.Join("..", "..", "database", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	constraint := regexp.MustCompile(`payment_intents_provider_check CHECK \(\(provider = ANY \(ARRAY\[([^\]]*)\]`).FindSubmatch(schema)
	if constraint == nil {
		t.Fatal("payment_intents_provider_check not found in database/schema.sql")
	}
	assertSameSlugs(t, "payment_intents check", splitList(string(constraint[1]), "::text"), want)
}

func TestEveryBuilderIsUnconfiguredWithAnEmptyEnvironment(t *testing.T) {
	t.Parallel()
	settings := Settings{Env: func(string) string { return "" }, PublicURL: "https://example.com"}
	for _, builder := range Builders {
		provider, err := builder.Build(settings)
		if err != ErrNotConfigured || provider != nil {
			t.Errorf("%s: got provider=%v err=%v, want ErrNotConfigured", builder.Slug, provider, err)
		}
	}
}

func splitList(raw, suffix string) []string {
	var slugs []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), suffix))
		part = strings.Trim(part, "'")
		if part != "" {
			slugs = append(slugs, part)
		}
	}
	sort.Strings(slugs)
	return slugs
}

func assertSameSlugs(t *testing.T, where string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s slugs = %v, want %v", where, got, want)
	}
}
