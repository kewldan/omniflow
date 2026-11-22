package remnawave

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const maxResponseBytes = 1 << 20

var ErrNotFound = errors.New("remnawave resource not found")

type Service interface {
	User(context.Context, int64) (User, error)
	UserByTelegramID(context.Context, int64) (User, error)
	Subscription(context.Context, int64) (Subscription, error)
	Devices(context.Context, int64) (Devices, error)
	DeleteDevice(context.Context, int64, string) error
	DeleteAllDevices(context.Context, int64) error
	RevokeSubscription(context.Context, int64) error
}

type Provisioner interface {
	User(context.Context, int64) (User, error)
	UserByUsername(context.Context, string) (User, error)
	CreateUser(context.Context, ProvisionUser) (User, error)
	UpdateUser(context.Context, int64, ProvisionUser) (User, error)
	EnableUser(context.Context, int64) error
	DisableUser(context.Context, int64) error
	ResetUserTraffic(context.Context, int64) error
	// DeleteAllDevices removes every HWID device a user has registered, which
	// a renewal does when the installation resets devices on renewal.
	DeleteAllDevices(context.Context, int64) error
	// DeleteUser removes a user from the panel. Closing a subscription does
	// this when the installation deletes rather than disables.
	DeleteUser(context.Context, int64) error
}

type Importer interface {
	ListUsers(context.Context, int, int) ([]User, int, error)
}

// ProvisionUser is the desired state pushed to Remnawave for one user.
//
// The two limits are always on the wire, and a nil limit means unlimited.
// Remnawave reads an absent field as "leave it as it is" — which is how a plan
// with no limit used to keep the previous plan's limit after a renewal — so a
// nil limit has to be sent as the panel's explicit unlimited value rather than
// being omitted.
//
// For both limits that value is zero. `trafficLimitBytes: 0` is unlimited
// traffic, and `hwidDeviceLimit: 0` bypasses the device check entirely. A null
// device limit is not unlimited: the panel substitutes the global HWID
// fallback limit for it, and on a user create the contract rejects null with a
// 400 because the field is optional but not nullable there.
type ProvisionUser struct {
	Username          string
	ExpireAt          time.Time
	TrafficLimitBytes *int64
	HWIDDeviceLimit   *int
	InternalSquadIDs  []string
	// TrafficLimitStrategy is when the panel resets the traffic counter on its
	// own: NO_RESET, DAY, WEEK, MONTH, or MONTH_ROLLING. Empty leaves it out,
	// which the panel reads as NO_RESET on a create and "unchanged" on an
	// update.
	TrafficLimitStrategy string
	// ExternalSquadID is the external squad that shapes the user's
	// subscription page. Empty leaves whatever the panel holds.
	ExternalSquadID string
	// Tag is the panel's user tag. It is sent only while TagManaged is set,
	// and then an empty tag clears it: once Omniflow tags users, a trial tag
	// must not outlive the purchase. With TagManaged unset the field is left
	// out and a tag an operator set by hand is untouched.
	Tag        string
	TagManaged bool
	// TelegramID and Description are sent when the user is created and never
	// on an update: they describe who the user is for, and an operator who
	// edited either in the panel has said something Omniflow must not undo.
	TelegramID  *int64
	Description string
}

// createWire is the POST /api/users body.
type createWire struct {
	Username             string    `json:"username"`
	ExpireAt             time.Time `json:"expireAt"`
	TrafficLimitBytes    int64     `json:"trafficLimitBytes"`
	HWIDDeviceLimit      int       `json:"hwidDeviceLimit"`
	InternalSquadIDs     []string  `json:"activeInternalSquads,omitempty"`
	TrafficLimitStrategy string    `json:"trafficLimitStrategy,omitempty"`
	ExternalSquadID      string    `json:"externalSquadUuid,omitempty"`
	Tag                  string    `json:"tag,omitempty"`
	TelegramID           *int64    `json:"telegramId,omitempty"`
	Description          string    `json:"description,omitempty"`
}

// updateWire is the PATCH /api/users body. The contract addresses the user by
// exactly one of `id` or `username`; Omniflow always knows the numeric ID, and
// the panel cannot rename a user through this call anyway, so the username is
// not sent.
//
// Tag is a pointer to a pointer so the three cases stay apart: left out, sent
// as null to clear, or sent as a value.
type updateWire struct {
	ID                   int64     `json:"id"`
	ExpireAt             time.Time `json:"expireAt"`
	TrafficLimitBytes    int64     `json:"trafficLimitBytes"`
	HWIDDeviceLimit      int       `json:"hwidDeviceLimit"`
	InternalSquadIDs     []string  `json:"activeInternalSquads,omitempty"`
	TrafficLimitStrategy string    `json:"trafficLimitStrategy,omitempty"`
	ExternalSquadID      string    `json:"externalSquadUuid,omitempty"`
	Tag                  **string  `json:"tag,omitempty"`
}

// withoutExternalSquad is the same desired state with the external squad left
// out. A squad deleted in the panel after a plan named it fails the whole
// write, and the user's access matters more than the page's shape.
func (desired ProvisionUser) withoutExternalSquad() ProvisionUser {
	desired.ExternalSquadID = ""
	return desired
}

// Error codes the panel answers a write naming an external squad it no
// longer has with: A018 on a create, A039 on an update, A182 for the squad.
var staleExternalSquadCodes = map[string]bool{"A018": true, "A039": true, "A182": true}

func staleExternalSquad(err error) bool {
	var apiError *APIError
	return errors.As(err, &apiError) && staleExternalSquadCodes[apiError.Code]
}

// limits resolves the two limits to their wire values; see ProvisionUser.
func (desired ProvisionUser) limits() (traffic int64, devices int) {
	if desired.TrafficLimitBytes != nil {
		traffic = *desired.TrafficLimitBytes
	}
	if desired.HWIDDeviceLimit != nil {
		devices = *desired.HWIDDeviceLimit
	}
	return traffic, devices
}

func (desired ProvisionUser) createWire() createWire {
	traffic, devices := desired.limits()
	wire := createWire{
		Username: desired.Username, ExpireAt: desired.ExpireAt, TrafficLimitBytes: traffic,
		HWIDDeviceLimit: devices, InternalSquadIDs: desired.InternalSquadIDs,
		TrafficLimitStrategy: desired.TrafficLimitStrategy, ExternalSquadID: desired.ExternalSquadID,
		TelegramID: desired.TelegramID, Description: desired.Description,
	}
	if desired.TagManaged {
		wire.Tag = desired.Tag
	}
	return wire
}

func (desired ProvisionUser) updateWire(userID int64) updateWire {
	traffic, devices := desired.limits()
	wire := updateWire{
		ID: userID, ExpireAt: desired.ExpireAt, TrafficLimitBytes: traffic,
		HWIDDeviceLimit: devices, InternalSquadIDs: desired.InternalSquadIDs,
		TrafficLimitStrategy: desired.TrafficLimitStrategy, ExternalSquadID: desired.ExternalSquadID,
	}
	if desired.TagManaged {
		var tag *string
		if desired.Tag != "" {
			value := desired.Tag
			tag = &value
		}
		wire.Tag = &tag
	}
	return wire
}

type InternalSquad struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// User is a Remnawave user as the panel reports it. ID is the panel's numeric
// user ID, the only identifier its user, subscription, and HWID routes accept.
type User struct {
	ID                int64     `json:"id"`
	Username          string    `json:"username"`
	Status            string    `json:"status"`
	TrafficLimitBytes int64     `json:"trafficLimitBytes"`
	ExpireAt          time.Time `json:"expireAt"`
	// HWIDDeviceLimit is the panel's raw value: zero is unlimited, null means
	// the panel's global fallback limit applies, anything else is the limit.
	HWIDDeviceLimit      *int            `json:"hwidDeviceLimit"`
	SubscriptionURL      string          `json:"subscriptionUrl"`
	TelegramID           *int64          `json:"telegramId"`
	ActiveInternalSquads []InternalSquad `json:"activeInternalSquads"`
	Traffic              UserTraffic     `json:"userTraffic"`
}

func (client *Client) ListUsers(ctx context.Context, start, size int) ([]User, int, error) {
	endpoint := "/api/users/?start=" + strconv.Itoa(start) + "&size=" + strconv.Itoa(size)
	var envelope struct {
		Response struct {
			Users []User `json:"users"`
			Total int    `json:"total"`
		} `json:"response"`
	}
	if err := client.get(ctx, endpoint, &envelope); err != nil {
		return nil, 0, err
	}
	return envelope.Response.Users, envelope.Response.Total, nil
}

type UserTraffic struct {
	UsedBytes     int64      `json:"usedTrafficBytes"`
	LifetimeBytes int64      `json:"lifetimeUsedTrafficBytes"`
	OnlineAt      *time.Time `json:"onlineAt"`
	// FirstConnectedAt is when the user first connected through any node,
	// or nil for a user who never has.
	FirstConnectedAt *time.Time `json:"firstConnectedAt"`
	// LastConnectedNodeUUID is the node the user last connected through.
	LastConnectedNodeUUID string `json:"lastConnectedNodeUuid"`
}

// PanelDevice is one HWID device in the panel-wide device list.
type PanelDevice struct {
	UserID    int64     `json:"userId"`
	Platform  *string   `json:"platform"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AllDevices pages through every HWID device the panel knows. The hardware
// ID is deliberately not decoded: the passes that read this count devices,
// they do not identify them.
func (client *Client) AllDevices(ctx context.Context, start, size int) ([]PanelDevice, int, error) {
	endpoint := "/api/hwid/devices?start=" + strconv.Itoa(start) + "&size=" + strconv.Itoa(size)
	var envelope struct {
		Response struct {
			Devices []PanelDevice `json:"devices"`
			Total   int           `json:"total"`
		} `json:"response"`
	}
	if err := client.get(ctx, endpoint, &envelope); err != nil {
		return nil, 0, err
	}
	return envelope.Response.Devices, envelope.Response.Total, nil
}

type Subscription struct {
	Found           bool             `json:"isFound"`
	User            SubscriptionUser `json:"user"`
	SubscriptionURL string           `json:"subscriptionUrl"`
}

type SubscriptionUser struct {
	DaysLeft          int64     `json:"daysLeft"`
	TrafficUsed       string    `json:"trafficUsed"`
	TrafficLimit      string    `json:"trafficLimit"`
	Username          string    `json:"username"`
	ExpiresAt         time.Time `json:"expiresAt"`
	Active            bool      `json:"isActive"`
	Status            string    `json:"userStatus"`
	TrafficUsedBytes  string    `json:"trafficUsedBytes"`
	TrafficLimitBytes string    `json:"trafficLimitBytes"`
}

type Devices struct {
	Total   int      `json:"total"`
	Devices []Device `json:"devices"`
}

type Device struct {
	HWID        string    `json:"hwid"`
	Platform    *string   `json:"platform"`
	OSVersion   *string   `json:"osVersion"`
	DeviceModel *string   `json:"deviceModel"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Error codes the panel puts in the `errorCode` field of a failure body. Only
// the ones the adapter acts on are named.
const (
	codeUserAlreadyDisabled = "A029"
	codeUserAlreadyEnabled  = "A030"
)

// APIError is a non-2xx answer from the panel.
//
// Code is the panel's own `errorCode` (for example A025 for an unknown user)
// when the failure body carried one, and empty otherwise — a schema validation
// failure, a proxy's error page, or an empty body. The panel's message is
// deliberately not kept: it can echo request values, and this error ends up in
// job records and logs.
type APIError struct {
	StatusCode int
	Code       string
}

func (err *APIError) Error() string {
	if err.Code != "" {
		return fmt.Sprintf("remnawave API returned HTTP %d (%s)", err.StatusCode, err.Code)
	}
	return fmt.Sprintf("remnawave API returned HTTP %d", err.StatusCode)
}

// hasCode reports whether err is a panel failure carrying the given code.
func hasCode(err error, code string) bool {
	var apiError *APIError
	return errors.As(err, &apiError) && apiError.Code == code
}

type Client struct {
	baseURL  *url.URL
	token    string
	http     *http.Client
	options  Options
	limiter  *limiter
	observer Observer
}

// Observer is told about every panel call: a bounded operation name built
// from the method and the path template, an outcome of "succeeded",
// "not_found", or "failed", a classified code for a failure, and the time the
// call took. Nothing that identifies a user ever reaches it.
type Observer func(operation, outcome, code string, elapsed time.Duration)

// WithObserver installs the observer and returns the client.
func (client *Client) WithObserver(observer Observer) *Client {
	client.observer = observer
	return client
}

// NewClient builds the adapter. At most one Options value may follow the
// token; without it the client authenticates with the bearer token alone and
// waits ten seconds for each call, which is what a panel reached directly or
// through its own reverse proxy needs.
func NewClient(rawURL, token string, extra ...Options) (*Client, error) {
	var options Options
	if len(extra) > 0 {
		options = extra[0]
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	baseURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || baseURL.Host == "" || (baseURL.Scheme != "http" && baseURL.Scheme != "https") {
		return nil, errors.New("Remnawave URL must be an absolute HTTP(S) URL")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("Remnawave API token is required")
	}
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	return &Client{
		baseURL: baseURL,
		token:   token,
		options: options,
		limiter: newLimiter(options.RequestsPerMinute),
		// Every panel call is traced as a child of the request or job that
		// caused it, which is what makes a slow Remnawave visible end to end.
		http: &http.Client{
			Timeout:   timeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (client *Client) User(ctx context.Context, userID int64) (User, error) {
	var envelope struct {
		Response User `json:"response"`
	}
	err := client.get(ctx, "/api/users/"+strconv.FormatInt(userID, 10), &envelope)
	return envelope.Response, err
}

func (client *Client) UserByTelegramID(ctx context.Context, telegramID int64) (User, error) {
	endpoint := "/api/users/stream?size=2&telegramId=" + url.QueryEscape(strconv.FormatInt(telegramID, 10))
	var envelope struct {
		Response struct {
			Users []User `json:"users"`
		} `json:"response"`
	}
	if err := client.get(ctx, endpoint, &envelope); err != nil {
		return User{}, err
	}
	if len(envelope.Response.Users) == 0 {
		return User{}, ErrNotFound
	}
	if len(envelope.Response.Users) > 1 {
		return User{}, errors.New("multiple Remnawave users share one Telegram ID")
	}
	return envelope.Response.Users[0], nil
}

func (client *Client) UserByUsername(ctx context.Context, username string) (User, error) {
	var envelope struct {
		Response User `json:"response"`
	}
	err := client.get(ctx, "/api/users/by-username/"+url.PathEscape(username), &envelope)
	return envelope.Response, err
}

func (client *Client) CreateUser(ctx context.Context, desired ProvisionUser) (User, error) {
	var envelope struct {
		Response User `json:"response"`
	}
	err := client.do(ctx, http.MethodPost, "/api/users/", desired.createWire(), &envelope)
	if err != nil && desired.ExternalSquadID != "" && staleExternalSquad(err) {
		err = client.do(ctx, http.MethodPost, "/api/users/", desired.withoutExternalSquad().createWire(), &envelope)
	}
	return envelope.Response, err
}

func (client *Client) UpdateUser(ctx context.Context, userID int64, desired ProvisionUser) (User, error) {
	var envelope struct {
		Response User `json:"response"`
	}
	err := client.do(ctx, http.MethodPatch, "/api/users/", desired.updateWire(userID), &envelope)
	if err != nil && desired.ExternalSquadID != "" && staleExternalSquad(err) {
		err = client.do(ctx, http.MethodPatch, "/api/users/", desired.withoutExternalSquad().updateWire(userID), &envelope)
	}
	return envelope.Response, err
}

// EnableUser asks the panel to enable a user. A user that is already enabled
// is success: the panel answers that case with a 400 (A030), and treating it
// as a failure would turn a retried operation whose first attempt landed — or
// a resume against a user somebody already re-enabled — into a permanent error.
func (client *Client) EnableUser(ctx context.Context, userID int64) error {
	err := client.post(ctx, "/api/users/"+strconv.FormatInt(userID, 10)+"/actions/enable", nil)
	if hasCode(err, codeUserAlreadyEnabled) {
		return nil
	}
	return err
}

// DisableUser is EnableUser's mirror: an already-disabled user (A029) is
// success, for the same retry reason.
func (client *Client) DisableUser(ctx context.Context, userID int64) error {
	err := client.post(ctx, "/api/users/"+strconv.FormatInt(userID, 10)+"/actions/disable", nil)
	if hasCode(err, codeUserAlreadyDisabled) {
		return nil
	}
	return err
}

// DeleteUser deletes a user. A user that is already gone is success: the
// state asked for is the state the panel is in, and a retried operation whose
// first attempt landed must not fail for good.
func (client *Client) DeleteUser(ctx context.Context, userID int64) error {
	err := client.do(ctx, http.MethodDelete, "/api/users/"+strconv.FormatInt(userID, 10), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (client *Client) ResetUserTraffic(ctx context.Context, userID int64) error {
	return client.post(ctx, "/api/users/"+strconv.FormatInt(userID, 10)+"/actions/reset-traffic", nil)
}

func (client *Client) Subscription(ctx context.Context, userID int64) (Subscription, error) {
	var envelope struct {
		Response Subscription `json:"response"`
	}
	err := client.get(ctx, "/api/subscriptions/by-id/"+strconv.FormatInt(userID, 10), &envelope)
	if err == nil && !envelope.Response.Found {
		err = ErrNotFound
	}
	return envelope.Response, err
}

func (client *Client) Devices(ctx context.Context, userID int64) (Devices, error) {
	var envelope struct {
		Response Devices `json:"response"`
	}
	err := client.get(ctx, "/api/hwid/devices/"+strconv.FormatInt(userID, 10), &envelope)
	return envelope.Response, err
}

func (client *Client) DeleteDevice(ctx context.Context, userID int64, hwid string) error {
	return client.post(ctx, "/api/hwid/devices/delete", map[string]any{"userId": userID, "hwid": hwid})
}

func (client *Client) DeleteAllDevices(ctx context.Context, userID int64) error {
	return client.post(ctx, "/api/hwid/devices/delete-all", map[string]any{"userId": userID})
}

func (client *Client) RevokeSubscription(ctx context.Context, userID int64) error {
	return client.post(ctx, "/api/users/"+strconv.FormatInt(userID, 10)+"/actions/revoke", map[string]any{})
}

// Node is one Remnawave node as the panel reports it.
//
// Only the fields Omniflow renders are decoded, and every one of them is
// optional in the sense that a panel which omits it leaves a zero here rather
// than failing the decode. That is deliberate: this is the one Remnawave surface
// Omniflow reads that is not on the critical path of a purchase, and a panel
// version that shapes it differently must degrade to "no node data" rather than
// break the report.
type Node struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
	// CountryCode is what a panel labels the node's location with. It is a
	// label rather than an assertion about where traffic goes.
	CountryCode string `json:"countryCode"`
	IsConnected bool   `json:"isConnected"`
	IsDisabled  bool   `json:"isDisabled"`
	// TrafficUsedBytes and TrafficLimitBytes are the node's own counters. A zero
	// limit means the node has none, not that it is full.
	TrafficUsedBytes  int64 `json:"trafficUsedBytes"`
	TrafficLimitBytes int64 `json:"trafficLimitBytes"`
	UsersOnline       *int  `json:"usersOnline"`
}

// Nodes lists the panel's nodes.
//
// It returns ErrNotFound when the panel does not expose the route, which callers
// render as "this panel did not provide node data" rather than as zeros. The
// distinction matters: a node list of zero bytes and no node list at all look
// identical on a screen and mean opposite things.
//
// Remnawave owns nodes, traffic, and connections. Omniflow reads this and stores
// none of it — there is no table for a node in this repository, and adding one
// would be the boundary violation that decision 0004 exists to prevent.
func (client *Client) Nodes(ctx context.Context) ([]Node, error) {
	var envelope struct {
		Response []Node `json:"response"`
	}
	if err := client.get(ctx, "/api/nodes", &envelope); err != nil {
		return nil, err
	}
	return envelope.Response, nil
}

// Squad is one Remnawave squad as the plan editor lists it: an internal squad
// (which inbounds a user reaches) or an external one (how the subscription
// page and the client configuration are shaped). Members is the panel's own
// count, read for display and never stored.
type Squad struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	Members int    `json:"members"`
}

// squadWire is one squad as the 3.4 contract encodes it.
type squadWire struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
	Info struct {
		MembersCount int `json:"membersCount"`
	} `json:"info"`
}

func squadsFromWire(wire []squadWire) []Squad {
	squads := make([]Squad, 0, len(wire))
	for _, squad := range wire {
		squads = append(squads, Squad{UUID: squad.UUID, Name: squad.Name, Members: squad.Info.MembersCount})
	}
	return squads
}

// InternalSquads lists the panel's internal squads.
//
// Remnawave owns squads; Omniflow reads the list so an operator picks a squad
// by name instead of pasting an identifier, and so a purchase can refuse a
// squad the panel no longer has before any money moves. Nothing is created,
// renamed, or deleted here.
func (client *Client) InternalSquads(ctx context.Context) ([]Squad, error) {
	var envelope struct {
		Response struct {
			InternalSquads []squadWire `json:"internalSquads"`
		} `json:"response"`
	}
	if err := client.get(ctx, "/api/internal-squads", &envelope); err != nil {
		return nil, err
	}
	return squadsFromWire(envelope.Response.InternalSquads), nil
}

// ExternalSquads lists the panel's external squads, read for the same reason
// as InternalSquads.
func (client *Client) ExternalSquads(ctx context.Context) ([]Squad, error) {
	var envelope struct {
		Response struct {
			ExternalSquads []squadWire `json:"externalSquads"`
		} `json:"response"`
	}
	if err := client.get(ctx, "/api/external-squads", &envelope); err != nil {
		return nil, err
	}
	return squadsFromWire(envelope.Response.ExternalSquads), nil
}

// Ping reports whether the panel answers an authenticated request. It reads one
// user rather than an unauthenticated page, so an expired token is detected as
// unavailability instead of being mistaken for a healthy panel.
func (client *Client) Ping(ctx context.Context) error {
	_, _, err := client.ListUsers(ctx, 0, 1)
	return err
}

func (client *Client) get(ctx context.Context, path string, target any) error {
	return client.do(ctx, http.MethodGet, path, nil, target)
}

func (client *Client) post(ctx context.Context, path string, body any) error {
	return client.do(ctx, http.MethodPost, path, body, nil)
}

func (client *Client) do(ctx context.Context, method, path string, body any, target any) error {
	if client.observer == nil {
		return client.call(ctx, method, path, body, target)
	}
	started := time.Now()
	err := client.call(ctx, method, path, body, target)
	outcome, code := classifyCall(err)
	client.observer(OperationName(method, path), outcome, code, time.Since(started))
	return err
}

// classifyCall turns a call's error into the observer's outcome and code.
func classifyCall(err error) (outcome, code string) {
	var apiError *APIError
	switch {
	case err == nil:
		return "succeeded", ""
	case errors.Is(err, ErrNotFound):
		return "not_found", ""
	case errors.As(err, &apiError):
		return "failed", fmt.Sprintf("remnawave_http_%d", apiError.StatusCode)
	case errors.Is(err, context.DeadlineExceeded):
		return "failed", "timeout"
	default:
		return "failed", "remnawave_unavailable"
	}
}

// OperationName is the bounded label for a panel call: the method and the
// path with every identifier replaced by ":id". A segment is an identifier
// when it is numeric, a UUID or other long hex run, or follows a "by-…"
// segment (by-username, by-telegram-id, by-short-uuid), so neither a user
// number nor a username can become a metric label.
func OperationName(method, path string) string {
	if index := strings.IndexAny(path, "?#"); index >= 0 {
		path = path[:index]
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for index, segment := range segments {
		if index > 0 && strings.HasPrefix(segments[index-1], "by-") || isIdentifier(segment) {
			segments[index] = ":id"
		}
	}
	return method + " /" + strings.Join(segments, "/")
}

func isIdentifier(segment string) bool {
	if segment == "" {
		return false
	}
	digits, hex := true, true
	for _, r := range segment {
		if r < '0' || r > '9' {
			digits = false
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' || r == '-') {
			hex = false
		}
	}
	return digits || hex && len(segment) >= 8
}

func (client *Client) call(ctx context.Context, method, path string, body any, target any) error {
	pathURL, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("parse Remnawave path: %w", err)
	}
	endpoint := client.baseURL.JoinPath(pathURL.Path)
	endpoint.RawQuery = pathURL.RawQuery
	var requestBody io.Reader
	if body != nil {
		encoded, encodeErr := json.Marshal(body)
		if encodeErr != nil {
			return fmt.Errorf("encode Remnawave request: %w", encodeErr)
		}
		requestBody = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), requestBody)
	if err != nil {
		return fmt.Errorf("create Remnawave request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	client.options.authenticate(request, client.token)
	if client.baseURL.Scheme == "http" {
		// The panel drops the connection of any request that does not look like
		// it came through an HTTPS reverse proxy. A plain-HTTP base URL is a
		// direct, in-network connection to the backend, so the request says so
		// itself, with the values the contract exports for exactly this
		// (REMNAWAVE_BYPASS_HTTPS_RESTRCTIONS). Over HTTPS the proxy in front of
		// the panel sets them.
		request.Header.Set("X-Forwarded-Proto", "https")
		request.Header.Set("X-Forwarded-For", "127.0.0.1")
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	if err := client.limiter.wait(ctx); err != nil {
		return fmt.Errorf("call Remnawave API: %w", err)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("call Remnawave API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return ErrNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &APIError{StatusCode: response.StatusCode, Code: errorCode(response.Body)}
	}
	if target == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode Remnawave response: %w", err)
	}
	return nil
}

// maxErrorBytes bounds how much of a failure body is read for its code. The
// panel's error body is a few short fields; anything larger is not one.
const maxErrorBytes = 16 << 10

// errorCode extracts the panel's `errorCode` from a failure body, or returns
// empty when the body is not the panel's error shape. The rest of the body is
// drained, bounded, so the connection can be reused.
func errorCode(body io.Reader) string {
	defer func() { _, _ = io.Copy(io.Discard, io.LimitReader(body, maxResponseBytes)) }()
	var failure struct {
		ErrorCode string `json:"errorCode"`
	}
	if json.NewDecoder(io.LimitReader(body, maxErrorBytes)).Decode(&failure) != nil {
		return ""
	}
	// A panel code is a letter and three digits; anything else is not one and
	// is not carried into logs.
	if len(failure.ErrorCode) > 8 || strings.TrimFunc(failure.ErrorCode, func(r rune) bool {
		return (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}) != "" {
		return ""
	}
	return failure.ErrorCode
}
