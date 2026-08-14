//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/connectpg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// The connection catalogue against a real database.
//
// The property this whole change exists to preserve is that the Telegram bot
// and the customer web panel cannot recommend different applications. That used
// to be guaranteed by the table being a compiled constant. It is now guaranteed
// by both surfaces reading one query, and that is only a guarantee if something
// asserts it — which is what the seed and enablement tests below do.

func TestTheSeededCatalogueIsWhatTheBinaryUsedToCarry(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	catalogue := connectpg.New(harness.pool)

	platforms, err := catalogue.Platforms(ctx, "en")
	if err != nil {
		t.Fatalf("platforms: %v", err)
	}
	// The five the compiled table documented, in the order it recommended them.
	want := []string{"ios", "android", "windows", "macos", "linux"}
	if len(platforms) != len(want) {
		t.Fatalf("%d platforms seeded, want %d", len(platforms), len(want))
	}
	for index, slug := range want {
		if platforms[index].Slug != slug {
			t.Errorf("platform %d is %q, want %q — the recommendation order moved",
				index, platforms[index].Slug, slug)
		}
		if strings.TrimSpace(platforms[index].Label) == "" {
			t.Errorf("platform %q has no label, so its button would be blank", slug)
		}
	}

	// iOS documented three clients, Happ first, and the first one is what the
	// setup steps name.
	clients, err := catalogue.Clients(ctx, "ios", "en")
	if err != nil {
		t.Fatalf("clients: %v", err)
	}
	if len(clients) != 3 || clients[0].Name != "Happ" {
		t.Fatalf("the iOS seed changed: %+v", clients)
	}
	link := clients[0].DeepLink("https://sub.example.test/abc")
	if link != "happ://add/https://sub.example.test/abc" {
		t.Fatalf("deep link is %q", link)
	}
}

// Disabling a platform has to remove it from both surfaces at once, which is
// the same statement as "the enabled predicate is in the query rather than in
// one caller".
func TestDisablingHidesFromEveryCustomerSurface(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	catalogue := connectpg.New(harness.pool)
	actor := harness.operator(ctx, t, "connect@example.test")

	if _, err := service.SaveConnectPlatform(ctx, panelpg.ConnectPlatform{
		Slug: "linux", LabelEN: "Linux", LabelRU: "Linux", Enabled: false, SortOrder: 50,
	}, actor); err != nil {
		t.Fatalf("disable: %v", err)
	}

	platforms, err := catalogue.Platforms(ctx, "en")
	if err != nil {
		t.Fatalf("platforms: %v", err)
	}
	for _, platform := range platforms {
		if platform.Slug == "linux" {
			t.Fatal("a disabled platform is still offered to customers")
		}
	}
	// Naming it directly must not get round the switch either, or a stale
	// bookmark would reach guidance the operator withdrew.
	clients, err := catalogue.Clients(ctx, "linux", "en")
	if err != nil {
		t.Fatalf("clients: %v", err)
	}
	if len(clients) != 0 {
		t.Fatal("a disabled platform's clients are reachable by naming the platform")
	}

	// The operator's own view still shows it, because turning it back on is
	// what somebody opens this screen to do.
	stored, err := service.ConnectCatalogue(ctx)
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	found := false
	for _, platform := range stored.Platforms {
		if platform.Slug == "linux" {
			found, _ = true, platform
			if platform.Enabled {
				t.Fatal("the operator view reports the platform as enabled")
			}
		}
	}
	if !found {
		t.Fatal("a disabled platform vanished from the operator's own screen")
	}
}

// The scheme ends up in the href of an anchor on a page that holds a session
// cookie. This is the test that stands between an operator and stored
// cross-site scripting, and it asserts the refusal survives the round trip to
// the database rather than only the Go check.
func TestAnUnsafeSchemeIsRefused(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "scheme@example.test")

	for _, scheme := range []string{
		"javascript://alert(1)",
		"data://text/html,<script>alert(1)</script>",
		`happ://add/" onmouseover="alert(1)`,
		"not-a-scheme",
	} {
		if _, err := service.SaveConnectClient(ctx, panelpg.ConnectClient{
			Platform: "ios", Name: "Malicious", Scheme: scheme, Enabled: true,
		}, actor); !errors.Is(err, panelpg.ErrValidaton) {
			t.Errorf("%q was accepted as an import scheme: %v", scheme, err)
		}
	}

	// A download address is a link handed to somebody about to install software
	// on their own device, so plain HTTP is refused too.
	if _, err := service.SaveConnectClient(ctx, panelpg.ConnectClient{
		Platform: "ios", Name: "Plain", Scheme: "happ://add/",
		DownloadURL: "http://apps.example.test/happ", Enabled: true,
	}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Errorf("an http download address was accepted: %v", err)
	}
}

// An operator adding a client and writing instructions for it is the whole
// point of the change, so the round trip is asserted end to end.
func TestAnOperatorCanAddAClientWithoutARelease(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	catalogue := connectpg.New(harness.pool)
	actor := harness.operator(ctx, t, "add-client@example.test")

	saved, err := service.SaveConnectClient(ctx, panelpg.ConnectClient{
		Platform: "ios", Name: "Shadowrocket", Scheme: "sub://",
		DownloadURL:    "https://apps.example.test/shadowrocket",
		InstructionsEN: "Open the app, tap +, choose Subscribe.",
		InstructionsRU: "Откройте приложение, нажмите +, выберите «Подписка».",
		Enabled:        true, SortOrder: 5,
	}, actor)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("the saved client has no identifier, so it can never be edited")
	}

	// Sort order 5 puts it before Happ at 10, and the first client is the one
	// the setup steps name on both surfaces.
	clients, err := catalogue.Clients(ctx, "ios", "ru")
	if err != nil {
		t.Fatalf("clients: %v", err)
	}
	if clients[0].Name != "Shadowrocket" {
		t.Fatalf("the ordering was not honoured: %+v", clients)
	}
	if !strings.Contains(clients[0].Instructions, "Подписка") {
		t.Fatalf("a Russian reader got %q", clients[0].Instructions)
	}
	english, err := catalogue.Clients(ctx, "ios", "en")
	if err != nil {
		t.Fatalf("clients: %v", err)
	}
	if !strings.Contains(english[0].Instructions, "Subscribe") {
		t.Fatalf("an English reader got %q", english[0].Instructions)
	}

	// Editing carries the identifier, so a rename is an update rather than a
	// second row the screen would show twice.
	saved.Name = "Shadowrocket 2"
	if _, err := service.SaveConnectClient(ctx, saved, actor); err != nil {
		t.Fatalf("rename: %v", err)
	}
	renamed, err := catalogue.Clients(ctx, "ios", "en")
	if err != nil {
		t.Fatalf("clients: %v", err)
	}
	if len(renamed) != 4 {
		t.Fatalf("%d clients after a rename, want 4", len(renamed))
	}

	if err := service.DeleteConnectClient(ctx, saved.ID, actor); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
