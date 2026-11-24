// Package smartsubpg runs the smart subscription (internal/smartsub) against
// PostgreSQL and the Remnawave adapter: it serves the link VPN clients
// import, keeps a short-lived copy of the panel's hosts and node status, and
// stores what each customer chose for their own subscription.
//
// The operator's settings are written by internal/panelpg, which audits the
// change; this package only reads them.
package smartsubpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/omniflow/omniflow/internal/smartsub"
)

// Panel is what the module reads from Remnawave.
type Panel interface {
	Hosts(ctx context.Context) ([]remnawave.Host, error)
	Nodes(ctx context.Context) ([]remnawave.Node, error)
	InternalSquadInbounds(ctx context.Context) (map[string][]string, map[string]string, error)
	User(ctx context.Context, userID int64) (remnawave.User, error)
	UserByShortUUID(ctx context.Context, shortUUID string) (remnawave.User, error)
	FetchSubscription(ctx context.Context, shortUUID, format string, from http.Header, clientIP string) (remnawave.SubscriptionContent, error)
	RequestGeocheck(ctx context.Context, nodeUUID string) (string, error)
	Geocheck(ctx context.Context, jobID string) (remnawave.GeocheckJob, error)
}

var (
	// ErrNotFound is a subscription that is not the customer's, or a link the
	// panel does not know.
	ErrNotFound = errors.New("smart subscription not found")
	// ErrDisabled is the module switched off.
	ErrDisabled = errors.New("smart subscription is off")
	// ErrNotProvisioned is a subscription with no panel user yet.
	ErrNotProvisioned = errors.New("subscription has no panel user yet")
	// ErrPanelUnavailable is a panel that did not answer.
	ErrPanelUnavailable = errors.New("remnawave is unavailable")
)

// Cache periods. Settings are read on every subscription request, so they are
// held briefly; the host list changes when an operator edits the panel, and a
// minute's delay there is invisible next to how often clients refresh.
const (
	settingsTTL   = 15 * time.Second
	catalogTTL    = time.Minute
	userTTL       = 10 * time.Minute
	userCacheSize = 20000
)

// Service runs the module.
type Service struct {
	pool      *pgxpool.Pool
	panel     Panel
	logger    *slog.Logger
	publicURL string
	clock     func() time.Time

	mu         sync.Mutex
	settings   smartsub.Settings
	settingsAt time.Time
	catalog    smartsub.Catalog
	catalogAt  time.Time
	users      map[string]cachedUser
	checkStore CheckStore
	checks     map[string]smartsub.NodeCheck
	checksAt   time.Time
	// logoToken is the logo.dev publishable key (APP_LOGO_DEV_TOKEN); empty
	// leaves logo search off and icons upload-only.
	logoToken string
}

// WithLogoSearch lets the operator fetch a rule's icon by domain from
// logo.dev with this publishable key.
func (service *Service) WithLogoSearch(token string) *Service {
	service.logoToken = strings.TrimSpace(token)
	return service
}

type cachedUser struct {
	remnawaveID int64
	at          time.Time
}

// New builds the service. publicURL is APP_PUBLIC_URL, the default base of
// the link when the operator did not set one.
func New(pool *pgxpool.Pool, panel Panel, publicURL string, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		pool: pool, panel: panel, logger: logger, clock: time.Now,
		publicURL: strings.TrimRight(strings.TrimSpace(publicURL), "/"),
		users:     map[string]cachedUser{},
	}
}

// Settings reads the operator's settings, normalized, through a short cache.
// A read failure keeps the last good copy, or the module off.
func (service *Service) Settings(ctx context.Context) smartsub.Settings {
	service.mu.Lock()
	cached, at := service.settings, service.settingsAt
	service.mu.Unlock()
	if !at.IsZero() && service.clock().Sub(at) < settingsTTL {
		return cached
	}
	settings, err := ReadSettings(ctx, service.pool)
	if err != nil {
		service.logger.Warn("smart subscription settings read failed", "error", err)
		if at.IsZero() {
			return defaultSettings()
		}
		return cached
	}
	service.mu.Lock()
	service.settings, service.settingsAt = settings, service.clock()
	service.mu.Unlock()
	return settings
}

// Invalidate drops the cached settings, so a save in this process applies at
// once.
func (service *Service) Invalidate() {
	service.mu.Lock()
	service.settingsAt = time.Time{}
	service.mu.Unlock()
}

// defaultSettings are the settings of an installation that saved none, with
// the built-in rules in place.
func defaultSettings() smartsub.Settings {
	settings, err := smartsub.DefaultSettings().Normalize()
	if err != nil {
		return smartsub.DefaultSettings()
	}
	return settings
}

// ReadSettings reads the settings row. It is shared with the operator panel,
// which shows exactly what the module runs on.
func ReadSettings(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (smartsub.Settings, error) {
	var (
		enabled  bool
		document []byte
	)
	err := querier.QueryRow(ctx, `SELECT enabled, document FROM smart_subscription_settings WHERE singleton`).
		Scan(&enabled, &document)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultSettings(), nil
	}
	if err != nil {
		return smartsub.Settings{}, err
	}
	settings := smartsub.DefaultSettings()
	if len(document) > 0 {
		if err := json.Unmarshal(document, &settings); err != nil {
			return smartsub.Settings{}, fmt.Errorf("decode smart subscription settings: %w", err)
		}
	}
	settings.Enabled = enabled
	normalized, err := settings.Normalize()
	if err != nil {
		// A document that no longer validates — a country code a later version
		// dropped — must not take every subscription down with it.
		return defaultSettings(), fmt.Errorf("stored smart subscription settings: %w", err)
	}
	return normalized, nil
}

// Catalog is the panel's hosts and down nodes through a one-minute cache. A
// panel that does not answer leaves the last good copy, or an empty one: the
// module then places servers by their remarks alone.
func (service *Service) Catalog(ctx context.Context) smartsub.Catalog {
	service.mu.Lock()
	cached, at := service.catalog, service.catalogAt
	service.mu.Unlock()
	if !at.IsZero() && service.clock().Sub(at) < catalogTTL {
		return cached
	}
	catalog, err := service.FreshCatalog(ctx)
	if err != nil {
		service.logger.Warn("smart subscription could not read hosts", "error", err)
		service.mu.Lock()
		// Try again in a minute rather than on every request.
		service.catalogAt = service.clock()
		service.mu.Unlock()
		return cached
	}
	return catalog
}

// FreshCatalog reads the hosts and nodes from the panel now, and refreshes
// the cache with them. The operator's host list uses it, so a host added a
// moment ago shows up.
func (service *Service) FreshCatalog(ctx context.Context) (smartsub.Catalog, error) {
	hosts, err := service.panel.Hosts(ctx)
	if err != nil {
		return smartsub.Catalog{}, err
	}
	catalog := smartsub.Catalog{DownNodes: map[string]bool{}, NodeNames: map[string]string{}}
	for _, host := range hosts {
		catalog.Hosts = append(catalog.Hosts, smartsub.Host{
			UUID: strings.ToLower(host.UUID), Remark: host.Remark, Address: host.Address, Port: host.Port,
			Nodes: host.Nodes, Disabled: host.IsDisabled, Hidden: host.IsHidden,
			InboundUUID: host.Inbound.ConfigProfileInboundUUID,
			SquadMode:   host.InternalSquads.Mode, Squads: host.InternalSquads.Squads,
		})
	}
	// The squads decide which hosts a customer's screen lists; without them
	// the screen says the list is unavailable rather than guessing.
	if squads, types, err := service.panel.InternalSquadInbounds(ctx); err == nil {
		catalog.SquadInbounds = squads
		for index := range catalog.Hosts {
			catalog.Hosts[index].Transport = smartsub.TransportOf(types[catalog.Hosts[index].InboundUUID])
		}
	}
	// Node status only ever removes servers, so a panel that will not list
	// its nodes leaves every server in rather than failing the read.
	if nodes, err := service.panel.Nodes(ctx); err == nil {
		for _, node := range nodes {
			catalog.NodeNames[strings.ToLower(node.UUID)] = node.Name
			if node.IsDisabled || !node.IsConnected {
				catalog.DownNodes[node.UUID] = true
			}
		}
	}
	service.mu.Lock()
	service.catalog, service.catalogAt = catalog, service.clock()
	service.mu.Unlock()
	return catalog, nil
}

// LinkBase is where links are built, or "" when the module cannot build one:
// no base set and no public URL to default to.
func (service *Service) LinkBase(settings smartsub.Settings) string {
	if settings.LinkBase != "" {
		return settings.LinkBase
	}
	if service.publicURL == "" {
		return ""
	}
	return service.publicURL + "/v1/sub/"
}

// Link turns the panel's subscription URL into the module's, when the module
// is on. The short UUID is the last segment of the panel's link.
func (service *Service) Link(ctx context.Context, panelURL string) string {
	settings := service.Settings(ctx)
	if !settings.Enabled || panelURL == "" {
		return panelURL
	}
	base := service.LinkBase(settings)
	short := ShortUUID(panelURL)
	if base == "" || short == "" {
		return panelURL
	}
	return base + short
}

// ShortUUID reads the short UUID out of a panel subscription link.
func ShortUUID(panelURL string) string {
	parsed, err := url.Parse(panelURL)
	if err != nil {
		return ""
	}
	segment := parsed.Path[strings.LastIndexByte(parsed.Path, '/')+1:]
	if !remnawave.ValidShortUUID(segment) {
		return ""
	}
	return segment
}

// remnawaveUser finds the panel user behind a short UUID, through a cache:
// the mapping only changes when the link is reissued, and a reissued link is
// a different short UUID. It is a plain user read, so it neither checks nor
// registers a device.
func (service *Service) remnawaveUser(ctx context.Context, short string) (int64, error) {
	now := service.clock()
	service.mu.Lock()
	cached, ok := service.users[short]
	service.mu.Unlock()
	if ok && now.Sub(cached.at) < userTTL {
		return cached.remnawaveID, nil
	}
	user, err := service.panel.UserByShortUUID(ctx, short)
	if err != nil {
		return 0, err
	}
	service.mu.Lock()
	if len(service.users) >= userCacheSize {
		service.users = map[string]cachedUser{}
	}
	service.users[short] = cachedUser{remnawaveID: user.ID, at: now}
	service.mu.Unlock()
	return user.ID, nil
}
