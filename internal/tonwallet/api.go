package tonwallet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// API providers the operator may use. Both need a key; the fork's free-tier
// experience is why the rate limit below defaults to one request a second.
const (
	ProviderToncenter = "toncenter"
	ProviderTonapi    = "tonapi"
)

const (
	toncenterBaseURL = "https://toncenter.com/api/v2"
	tonapiBaseURL    = "https://tonapi.io/v2"
	maxAPIBody       = 1 << 20
)

// ErrUnsupportedProvider reports an API provider name this package does not
// speak.
var ErrUnsupportedProvider = errors.New("TON API provider is not supported")

// StatusError is an HTTP answer from the TON API that was not a success.
//
// The body is not kept: an error string travels into logs and delivery
// records, and a provider echo is nothing either should hold.
type StatusError struct{ Status int }

func (err StatusError) Error() string {
	return "TON API responded with HTTP " + strconv.Itoa(err.Status)
}

// RateLimited reports an answer that means "not processed, slow down". It is
// the only answer after which a signed message may be sent again: anything
// else may mean the provider forwarded it.
func RateLimited(err error) bool {
	var status StatusError
	return errors.As(err, &status) && status.Status == http.StatusTooManyRequests
}

// Account is what the network says about the wallet right now.
type Account struct {
	BalanceNano int64
	// Deployed is false for a wallet that has received funds but never sent,
	// so its contract code is not on chain yet.
	Deployed bool
	Seqno    uint32
}

// API is the subset of a TON HTTP API the wallet needs.
type API interface {
	Account(ctx context.Context, address string) (Account, error)
	// SendBOC submits a signed external message.
	SendBOC(ctx context.Context, boc string) error
}

// APIOptions configures a TON HTTP API client.
type APIOptions struct {
	Provider string
	// Key is the provider API key. It is sent as a header and never appears
	// in an error, a log line, or a URL.
	Key string
	// RPS bounds requests per second across every client in this process
	// that shares the same provider and key. Zero disables the limit.
	RPS float64
	// BaseURL overrides the provider's address, for tests.
	BaseURL string
	HTTP    *http.Client
}

// NewAPI builds a client for one provider.
func NewAPI(options APIOptions) (API, error) {
	client := options.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	limiter := sharedLimiter(options.Provider+"\x00"+options.Key, options.RPS)
	switch strings.ToLower(strings.TrimSpace(options.Provider)) {
	case "", ProviderToncenter:
		base := options.BaseURL
		if base == "" {
			base = toncenterBaseURL
		}
		return &toncenter{base: strings.TrimSuffix(base, "/"), key: options.Key, http: client, limiter: limiter}, nil
	case ProviderTonapi:
		base := options.BaseURL
		if base == "" {
			base = tonapiBaseURL
		}
		return &tonapi{base: strings.TrimSuffix(base, "/"), key: options.Key, http: client, limiter: limiter}, nil
	default:
		return nil, ErrUnsupportedProvider
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

// limiter spaces requests so a provider's per-key limit is not exceeded.
//
// It is process-wide on purpose. The fork learned that an operator opening the
// wallet card while a purchase was confirming would trip the free tier's
// one-request-a-second limit and push the purchase into manual review; one
// shared queue per key is what prevents that. Each slot is spaced by 1.1/rps,
// the fork's own margin over the nominal rate.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

var (
	limitersMu sync.Mutex
	limiters   = map[string]*limiter{}
)

func sharedLimiter(key string, rps float64) *limiter {
	if rps <= 0 {
		return nil
	}
	interval := time.Duration(float64(time.Second) * 1.1 / rps)
	limitersMu.Lock()
	defer limitersMu.Unlock()
	existing, ok := limiters[key]
	if !ok {
		existing = &limiter{}
		limiters[key] = existing
	}
	existing.mu.Lock()
	existing.interval = interval
	existing.mu.Unlock()
	return existing
}

// wait blocks until this caller's slot, or the context ends.
func (limit *limiter) wait(ctx context.Context) error {
	if limit == nil {
		return nil
	}
	limit.mu.Lock()
	now := time.Now()
	slot := limit.next
	if slot.Before(now) {
		slot = now
	}
	limit.next = slot.Add(limit.interval)
	limit.mu.Unlock()

	delay := time.Until(slot)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ---------------------------------------------------------------------------
// toncenter v2
// ---------------------------------------------------------------------------

type toncenter struct {
	base    string
	key     string
	http    *http.Client
	limiter *limiter
}

func (client *toncenter) Account(ctx context.Context, address string) (Account, error) {
	var response struct {
		OK     bool `json:"ok"`
		Result struct {
			Balance      json.Number `json:"balance"`
			AccountState string      `json:"account_state"`
			Seqno        *int64      `json:"seqno"`
		} `json:"result"`
	}
	if err := client.do(ctx, http.MethodGet,
		"/getWalletInformation?address="+url.QueryEscape(address), nil, &response); err != nil {
		return Account{}, err
	}
	if !response.OK {
		return Account{}, errors.New("toncenter refused the wallet lookup")
	}
	balance, err := parseNano(response.Result.Balance.String())
	if err != nil {
		return Account{}, err
	}
	account := Account{BalanceNano: balance, Deployed: response.Result.AccountState == "active"}
	if response.Result.Seqno != nil && *response.Result.Seqno >= 0 {
		account.Seqno = uint32(*response.Result.Seqno)
	}
	return account, nil
}

func (client *toncenter) SendBOC(ctx context.Context, boc string) error {
	var response struct {
		OK bool `json:"ok"`
	}
	if err := client.do(ctx, http.MethodPost, "/sendBoc", map[string]string{"boc": boc}, &response); err != nil {
		return err
	}
	if !response.OK {
		// A 200 that says "not ok" is still an answer from a provider that may
		// have forwarded the message. The caller treats it like any other
		// broadcast failure: unknown, never retried.
		return errors.New("toncenter did not accept the message")
	}
	return nil
}

func (client *toncenter) do(ctx context.Context, method, path string, payload, out any) error {
	return doJSON(ctx, client.http, client.limiter, method, client.base+path, payload, out,
		func(request *http.Request) { request.Header.Set("X-API-Key", client.key) })
}

// ---------------------------------------------------------------------------
// tonapi v2
// ---------------------------------------------------------------------------

type tonapi struct {
	base    string
	key     string
	http    *http.Client
	limiter *limiter
}

func (client *tonapi) Account(ctx context.Context, address string) (Account, error) {
	var account struct {
		Balance json.Number `json:"balance"`
		Status  string      `json:"status"`
	}
	if err := client.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(address), nil, &account); err != nil {
		return Account{}, err
	}
	balance, err := parseNano(account.Balance.String())
	if err != nil {
		return Account{}, err
	}
	result := Account{BalanceNano: balance, Deployed: account.Status == "active"}
	if !result.Deployed {
		// An undeployed wallet has no seqno method to call; zero is what the
		// first message is signed for.
		return result, nil
	}
	var seqno struct {
		Seqno int64 `json:"seqno"`
	}
	if err := client.do(ctx, http.MethodGet, "/wallet/"+url.PathEscape(address)+"/seqno", nil, &seqno); err != nil {
		return Account{}, err
	}
	if seqno.Seqno >= 0 {
		result.Seqno = uint32(seqno.Seqno)
	}
	return result, nil
}

func (client *tonapi) SendBOC(ctx context.Context, boc string) error {
	return client.do(ctx, http.MethodPost, "/blockchain/message", map[string]string{"boc": boc}, nil)
}

func (client *tonapi) do(ctx context.Context, method, path string, payload, out any) error {
	return doJSON(ctx, client.http, client.limiter, method, client.base+path, payload, out,
		func(request *http.Request) { request.Header.Set("Authorization", "Bearer "+client.key) })
}

// ---------------------------------------------------------------------------
// Shared transport
// ---------------------------------------------------------------------------

func doJSON(
	ctx context.Context, httpClient *http.Client, limit *limiter,
	method, target string, payload, out any, authorize func(*http.Request),
) error {
	if err := limit.wait(ctx); err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	authorize(request)

	response, err := httpClient.Do(request)
	if err != nil {
		// The URL is dropped from the error: it is harmless today, but a
		// provider that ever took the key as a query parameter would put it
		// into every log line this error reaches.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("TON API request failed: %w", urlErr.Err)
		}
		return fmt.Errorf("TON API request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxAPIBody))
		return StatusError{Status: response.StatusCode}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxAPIBody))
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxAPIBody))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode TON API response: %w", err)
	}
	return nil
}

// parseNano reads a nanoton amount that a provider may send as a string or a
// number, and refuses anything that does not fit an int64.
func parseNano(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, ok := new(big.Int).SetString(value, 10)
	if !ok || parsed.Sign() < 0 || !parsed.IsInt64() {
		return 0, fmt.Errorf("TON API returned an unusable balance")
	}
	return parsed.Int64(), nil
}
