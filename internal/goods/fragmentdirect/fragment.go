package fragmentdirect

// The Fragment protocol in this file is ported from fragment-api-py 12.1.0 by
// S1qwy (MIT), by way of the trimmed copy the Bedolaga fork vendors under
// app/external/fragment/_vendor. Version 13 and later are deliberately not the
// reference: they divert 0.5% of every purchase to the library author and
// authenticate through a shared wallet whose seed phrase is public.
//
// Copyright (c) S1qwy. Permission is hereby granted, free of charge, to any
// person obtaining a copy of this software and associated documentation files
// (the "Software"), to deal in the Software without restriction, including
// without limitation the rights to use, copy, modify, merge, publish,
// distribute, sublicense, and/or sell copies of the Software, and to permit
// persons to whom the Software is furnished to do so, subject to the following
// conditions: The above copyright notice and this permission notice shall be
// included in all copies or substantial portions of the Software. THE SOFTWARE
// IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	fragmentBaseURL = "https://fragment.com"
	starsPage       = fragmentBaseURL + "/stars"
	// confirmReferer is the page confirmReq is sent from, as the fork does.
	confirmReferer = fragmentBaseURL + "/stars/buy"
	hashTTL        = 120 * time.Second
	maxBody        = 1 << 20
	userAgent      = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36"
	// deviceFingerprint is the TON Connect device Fragment is told it is
	// talking to, byte for byte the fork's DEVICE_FINGERPRINT.
	deviceFingerprint = `{"platform":"android","appName":"Tonkeeper","appVersion":"26.07.1",` +
		`"maxProtocolVersion":2,"features":["SendTransaction",{"name":"SignData",` +
		`"types":["text","binary","cell"]},{"name":"SendTransaction","maxMessages":255}]}`
)

// requiredCookies are the session cookies a KYC-verified Fragment account with
// a linked TON wallet carries. All four are needed to buy.
var requiredCookies = []string{"stel_ssid", "stel_dt", "stel_token", "stel_ton_token"}

var (
	hashPattern     = regexp.MustCompile(`(?:https://fragment\.com)?/api\?hash=([a-f0-9]+)`)
	tonPricePattern = regexp.MustCompile(`icon-ton[^>]*>([^<]*(?:<span[^>]*>[^<]*</span>)?)`)
	// tagPattern is the fork's linear `<[^<>]+>`, not the original's
	// quadratic `<[^>]+>`.
	tagPattern = regexp.MustCompile(`<[^<>]+>`)
)

// Errors, split by whether money can have left the operator's wallet.
var (
	// ErrConfiguration is a cookie, KYC, wallet, or key problem. Nothing was
	// paid; an operator fixes it and a retry can succeed.
	ErrConfiguration = errors.New("fragment is not configured correctly")
	// ErrRecipientNotFound is Fragment saying the username does not resolve.
	ErrRecipientNotFound = errors.New("fragment found no such recipient")
	// ErrBeforePayment is any other failure before the payment was sent.
	// Retrying it is safe.
	ErrBeforePayment = errors.New("fragment purchase failed before payment")
)

// ParseCookies accepts the session cookies as `k=v; k2=v2` (what a browser's
// cookie header looks like) or as a JSON object, and refuses a set missing any
// of the four Fragment needs. A refusal names the missing keys, never a value.
func ParseCookies(raw string) (map[string]string, error) {
	text := strings.TrimSpace(raw)
	cookies := map[string]string{}
	if strings.HasPrefix(text, "{") {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return nil, fmt.Errorf("%w: cookies are not valid JSON", ErrConfiguration)
		}
		for key, value := range decoded {
			cookies[key] = fmt.Sprint(value)
		}
	} else {
		for _, item := range strings.Split(text, ";") {
			key, value, found := strings.Cut(item, "=")
			if found {
				cookies[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}
		}
	}
	var missing []string
	for _, key := range requiredCookies {
		if strings.TrimSpace(cookies[key]) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: cookies are missing %s", ErrConfiguration, strings.Join(missing, ", "))
	}
	return cookies, nil
}

// fragmentClient speaks Fragment's page-and-form API.
type fragmentClient struct {
	cookieHeader string
	baseURL      string
	http         *http.Client
	retryDelay   func(attempt int) time.Duration

	mu       sync.Mutex
	hash     string
	hashedAt time.Time
}

func newFragmentClient(cookies map[string]string, httpClient *http.Client, baseURL string) *fragmentClient {
	keys := make([]string, 0, len(cookies))
	for key := range cookies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+cookies[key])
	}
	if baseURL == "" {
		baseURL = fragmentBaseURL
	}
	return &fragmentClient{
		cookieHeader: strings.Join(parts, "; "),
		baseURL:      strings.TrimSuffix(baseURL, "/"),
		http:         httpClient,
		retryDelay: func(attempt int) time.Duration {
			// The fork's with_retry: one second, doubling, with up to 30%
			// jitter, capped at thirty.
			delay := time.Second << (attempt - 1)
			jitter := time.Duration(rand.Int64N(int64(delay)*3/10 + 1))
			return min(delay+jitter, 30*time.Second)
		},
	}
}

// statusError carries a Fragment HTTP status without its body, which can echo
// the session.
type statusError struct{ status int }

func (err statusError) Error() string { return fmt.Sprintf("fragment answered HTTP %d", err.status) }

// retryable mirrors the fork's _should_retry_exception: rate limits, server
// errors, and transport failures are tried again; anything Fragment actually
// said is not.
func retryable(err error) bool {
	var status statusError
	if errors.As(err, &status) {
		switch status.status {
		case 429, 500, 502, 503, 504:
			return true
		}
		return false
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// withRetry runs an operation up to three times, as the fork's decorator does.
// It is only ever used for calls made before any payment.
func (client *fragmentClient) withRetry(ctx context.Context, operation func() error) error {
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if err = operation(); err == nil || !retryable(err) || attempt == 3 {
			return err
		}
		timer := time.NewTimer(client.retryDelay(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

// apiHash reads the per-session API hash from Fragment's home page and caches
// it for two minutes, as the fork does.
func (client *fragmentClient) apiHash(ctx context.Context) (string, error) {
	client.mu.Lock()
	if client.hash != "" && time.Since(client.hashedAt) < hashTTL {
		hash := client.hash
		client.mu.Unlock()
		return hash, nil
	}
	client.mu.Unlock()

	var hash string
	err := client.withRetry(ctx, func() error {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		request.Header.Set("Accept-Language", "en-US,en;q=0.9")
		request.Header.Set("User-Agent", userAgent)
		request.Header.Set("Cookie", client.cookieHeader)
		response, err := client.http.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 4*maxBody))
		if err != nil {
			return err
		}
		if response.StatusCode != http.StatusOK {
			return statusError{status: response.StatusCode}
		}
		match := hashPattern.FindSubmatch(body)
		if match == nil {
			return fmt.Errorf("%w: fragment API hash not found on the home page", ErrConfiguration)
		}
		hash = string(match[1])
		return nil
	})
	if err != nil {
		return "", err
	}
	client.mu.Lock()
	client.hash, client.hashedAt = hash, time.Now()
	client.mu.Unlock()
	return hash, nil
}

// call posts one API method, as the page's own XHR would, and decodes the
// JSON answer.
func (client *fragmentClient) call(
	ctx context.Context, referer string, form url.Values, out any,
) error {
	hash, err := client.apiHash(ctx)
	if err != nil {
		return err
	}
	return client.withRetry(ctx, func() error {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			client.baseURL+"/api?hash="+url.QueryEscape(hash), strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
		request.Header.Set("Accept-Language", "en-US,en;q=0.9")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
		request.Header.Set("Origin", fragmentBaseURL)
		request.Header.Set("Priority", "u=1, i")
		request.Header.Set("Sec-Fetch-Dest", "empty")
		request.Header.Set("Sec-Fetch-Mode", "cors")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		request.Header.Set("User-Agent", userAgent)
		request.Header.Set("X-Requested-With", "XMLHttpRequest")
		request.Header.Set("Referer", referer)
		request.Header.Set("X-Aj-Referer", referer)
		request.Header.Set("Cookie", client.cookieHeader)
		response, err := client.http.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxBody))
			return statusError{status: response.StatusCode}
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, maxBody)).Decode(out); err != nil {
			return fmt.Errorf("decode fragment %s: %w", form.Get("method"), err)
		}
		return nil
	})
}

// recipient is what searchStarsRecipient resolves a username to.
type recipient struct {
	ID   string
	Name string
}

// searchRecipient asks Fragment whether a username can receive Stars.
func (client *fragmentClient) searchRecipient(ctx context.Context, username string) (recipient, error) {
	var response struct {
		Found *struct {
			Recipient string `json:"recipient"`
			Name      string `json:"name"`
		} `json:"found"`
		Error string `json:"error"`
	}
	err := client.call(ctx, starsPage, url.Values{
		"method": {"searchStarsRecipient"}, "query": {username}, "quantity": {""},
	}, &response)
	if err != nil {
		return recipient{}, fmt.Errorf("%w: %w", ErrBeforePayment, err)
	}
	if response.Found != nil && response.Found.Recipient != "" {
		return recipient{ID: response.Found.Recipient, Name: response.Found.Name}, nil
	}
	// The fork reads any answer without a recipient as "not found". An
	// answer that carries some other error is not about the username — an
	// expired session reads like this — so it is reported as a retryable
	// failure rather than refunding the customer for a username that may be
	// fine.
	if response.Error != "" && !notFoundMessage(response.Error) {
		return recipient{}, fmt.Errorf("%w: fragment refused the recipient search", ErrBeforePayment)
	}
	return recipient{}, ErrRecipientNotFound
}

func notFoundMessage(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "not found") || strings.Contains(lower, "no telegram user") ||
		strings.Contains(lower, "no users")
}

// starsPriceTON is Fragment's current price for a quantity of Stars, as the
// decimal TON string its page shows.
func (client *fragmentClient) starsPriceTON(ctx context.Context, quantity int) (string, error) {
	var response struct {
		CurPrice string `json:"cur_price"`
	}
	err := client.call(ctx, starsPage, url.Values{
		"stars": {"0"}, "quantity": {fmt.Sprint(quantity)}, "method": {"updateStarsPrices"},
	}, &response)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrBeforePayment, err)
	}
	return parseTONPrice(response.CurPrice)
}

// parseTONPrice extracts the TON figure from Fragment's price fragment.
func parseTONPrice(fragment string) (string, error) {
	match := tonPricePattern.FindStringSubmatch(fragment)
	if match == nil {
		return "", fmt.Errorf("%w: fragment price is not readable", ErrBeforePayment)
	}
	value := tagPattern.ReplaceAllString(match[1], "")
	value = strings.TrimSpace(strings.NewReplacer(",", "", " ", "").Replace(value))
	if value == "" {
		return "", fmt.Errorf("%w: fragment price is empty", ErrBeforePayment)
	}
	return value, nil
}

// paymentMessage is one message of the TON Connect payment Fragment prepares.
type paymentMessage struct {
	Address string      `json:"address"`
	Amount  json.Number `json:"amount"`
	Payload string      `json:"payload"`
}

// payment is a prepared Fragment purchase: the request and what to pay for it.
type payment struct {
	RequestID string
	Messages  []paymentMessage
}

// preparePayment runs initBuyStarsRequest and getBuyStarsLink. Nothing in it
// spends anything.
func (client *fragmentClient) preparePayment(
	ctx context.Context, recipientID string, quantity int, account any, showSender bool,
) (payment, error) {
	var initialized struct {
		ReqID json.RawMessage `json:"req_id"`
		Error string          `json:"error"`
	}
	if err := client.call(ctx, starsPage, url.Values{
		"method": {"initBuyStarsRequest"}, "recipient": {recipientID},
		"quantity": {fmt.Sprint(quantity)}, "payment_method": {"ton"},
	}, &initialized); err != nil {
		return payment{}, fmt.Errorf("%w: %w", ErrBeforePayment, err)
	}
	if initialized.Error != "" {
		return payment{}, fmt.Errorf("%w: fragment rejected the request", ErrBeforePayment)
	}
	requestID := strings.Trim(string(initialized.ReqID), `"`)
	if requestID == "" || requestID == "null" {
		return payment{}, fmt.Errorf("%w: fragment returned no request id", ErrBeforePayment)
	}

	encodedAccount, err := json.Marshal(account)
	if err != nil {
		return payment{}, fmt.Errorf("%w: %w", ErrBeforePayment, err)
	}
	show := "0"
	if showSender {
		show = "1"
	}
	var link struct {
		NeedVerify  bool   `json:"need_verify"`
		Error       string `json:"error"`
		EVM         any    `json:"evm"`
		Transaction *struct {
			Messages []paymentMessage `json:"messages"`
		} `json:"transaction"`
	}
	if err := client.call(ctx, starsPage, url.Values{
		"method": {"getBuyStarsLink"}, "account": {string(encodedAccount)},
		"device": {deviceFingerprint}, "transaction": {"1"}, "id": {requestID},
		"show_sender": {show},
	}, &link); err != nil {
		return payment{}, fmt.Errorf("%w: %w", ErrBeforePayment, err)
	}
	switch {
	case link.NeedVerify:
		return payment{}, fmt.Errorf("%w: fragment requires KYC for this account", ErrConfiguration)
	case link.Error != "":
		return payment{}, fmt.Errorf("%w: fragment refused the payment link", ErrBeforePayment)
	case link.Transaction == nil || len(link.Transaction.Messages) == 0 || truthy(link.EVM):
		return payment{}, fmt.Errorf("%w: fragment returned an unexpected payment shape", ErrBeforePayment)
	}
	return payment{RequestID: requestID, Messages: link.Transaction.Messages}, nil
}

// confirm tells Fragment which signed message paid for a request. It runs
// after the money left, so a failure here is only logged by the caller.
func (client *fragmentClient) confirm(ctx context.Context, requestID, boc string) error {
	var response map[string]any
	return client.call(ctx, confirmReferer, url.Values{
		"method": {"confirmReq"}, "id": {requestID}, "boc": {boc},
	}, &response)
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	default:
		return true
	}
}
