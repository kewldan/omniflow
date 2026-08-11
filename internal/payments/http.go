package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const maxProviderBody = 1 << 20

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
		return fmt.Errorf("%w: HTTP %d", ErrProviderResponse, response.StatusCode)
	}
	if responseBody == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxProviderBody)).Decode(responseBody); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	return nil
}
