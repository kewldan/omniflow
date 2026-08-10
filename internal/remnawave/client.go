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

type User struct {
	ID                int64       `json:"id"`
	Username          string      `json:"username"`
	Status            string      `json:"status"`
	TrafficLimitBytes int64       `json:"trafficLimitBytes"`
	ExpireAt          time.Time   `json:"expireAt"`
	HWIDDeviceLimit   *int        `json:"hwidDeviceLimit"`
	SubscriptionURL   string      `json:"subscriptionUrl"`
	Traffic           UserTraffic `json:"userTraffic"`
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
		http: &http.Client{
			Timeout: 10 * time.Second,
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
