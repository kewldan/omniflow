// Package mailer sends transactional e-mail. It holds no template and no
// customer data of its own: a caller hands it one finished message, and the
// only provider today is Resend, the service the owner's Bedolaga installation
// already sends its sign-in mail through.
package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// ErrNotConfigured reports that no mail transport is set up.
var ErrNotConfigured = errors.New("e-mail is not configured")

// Message is one e-mail.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers a message.
type Sender interface {
	Send(ctx context.Context, message Message) error
}

// Resend sends through the Resend HTTP API.
type Resend struct {
	apiKey  string
	from    string
	baseURL string
	http    *http.Client
}

// NewResend builds the transport. Both values are required: an API key with
// no verified sender address is refused by Resend on every send.
func NewResend(apiKey, from string) (*Resend, error) {
	apiKey, from = strings.TrimSpace(apiKey), strings.TrimSpace(from)
	if apiKey == "" && from == "" {
		return nil, ErrNotConfigured
	}
	if apiKey == "" || from == "" || !strings.Contains(from, "@") {
		return nil, errors.New("APP_RESEND_API_KEY and APP_EMAIL_FROM must both be set")
	}
	return &Resend{
		apiKey: apiKey, from: from, baseURL: "https://api.resend.com",
		http: &http.Client{Timeout: 15 * time.Second, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}, nil
}

// Send posts one message. The recipient address is never logged or included
// in the returned error.
func (resend *Resend) Send(ctx context.Context, message Message) error {
	payload := map[string]any{
		"from": resend.from, "to": []string{message.To}, "subject": message.Subject, "text": message.Text,
	}
	if message.HTML != "" {
		payload["html"] = message.HTML
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, resend.baseURL+"/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+resend.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := resend.http.Do(request)
	if err != nil {
		return fmt.Errorf("call Resend: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("resend answered HTTP %d", response.StatusCode)
	}
	return nil
}
