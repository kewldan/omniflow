// Package nalogo talks to «Мой налог» (lknpd.nalog.ru), the service a
// self-employed person (НПД) registers income in, so each payment gets the
// receipt the law requires.
//
// The API is the one the web cabinet itself uses; the Federal Tax Service
// publishes no versioned contract for it. The shapes here follow the PHP
// library both Bedolaga's app/lib/nalogo and every other integration are
// ported from. It is isolated in this package so a change on the tax
// service's side is one file to fix.
package nalogo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the production API.
const DefaultBaseURL = "https://lknpd.nalog.ru/api"

// Outcomes a caller must tell apart.
var (
	// ErrUnauthorized means the token or the credentials were refused. No
	// receipt can have been created.
	ErrUnauthorized = errors.New("nalogo: unauthorized")
	// ErrRejected means the request was refused as invalid; repeating it
	// gets the same answer.
	ErrRejected = errors.New("nalogo: request rejected")
	// ErrUncertain means the outcome is unknown — a timeout or a server
	// error — and a receipt may exist. The caller must look before retrying.
	ErrUncertain = errors.New("nalogo: outcome unknown")
)

// moscow is the offset the API expects operation times in.
var moscow = time.FixedZone("MSK", 3*60*60)

// Token is an access token and the refresh token that renews it.
type Token struct {
	Access    string    `json:"token"`
	Refresh   string    `json:"refreshToken"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Valid reports whether the access token is usable for another minute.
func (token Token) Valid(now time.Time) bool {
	return token.Access != "" && now.Add(time.Minute).Before(token.ExpiresAt)
}

// Client calls the API.
type Client struct {
	base     string
	deviceID string
	http     *http.Client
}

// New builds a client. deviceID identifies this installation to the tax
// service and should stay the same across restarts; an empty one is minted.
func New(baseURL, deviceID string, client *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if deviceID == "" {
		deviceID = NewDeviceID()
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), deviceID: deviceID, http: client}
}

// NewDeviceID mints the 21-character identifier the web cabinet uses.
func NewDeviceID() string {
	raw := make([]byte, 11)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)[:21]
}

func (client *Client) deviceInfo() map[string]any {
	return map[string]any{
		"sourceType": "WEB", "sourceDeviceId": client.deviceID, "appVersion": "1.0.0",
		"metaDetails": map[string]string{"userAgent": "Mozilla/5.0 (compatible; Omniflow)"},
	}
}

// Authenticate exchanges the taxpayer number and password for a token.
func (client *Client) Authenticate(ctx context.Context, inn, password string) (Token, error) {
	return client.token(ctx, "/v1/auth/lkfl", map[string]any{
		"username": inn, "password": password, "deviceInfo": client.deviceInfo(),
	})
}

// Refresh renews a token.
func (client *Client) Refresh(ctx context.Context, refresh string) (Token, error) {
	return client.token(ctx, "/v1/auth/token", map[string]any{
		"refreshToken": refresh, "deviceInfo": client.deviceInfo(),
	})
}

func (client *Client) token(ctx context.Context, path string, body map[string]any) (Token, error) {
	var answer struct {
		Token          string `json:"token"`
		RefreshToken   string `json:"refreshToken"`
		TokenExpireIn  string `json:"tokenExpireIn"`
		TokenExpiresIn string `json:"tokenExpiresIn"`
	}
	if err := client.do(ctx, http.MethodPost, path, "", body, &answer); err != nil {
		if errors.Is(err, ErrRejected) {
			return Token{}, ErrUnauthorized
		}
		return Token{}, err
	}
	if answer.Token == "" {
		return Token{}, ErrUnauthorized
	}
	expires := time.Now().Add(time.Hour)
	for _, candidate := range []string{answer.TokenExpireIn, answer.TokenExpiresIn} {
		if parsed, err := time.Parse(time.RFC3339, candidate); err == nil {
			expires = parsed
			break
		}
	}
	return Token{Access: answer.Token, Refresh: answer.RefreshToken, ExpiresAt: expires}, nil
}

// Income is one receipt to register.
type Income struct {
	Name          string
	AmountMinor   int64
	OperationTime time.Time
}

// rubles renders kopecks as the decimal string the API expects.
func rubles(minor int64) string {
	return strconv.FormatInt(minor/100, 10) + "." + fmt.Sprintf("%02d", minor%100)
}

// CreateIncome registers the income and returns the receipt's identifier.
func (client *Client) CreateIncome(ctx context.Context, token string, income Income) (string, error) {
	now := time.Now().In(moscow)
	amount := rubles(income.AmountMinor)
	body := map[string]any{
		"operationTime": income.OperationTime.In(moscow).Format(time.RFC3339),
		"requestTime":   now.Format(time.RFC3339),
		"services":      []map[string]string{{"name": income.Name, "amount": amount, "quantity": "1"}},
		"totalAmount":   amount,
		"client": map[string]any{
			"contactPhone": nil, "displayName": nil, "incomeType": "FROM_INDIVIDUAL", "inn": nil,
		},
		"paymentType":                     "CASH",
		"ignoreMaxTotalIncomeRestriction": false,
	}
	var answer struct {
		UUID string `json:"approvedReceiptUuid"`
	}
	if err := client.do(ctx, http.MethodPost, "/v1/income", token, body, &answer); err != nil {
		return "", err
	}
	if answer.UUID == "" {
		return "", ErrUncertain
	}
	return answer.UUID, nil
}

// CancelIncome annuls a receipt because the payment was refunded.
func (client *Client) CancelIncome(ctx context.Context, token, receiptUUID string) error {
	now := time.Now().In(moscow).Format(time.RFC3339)
	return client.do(ctx, http.MethodPost, "/v1/cancel", token, map[string]any{
		"operationTime": now, "requestTime": now, "comment": "Возврат средств",
		"receiptUuid": receiptUUID, "partnerCode": nil,
	}, nil)
}

// FindIncome looks for a receipt already registered for this amount within
// ten minutes of the operation time: the check before retrying an uncertain
// attempt, so a timeout does not produce a second receipt.
func (client *Client) FindIncome(ctx context.Context, token string, income Income) (string, bool, error) {
	day := income.OperationTime.In(moscow)
	query := url.Values{
		"from": {day.Format("2006-01-02")}, "to": {day.AddDate(0, 0, 1).Format("2006-01-02")},
		"limit": {"50"}, "offset": {"0"}, "sortBy": {"OPERATION_TIME"}, "sortOrder": {"DESC"},
	}
	var answer struct {
		Content []struct {
			UUID          string          `json:"approvedReceiptUuid"`
			ReceiptUUID   string          `json:"receiptUuid"`
			TotalAmount   json.RawMessage `json:"totalAmount"`
			OperationTime string          `json:"operationTime"`
		} `json:"content"`
	}
	if err := client.do(ctx, http.MethodGet, "/v1/incomes?"+query.Encode(), token, nil, &answer); err != nil {
		return "", false, err
	}
	for _, item := range answer.Content {
		amount, err := strconv.ParseFloat(strings.Trim(string(item.TotalAmount), `"`), 64)
		if err != nil || math.Abs(amount*100-float64(income.AmountMinor)) >= 1 {
			continue
		}
		at, err := time.Parse(time.RFC3339, item.OperationTime)
		if err != nil || at.Sub(income.OperationTime).Abs() > 10*time.Minute {
			continue
		}
		if item.UUID != "" {
			return item.UUID, true, nil
		}
		if item.ReceiptUUID != "" {
			return item.ReceiptUUID, true, nil
		}
	}
	return "", false, nil
}

// PrintURL is the public link to a receipt.
func PrintURL(baseURL, inn, receiptUUID string) string {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return strings.TrimRight(baseURL, "/") + "/v1/receipt/" + url.PathEscape(inn) + "/" + url.PathEscape(receiptUUID) + "/print"
}

func (client *Client) do(ctx context.Context, method, path, token string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.base+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	request.Header.Set("Referrer", "https://lknpd.nalog.ru/auth/login")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	switch {
	case response.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case response.StatusCode >= 500:
		return fmt.Errorf("%w: HTTP %d", ErrUncertain, response.StatusCode)
	case response.StatusCode >= 400:
		return fmt.Errorf("%w: HTTP %d", ErrRejected, response.StatusCode)
	}
	if out == nil || len(payload) == 0 {
		return nil
	}
	if err = json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("%w: unreadable answer", ErrUncertain)
	}
	return nil
}
