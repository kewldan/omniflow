//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/branding"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// White-label theming against a real database.
//
// The properties worth proving here are the ones a mock would agree with
// whatever the Go code believed: the palette rides the same version guard every
// other settings section does, an unreadable palette is refused before it is
// stored rather than after, the image table's own constraints hold, and the
// checksum a public route publishes as an ETag is computed from the bytes that
// were actually saved.

// A minimal PNG. It is a real one — the eight-byte signature and an IHDR — so
// the content sniffing in SaveBrandingAsset accepts it for the same reason a
// browser would.
func onePixelPNG() []byte {
	return append(
		[]byte("\x89PNG\r\n\x1a\n"),
		[]byte("\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")...,
	)
}

func TestThemeRoundTripsAndKeepsItsVersionGuard(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "theme@example.test")

	// A fresh installation reads as the shipped design rather than as an error
	// or an empty document the screen would have to interpret.
	initial, err := service.Theme(ctx)
	if err != nil {
		t.Fatalf("reading an unconfigured theme: %v", err)
	}
	if initial.CSS != "" {
		t.Fatalf("an unconfigured installation published %q", initial.CSS)
	}
	if initial.Theme.Radius != "default" || initial.Theme.DefaultTheme != "system" {
		t.Fatalf("the shipped defaults were not applied: %+v", initial.Theme)
	}
	if len(initial.Themable) == 0 {
		t.Fatal("no themable tokens were published, so the screen has nothing to offer")
	}

	saved, err := service.SaveTheme(ctx, branding.Theme{
		Light:  map[string]string{"primary": "#0B5FFF"},
		Radius: "rounded",
	}, initial.Version, actor)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.Contains(saved.CSS, "--primary:#0b5fff") {
		t.Fatalf("the palette was not rendered: %q", saved.CSS)
	}
	if !strings.Contains(saved.CSS, "--radius-scale:1.6") {
		t.Fatalf("the radius scale was not rendered: %q", saved.CSS)
	}
	// The stored value is normalised, so two operators who typed the same
	// colour differently do not produce two different documents.
	if saved.Theme.Light["primary"] != "#0b5fff" {
		t.Fatalf("the colour was stored unnormalised: %q", saved.Theme.Light["primary"])
	}

	// Two panel tabs, and the one still holding the old version loses.
	if _, err := service.SaveTheme(ctx, branding.Theme{
		Light: map[string]string{"primary": "#112233"},
	}, initial.Version, actor); !errors.Is(err, panelpg.ErrConflict) {
		t.Fatalf("a stale save was not a conflict: %v", err)
	}

	current, err := service.Theme(ctx)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if current.Theme.Light["primary"] != "#0b5fff" {
		t.Fatalf("the losing save overwrote the winning one: %+v", current.Theme)
	}
}

// The contrast refusal is enforced where it matters — before the row is written
// — rather than reported afterwards on a palette that is already live.
func TestAnUnreadablePaletteIsRefusedBeforeItIsStored(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "contrast@example.test")

	initial, err := service.Theme(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Near-white body text on the near-white page the design ships.
	_, err = service.SaveTheme(ctx, branding.Theme{
		Light: map[string]string{"foreground": "#f4f4f5"},
	}, initial.Version, actor)
	if !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("an unreadable palette was accepted: %v", err)
	}
	// The message names the pair, because "contrast is too low" is not something
	// an operator can act on.
	if !strings.Contains(err.Error(), "foreground on background") {
		t.Fatalf("the refusal does not name the pair: %v", err)
	}

	after, err := service.Theme(ctx)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if after.Version != initial.Version {
		t.Fatalf("a refused save still advanced the version: %d then %d",
			initial.Version, after.Version)
	}

	// A brand tone that merely fails AA is the operator's decision, and is saved
	// with the warning attached.
	failing, err := service.SaveTheme(ctx, branding.Theme{
		Light: map[string]string{"foreground": "#8a8a8a"},
	}, initial.Version, actor)
	if err != nil {
		t.Fatalf("a legible palette below AA was refused: %v", err)
	}
	if len(failing.Warnings) == 0 {
		t.Fatal("a palette below AA was saved with no warning at all")
	}
	if branding.Blocking(failing.Warnings) {
		t.Fatalf("a stored palette carries a blocking warning: %+v", failing.Warnings)
	}
}

func TestBrandingAssetsAreStoredWithTheirOwnChecksum(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "assets@example.test")

	image := onePixelPNG()
	saved, err := service.SaveBrandingAsset(ctx, "logo_light", "image/png", image, actor)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ByteSize != int64(len(image)) {
		t.Fatalf("byte size %d, want %d", saved.ByteSize, len(image))
	}

	contentType, content, checksum, err := service.BrandingAssetContent(ctx, "logo_light")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if contentType != "image/png" || !bytes.Equal(content, image) {
		t.Fatalf("the bytes did not survive the round trip")
	}
	// The validator the public route publishes has to describe the bytes it
	// serves, or a browser holds one operator's logo under another's tag.
	if checksum != saved.Checksum {
		t.Fatalf("the stored checksum %q does not match the one reported on save %q",
			checksum, saved.Checksum)
	}

	// A declared type that does not match the bytes is refused, so nothing can
	// be stored under an image content type and served back with that header.
	if _, err := service.SaveBrandingAsset(
		ctx, "logo_dark", "image/png", []byte("<svg onload=alert(1)>"), actor,
	); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a non-image was accepted as a PNG: %v", err)
	}

	// A second upload replaces rather than accumulating: there is one slot.
	second := append(onePixelPNG(), 0x00)
	replaced, err := service.SaveBrandingAsset(ctx, "logo_light", "image/png", second, actor)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if replaced.Checksum == saved.Checksum {
		t.Fatal("replacing the image left the old checksum, so a browser would keep the old logo")
	}
	assets, err := service.BrandingAssets(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("%d assets stored, want 1", len(assets))
	}

	if err := service.DeleteBrandingAsset(ctx, "logo_light", actor); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, _, err := service.BrandingAssetContent(ctx, "logo_light"); !errors.Is(err, panelpg.ErrNotFound) {
		t.Fatalf("a removed asset is still readable: %v", err)
	}
}
