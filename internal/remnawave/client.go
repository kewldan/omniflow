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
}

type Importer interface {
	ListUsers(context.Context, int, int) ([]User, int, error)
}

// ProvisionUser is the desired state pushed to Remnawave for one user.
//
// The two limits are always on the wire. Remnawave treats `trafficLimitBytes:
// 0` as unlimited and `hwidDeviceLimit: null` as unlimited, and an absent
// field as "leave it as it is" — which is how a plan with no limit used to
// keep the previous plan's limit after a renewal. A nil limit here therefore
// encodes as the explicit unlimited value rather than being omitted.
type ProvisionUser struct {
	Username          string
	ExpireAt          time.Time
	TrafficLimitBytes *int64
	HWIDDeviceLimit   *int
	InternalSquadIDs  []string
}

// provisionWire is the request body Remnawave receives. The limits carry no
// omitempty on purpose: see ProvisionUser.
type provisionWire struct {
	ID                *int64    `json:"id,omitempty"`
	Username          string    `json:"username"`
	ExpireAt          time.Time `json:"expireAt"`
	TrafficLimitBytes int64     `json:"trafficLimitBytes"`
	HWIDDeviceLimit   *int      `json:"hwidDeviceLimit"`
	InternalSquadIDs  []string  `json:"activeInternalSquads,omitempty"`
}

func (desired ProvisionUser) wire(userID *int64) provisionWire {
	encoded := provisionWire{ID: userID, Username: desired.Username, ExpireAt: desired.ExpireAt, HWIDDeviceLimit: desired.HWIDDeviceLimit, InternalSquadIDs: desired.InternalSquadIDs}
	if desired.TrafficLimitBytes != nil {
		encoded.TrafficLimitBytes = *desired.TrafficLimitBytes
	}
	return encoded
}

type InternalSquad struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

type User struct {
	ID                   int64           `json:"id"`
	Username             string          `json:"username"`
	Status               string          `json:"status"`
	TrafficLimitBytes    int64           `json:"trafficLimitBytes"`
	ExpireAt             time.Time       `json:"expireAt"`
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

type APIError struct {
	StatusCode int
}

func (err *APIError) Error() string {
	return fmt.Sprintf("remnawave API returned HTTP %d", err.StatusCode)
}

type Client struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

func NewClient(rawURL, token string) (*Client, error) {
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
		// Every panel call is traced as a child of the request or job that
		// caused it, which is what makes a slow Remnawave visible end to end.
		http: &http.Client{
			Timeout:   10 * time.Second,
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
	err := client.do(ctx, http.MethodPost, "/api/users/", desired.wire(nil), &envelope)
	return envelope.Response, err
}

func (client *Client) UpdateUser(ctx context.Context, userID int64, desired ProvisionUser) (User, error) {
	var envelope struct {
		Response User `json:"response"`
	}
	err := client.do(ctx, http.MethodPatch, "/api/users/", desired.wire(&userID), &envelope)
	return envelope.Response, err
}

func (client *Client) EnableUser(ctx context.Context, userID int64) error {
	return client.post(ctx, "/api/users/"+strconv.FormatInt(userID, 10)+"/actions/enable", nil)
}

func (client *Client) DisableUser(ctx context.Context, userID int64) error {
	return client.post(ctx, "/api/users/"+strconv.FormatInt(userID, 10)+"/actions/disable", nil)
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
	request.Header.Set("Authorization", "Bearer "+client.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("call Remnawave API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return &APIError{StatusCode: response.StatusCode}
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
