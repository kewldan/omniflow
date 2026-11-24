package smartsubpg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/smartsub"
)

// MaxIconBytes bounds an uploaded icon: a card shows it at 36 px.
const MaxIconBytes = 128 << 10

var (
	// ErrIconInvalid is an upload that is not a small PNG, JPEG, WebP, or a
	// plain SVG.
	ErrIconInvalid = errors.New("icon must be a PNG, JPEG, WebP, or SVG under 128 KB")
	// ErrIconNotFound is an icon ID nobody uploaded.
	ErrIconNotFound = errors.New("icon not found")
	// ErrLogoSearchOff is a logo search on an installation without a
	// logo.dev key.
	ErrLogoSearchOff = errors.New("logo search is not configured")
	// ErrLogoNotFound is a domain logo.dev has no logo for.
	ErrLogoNotFound = errors.New("no logo for that domain")
)

// Icon is a stored icon.
type Icon struct {
	ID          string
	ContentType string
	Data        []byte
}

// unsafeSVG matches what an SVG needs to run code. The icon is served with a
// sandboxing policy anyway; refusing these keeps a stored icon inert even if
// something serves it without one.
var unsafeSVG = regexp.MustCompile(`(?i)<script|<foreignobject|\bon[a-z]+\s*=|javascript:|<iframe|<embed|<object`)

// iconType names an icon's type from its bytes, never from what the uploader
// claimed.
func iconType(data []byte) (string, error) {
	if len(data) == 0 || len(data) > MaxIconBytes {
		return "", ErrIconInvalid
	}
	switch detected := http.DetectContentType(data); detected {
	case "image/png", "image/jpeg", "image/webp":
		return detected, nil
	}
	head := bytes.TrimSpace(data)
	if len(head) > 512 {
		head = head[:512]
	}
	lowered := strings.ToLower(string(head))
	if (strings.HasPrefix(lowered, "<svg") || strings.HasPrefix(lowered, "<?xml")) && strings.Contains(strings.ToLower(string(data)), "<svg") {
		if unsafeSVG.Match(data) {
			return "", ErrIconInvalid
		}
		return "image/svg+xml", nil
	}
	return "", ErrIconInvalid
}

// SaveIcon stores an icon and returns its ID. The same bytes uploaded twice
// keep one row.
func (service *Service) SaveIcon(ctx context.Context, data []byte, operatorID string) (string, error) {
	contentType, err := iconType(data)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	var operator pgtype.UUID
	_ = operator.Scan(operatorID)
	var id pgtype.UUID
	err = service.pool.QueryRow(ctx, `INSERT INTO smart_subscription_icons (content_type, data, sha256, created_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (sha256) DO UPDATE SET sha256 = EXCLUDED.sha256
		RETURNING id`, contentType, data, sum[:], operator).Scan(&id)
	if err != nil {
		return "", err
	}
	return uuidText(id), nil
}

// Icon reads one icon.
func (service *Service) Icon(ctx context.Context, id string) (Icon, error) {
	var key pgtype.UUID
	if key.Scan(id) != nil {
		return Icon{}, ErrIconNotFound
	}
	icon := Icon{ID: strings.ToLower(id)}
	err := service.pool.QueryRow(ctx, `SELECT content_type, data FROM smart_subscription_icons WHERE id = $1`, key).
		Scan(&icon.ContentType, &icon.Data)
	if errors.Is(err, pgx.ErrNoRows) {
		return Icon{}, ErrIconNotFound
	}
	return icon, err
}

// LogoSearch reports whether the installation can fetch logos by domain.
func (service *Service) LogoSearch() bool { return service.logoToken != "" }

// FetchLogo downloads a site's logo from logo.dev once and stores it as an
// icon, so customers' browsers never ask a third party for it.
func (service *Service) FetchLogo(ctx context.Context, rawDomain, operatorID string) (string, error) {
	if service.logoToken == "" {
		return "", ErrLogoSearchOff
	}
	domain, err := smartsub.NormalizeDomain(rawDomain)
	if err != nil {
		return "", &smartsub.FieldError{Field: "domain", Reason: err.Error()}
	}
	endpoint := "https://img.logo.dev/" + url.PathEscape(domain) + "?" + url.Values{
		"token": {service.logoToken}, "size": {"128"}, "format": {"png"}, "fallback": {"404"},
	}.Encode()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		// The URL carries the key; the error must not.
		return "", errors.New("logo.dev did not answer")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return "", ErrLogoNotFound
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("logo.dev answered %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxIconBytes+1))
	if err != nil {
		return "", errors.New("logo.dev did not answer")
	}
	return service.SaveIcon(ctx, data, operatorID)
}
