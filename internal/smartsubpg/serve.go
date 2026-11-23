package smartsubpg

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/omniflow/omniflow/internal/smartsub"
)

// Response is what the link answers a client with.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	// Rewritten says whether the module changed the panel's answer.
	Rewritten bool
}

// hopHeaders are the panel's response headers that describe its own
// connection or body encoding rather than the subscription, and must not be
// copied onto a body this process writes.
var hopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Transfer-Encoding": true, "Content-Length": true,
	"Content-Encoding": true, "Set-Cookie": true, "Etag": true, "Last-Modified": true,
	"Strict-Transport-Security": true, "Alt-Svc": true, "Server": true, "Date": true,
}

// Serve answers one subscription request for the link with this short UUID.
//
// With the module off the panel's answer passes through unchanged, so a link
// a client imported while it was on keeps working after it is turned off.
// With it on, an Xray JSON client gets the rewritten groups and every other
// client the panel's own answer with hidden servers taken out. Anything the
// module cannot rewrite reaches the client exactly as the panel sent it.
func (service *Service) Serve(ctx context.Context, short string, from http.Header, clientIP string) (Response, error) {
	if !remnawave.ValidShortUUID(short) {
		return Response{}, ErrNotFound
	}
	settings := service.Settings(ctx)
	if !settings.Enabled {
		return service.passThrough(ctx, short, "", from, clientIP)
	}
	format := smartsub.DetectFormat(from.Get("User-Agent"))
	panelFormat := ""
	if format == smartsub.FormatXray {
		panelFormat = remnawave.SubscriptionFormatXrayJSON
	}
	content, err := service.panel.FetchSubscription(ctx, short, panelFormat, from, clientIP)
	if err != nil {
		return Response{}, ErrPanelUnavailable
	}
	if content.Status != http.StatusOK {
		if panelFormat != "" && content.Status != http.StatusNotFound {
			// The panel refuses the format-by-path route when its operator
			// turned that off. Its own choice for the client still works.
			return service.passThrough(ctx, short, "", from, clientIP)
		}
		return copyResponse(content), nil
	}

	preferences, locale := service.preferencesFor(ctx, short, settings, from)
	request := smartsub.Request{
		Upstream: content.Body, Settings: settings, Preferences: preferences,
		Catalog: service.Catalog(ctx), Locale: locale,
	}
	var body []byte
	if format == smartsub.FormatXray {
		body, err = smartsub.BuildXray(request)
	} else {
		body, err = smartsub.FilterLinks(request)
	}
	if errors.Is(err, smartsub.ErrPassThrough) {
		return copyResponse(content), nil
	}
	if err != nil {
		service.logger.Error("smart subscription rewrite failed", "error", err)
		return copyResponse(content), nil
	}
	response := copyResponse(content)
	response.Body, response.Rewritten = body, true
	if format == smartsub.FormatXray {
		response.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	return response, nil
}

func (service *Service) passThrough(ctx context.Context, short, format string, from http.Header, clientIP string) (Response, error) {
	content, err := service.panel.FetchSubscription(ctx, short, format, from, clientIP)
	if err != nil {
		return Response{}, ErrPanelUnavailable
	}
	return copyResponse(content), nil
}

func copyResponse(content remnawave.SubscriptionContent) Response {
	header := http.Header{}
	for name, values := range content.Header {
		if !hopHeaders[http.CanonicalHeaderKey(name)] {
			header[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
	return Response{Status: content.Status, Header: header, Body: content.Body}
}

// preferencesFor finds the customer behind a link and their choices. A link
// Omniflow does not know — a panel user created by hand — gets the operator's
// defaults, in the language the client asked for.
func (service *Service) preferencesFor(
	ctx context.Context, short string, settings smartsub.Settings, from http.Header,
) (smartsub.Preferences, string) {
	locale := languageOf(from.Get("Accept-Language"))
	defaults := settings.DefaultPreferences()
	remnawaveID, err := service.remnawaveUser(ctx, short)
	if err != nil || remnawaveID <= 0 {
		return defaults, locale
	}
	var (
		subscriptionID string
		customerLocale string
	)
	err = service.pool.QueryRow(ctx, `SELECT s.id::text, u.locale FROM subscriptions s
		JOIN users u ON u.id = s.user_id
		WHERE s.remnawave_user_id = $1
		ORDER BY s.status = 'active' DESC, s.created_at DESC LIMIT 1`, remnawaveID).Scan(&subscriptionID, &customerLocale)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			service.logger.Warn("smart subscription customer lookup failed", "error", err)
		}
		return defaults, locale
	}
	preferences, found, err := readPreferences(ctx, service.pool, subscriptionID)
	if err != nil {
		service.logger.Warn("smart subscription preferences read failed", "error", err)
		return defaults, customerLocale
	}
	if !found {
		return defaults, customerLocale
	}
	return applicable(preferences, settings), customerLocale
}

// applicable is what of a customer's saved choices the operator's current
// settings still allow: rules saved before custom rules were turned off, or
// beyond a lowered limit, stop applying without being deleted.
func applicable(preferences smartsub.Preferences, settings smartsub.Settings) smartsub.Preferences {
	if !settings.CustomRules {
		preferences.Rules = []smartsub.Rule{}
	} else if settings.MaxRules > 0 && len(preferences.Rules) > settings.MaxRules {
		preferences.Rules = preferences.Rules[:settings.MaxRules]
	}
	return preferences
}

func languageOf(header string) string {
	first, _, _ := strings.Cut(header, ",")
	first, _, _ = strings.Cut(first, ";")
	return strings.ToLower(strings.TrimSpace(first))
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readPreferences(ctx context.Context, querier rowQuerier, subscriptionID string) (smartsub.Preferences, bool, error) {
	var (
		preferences smartsub.Preferences
		rules       []byte
	)
	err := querier.QueryRow(ctx, `SELECT hidden_countries, auto_excluded, reserve, russia_direct, block_ads, block_quic, rules
		FROM smart_subscription_preferences WHERE subscription_id = $1`, subscriptionID).Scan(
		&preferences.HiddenCountries, &preferences.AutoExcluded, &preferences.Reserve,
		&preferences.Presets.RussiaDirect, &preferences.Presets.BlockAds, &preferences.Presets.BlockQUIC, &rules,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return smartsub.Preferences{}, false, nil
	}
	if err != nil {
		return smartsub.Preferences{}, false, err
	}
	if err := json.Unmarshal(rules, &preferences.Rules); err != nil {
		return smartsub.Preferences{}, false, err
	}
	if preferences.Rules == nil {
		preferences.Rules = []smartsub.Rule{}
	}
	return preferences, true, nil
}
