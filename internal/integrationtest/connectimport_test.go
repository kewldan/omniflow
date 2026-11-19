//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// Importing Remnawave's subscription page twice updates what the first import
// created rather than adding copies, and an application whose scheme the
// catalogue's own rule refuses is reported instead of stored.
func TestConnectImportIsIdempotentAndRefusesUnsafeSchemes(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	actor := panelpg.Actor{Type: "admin"}
	imported := []panelpg.ImportedPlatform{{
		Slug: "android-tv", LabelEN: "Android TV", LabelRU: "Android TV",
		Clients: []panelpg.ImportedClient{
			{Name: "Happ", Scheme: "happ://add/", DownloadURL: "https://play.example/happ", InstructionsEN: "Install", InstructionsRU: "Установите"},
			{Name: "Template", Scheme: "app://{{HAPP_CRYPT4_LINK}}"},
			{Name: "Script", Scheme: "javascript://alert/"},
		},
	}}
	first, err := panel.ImportConnectCatalogue(ctx, imported, actor)
	if err != nil || first.Platforms != 1 || first.Clients != 1 || len(first.Skipped) != 2 {
		t.Fatalf("first import = %+v (%v)", first, err)
	}
	imported[0].Clients[0].InstructionsEN = "Install it"
	if _, err = panel.ImportConnectCatalogue(ctx, imported, actor); err != nil {
		t.Fatal(err)
	}
	var count int
	var instructions string
	if err := harness.pool.QueryRow(ctx, `SELECT count(*), max(instructions_en) FROM connect_clients
		WHERE platform_slug = 'android-tv' AND name = 'Happ'`).Scan(&count, &instructions); err != nil {
		t.Fatal(err)
	}
	if count != 1 || instructions != "Install it" {
		t.Fatalf("after two imports: %d rows, instructions %q", count, instructions)
	}
}
