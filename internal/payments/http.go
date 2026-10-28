package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const maxProviderBody = 1 << 20

// StatusError is a provider answering with a non-success HTTP status. It is
// an ErrProviderResponse, so existing checks keep working, and it keeps the
// status so a caller can tell a refusal from an outage.
type StatusError struct {
	StatusCode int
}

func (err *StatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d", ErrProviderResponse, err.StatusCode)
}

func (err *StatusError) Unwrap() error { return ErrProviderResponse }

// Refused reports whether a provider error is a definite refusal: the request
// reached the provider and was turned down, so nothing moved. A timeout, a
// connection failure, a 5xx, a rate limit, or a conflict is not one — the
// request may or may not have taken effect, and only asking again with the
// same idempotency key can tell.
func Refused(err error) bool {
	if errors.Is(err, ErrUnsupported) {
		return true
	}
	var status *StatusError
	if !errors.As(err, &status) {
		return false
	}
	switch status.StatusCode {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests:
		return false
	default:
		return status.StatusCode >= 400 && status.StatusCode < 500
	}
}

// tracedClient is the HTTP client every payment adapter uses. Provider calls
// become child spans of the checkout or webhook that caused them, which is what
// makes a slow provider visible without adding logging to each adapter.
func tracedClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)}
}

func doJSON(ctx context.Context, client *http.Client, method, endpoint string, headers http.Header, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode provider request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("create provider request: %w", err)
	}
	request.Header = headers.Clone()
	request.Header.Set("Accept", "application/json")
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call provider: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxProviderBody))
		return &StatusError{StatusCode: response.StatusCode}
	}
	if responseBody == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxProviderBody)).Decode(responseBody); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	return nil
}
