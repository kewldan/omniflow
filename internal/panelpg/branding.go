package panelpg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/omniflow/omniflow/internal/branding"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ThemeSettings is the palette screen's whole state: what is stored, what it
// implies, and what is wrong with it.
type ThemeSettings struct {
	Theme branding.Theme `json:"theme"`
	// CSS is the stylesheet the panels inline. It is computed here rather than
	// in the browser so that every surface renders from one implementation and
	// so that nothing an operator typed is ever concatenated client-side.
	CSS string `json:"css"`
	// Warnings are recomputed on every read, so they cannot go stale against a
	// palette that was edited by a different route.
	Warnings []branding.Warning `json:"warnings"`
	// Themable is the list of slots an operator may set, published so the screen
	// renders the tokens this build actually honours rather than a list of its
	// own that could drift.
	Themable  []string  `json:"themable"`
	Version   int32     `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
}

// Theme reads the stored palette.
//
// A document that fails to parse — hand-edited in the database, or written by a
// build that knew a token this one does not — degrades to the shipped design
// rather than failing the request. A settings screen that cannot load is a
// screen nobody can use to fix the problem.
func (service *Service) Theme(ctx context.Context) (ThemeSettings, error) {
	section, err := service.SettingSection(ctx, "theme")
	if err != nil {
		return ThemeSettings{}, err
	}

	var stored branding.Theme
	if len(section.Document) > 0 {
		_ = json.Unmarshal(section.Document, &stored)
	}
	normalised, err := stored.Normalise()
	if err != nil {
		normalised, _ = branding.Theme{}.Normalise()
	}

	return ThemeSettings{
		Theme: normalised, CSS: normalised.CSS(), Warnings: normalised.Check(),
		Themable: branding.Themable, Version: section.Version,
		UpdatedAt: section.UpdatedAt, UpdatedBy: section.UpdatedBy,
	}, nil
}

// SaveTheme stores a palette, refusing one that cannot be read.
//
// The refusal is narrow on purpose. A brand tone that fails WCAG AA is a
// decision an operator is entitled to make and be told about; text below 3:1
// against its own background is not a decision, because there is no threshold
// in WCAG 2.2 under which that pair is usable. The first is saved with a
// warning, the second is refused with the pair named.
func (service *Service) SaveTheme(
	ctx context.Context, theme branding.Theme, expectedVersion int32, actor Actor,
) (ThemeSettings, error) {
	normalised, err := theme.Normalise()
	if err != nil {
		return ThemeSettings{}, wrapValidation(err)
	}
	warnings := normalised.Check()
	if branding.Blocking(warnings) {
		return ThemeSettings{}, wrapValidation(unreadablePalette(warnings))
	}

	document, err := json.Marshal(normalised)
	if err != nil {
		return ThemeSettings{}, err
	}
	if _, err := service.SaveSettingSection(
		ctx, "theme", document, expectedVersion, nil, actor,
	); err != nil {
		return ThemeSettings{}, err
	}
	return service.Theme(ctx)
}

// BrandingAsset is one image slot as the panel sees it. The bytes are not here:
// listing the slots must not pull three quarters of a megabyte into a response.
type BrandingAsset struct {
	Kind        string    `json:"kind"`
	ContentType string    `json:"contentType"`
	ByteSize    int64     `json:"byteSize"`
	Checksum    string    `json:"checksum"`
	UpdatedAt   time.Time `json:"updatedAt"`
	UpdatedBy   string    `json:"updatedBy,omitempty"`
}

// BrandingAssetKinds are the slots, in the order a screen renders them.
var BrandingAssetKinds = []string{"logo_light", "logo_dark", "favicon"}

// BrandingAssetTypes are the image formats accepted.
//
// SVG is absent deliberately: an SVG document can carry a script element and
// external references, and these files are served from the same origin as both
// panels. Accepting one would hand stored cross-site scripting to whoever can
// reach the settings screen.
var BrandingAssetTypes = []string{"image/png", "image/jpeg", "image/webp"}

// MaxBrandingAssetBytes matches the table's own constraint. It is stated here
// so a request can be refused with a message before a quarter of a megabyte is
// read, and enforced there so nothing else can bypass it.
const MaxBrandingAssetBytes = 262144

// BrandingAssets lists the slots that have an image.
func (service *Service) BrandingAssets(ctx context.Context) ([]BrandingAsset, error) {
	rows, err := service.queries().ListBrandingAssets(ctx)
	if err != nil {
		return nil, err
	}
	assets := make([]BrandingAsset, 0, len(rows))
	for _, row := range rows {
		assets = append(assets, BrandingAsset{
			Kind: row.Kind, ContentType: row.ContentType, ByteSize: row.ByteSize,
			Checksum: row.Checksum, UpdatedAt: row.UpdatedAt.Time,
			UpdatedBy: uuidString(row.UpdatedBy),
		})
	}
	return assets, nil
}

// BrandingAssetContent reads one image for the route that serves it.
func (service *Service) BrandingAssetContent(
	ctx context.Context, kind string,
) (contentType string, content []byte, checksum string, err error) {
	row, err := service.queries().GetBrandingAsset(ctx, kind)
	if err != nil {
		return "", nil, "", notFound(err)
	}
	return row.ContentType, row.Bytes, row.Checksum, nil
}

// SaveBrandingAsset stores an image against a slot.
//
// The checksum is computed here rather than accepted from the caller: it is the
// cache validator the public route publishes as an ETag, and a validator a
// client could choose would let one operator's upload be served under another's
// tag for as long as a browser kept it.
func (service *Service) SaveBrandingAsset(
	ctx context.Context, kind, contentType string, content []byte, actor Actor,
) (BrandingAsset, error) {
	if !allowed(BrandingAssetKinds, kind) || !allowed(BrandingAssetTypes, contentType) {
		return BrandingAsset{}, ErrValidaton
	}
	if len(content) == 0 || len(content) > MaxBrandingAssetBytes {
		return BrandingAsset{}, ErrValidaton
	}
	// The declared type has to match what the bytes are, or an operator could
	// store any file at all under an image content type and have the panel
	// serve it back with that header.
	if sniffImage(content) != contentType {
		return BrandingAsset{}, ErrValidaton
	}
	digest := sha256.Sum256(content)
	checksum := hex.EncodeToString(digest[:])

	var saved BrandingAsset
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.UpsertBrandingAsset(ctx, dbgen.UpsertBrandingAssetParams{
			Kind: kind, ContentType: contentType, Bytes: content,
			Checksum: checksum, UpdatedBy: optionalUUID(actor.AdminID),
		})
		if err != nil {
			return err
		}
		saved = BrandingAsset{
			Kind: row.Kind, ContentType: row.ContentType, ByteSize: row.ByteSize,
			Checksum: row.Checksum, UpdatedAt: row.UpdatedAt.Time,
			UpdatedBy: uuidString(row.UpdatedBy),
		}
		// The metadata records the shape of the file, never the file. An audit
		// trail that carried a quarter of a megabyte per upload would stop
		// being readable, and the auditable fact is that somebody changed the
		// logo on Tuesday.
		return appendAudit(ctx, queries, actor.audit(
			"branding.asset.saved", "configuration", "branding_asset", kind,
			map[string]any{
				"contentType": contentType, "byteSize": row.ByteSize,
				"checksum": checksum,
			},
		))
	})
	if err != nil {
		return BrandingAsset{}, err
	}
	return saved, nil
}

// DeleteBrandingAsset clears a slot, returning the installation to the shipped
// mark rather than to no mark at all.
func (service *Service) DeleteBrandingAsset(ctx context.Context, kind string, actor Actor) error {
	if !allowed(BrandingAssetKinds, kind) {
		return ErrValidaton
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, err := queries.DeleteBrandingAsset(ctx, kind)
		if err != nil {
			return err
		}
		if removed == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit(
			"branding.asset.removed", "configuration", "branding_asset", kind, nil,
		))
	})
}

func allowed(values []string, candidate string) bool {
	return slices.Contains(values, candidate)
}

// wrapValidation keeps the caller's reason attached to the sentinel, so a
// handler answers 422 and the operator is told which value was refused rather
// than only that something was.
func wrapValidation(cause error) error {
	return fmt.Errorf("%w: %s", ErrValidaton, cause.Error())
}

// unreadablePalette names the worst pair. One pair is enough to act on, and
// listing eleven of them in an error string turns a message into a log entry.
func unreadablePalette(warnings []branding.Warning) error {
	worst := branding.Warning{Ratio: 21}
	for _, warning := range warnings {
		if warning.Blocking && warning.Ratio < worst.Ratio {
			worst = warning
		}
	}
	return fmt.Errorf(
		"%s on %s in the %s palette is %.2f:1, below the 3:1 floor for any text",
		worst.Foreground, worst.Background, worst.Mode, worst.Ratio,
	)
}

// sniffImage reports what the bytes actually are, or "" for anything this
// installation does not accept.
//
// It reads the magic numbers rather than calling image.Decode, because decoding
// an attacker-supplied image to find out what it is means running a decoder
// over it — which is the thing worth avoiding, not the thing worth doing.
func sniffImage(content []byte) string {
	switch {
	case len(content) >= 8 && string(content[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(content) >= 3 && content[0] == 0xFF && content[1] == 0xD8 && content[2] == 0xFF:
		return "image/jpeg"
	case len(content) >= 12 && string(content[:4]) == "RIFF" && string(content[8:12]) == "WEBP":
		return "image/webp"
	default:
		return ""
	}
}
