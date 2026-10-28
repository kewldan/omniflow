package aigateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/egress"
)

// The three adapters below cover the providers an installation is likely to
// have access to. They are deliberately thin: each one maps the gateway's
// Request onto that provider's completion endpoint and maps the answer back,
// and does nothing else.
//
// None of them redacts, retries, or enforces a budget. Those belong to the
// gateway, and an adapter that did any of them would be a second place where
// the rules live — which is how one provider ends up behaving differently from
// another for reasons nobody remembers.

// maxProviderBody bounds what is read from a provider. A model endpoint that
// streams unbounded output should fail rather than exhaust memory.
const maxProviderBody = 4 << 20

// ErrProviderRejected reports a non-2xx answer. The provider's own message is
// deliberately not propagated to the caller: it can echo the prompt, and the
// prompt contains customer content that has no business in an error surfaced to
// an operator's browser. The detail goes to the log.
var ErrProviderRejected = errors.New("ai provider rejected the request")

// providerTimeout bounds one completion request.
const providerTimeout = 60 * time.Second

// Option adjusts how an adapter reaches its provider.
type Option func(*adapterOptions)

type adapterOptions struct {
	policy egress.Policy
	client *http.Client
}

// WithPolicy sets which destinations the adapter may reach. The default is the
// strict policy — HTTPS to public addresses only — because the base URL is
// typed by an operator and the request carries the provider's API key: an
// address that pointed inside the deployment's network would turn the adapter
// into a way to reach it, and one that pointed at plain HTTP would put the key
// on the wire in clear. An installation that runs a model on its own network
// opts in with APP_AI_ALLOW_PRIVATE_NETWORK, which the caller translates into a
// relaxed policy here.
func WithPolicy(policy egress.Policy) Option {
	return func(options *adapterOptions) { options.policy = policy }
}

// WithHTTPClient replaces the client entirely. It exists for tests, which talk
// to a local fake provider; the policy is still applied to the base URL.
func WithHTTPClient(client *http.Client) Option {
	return func(options *adapterOptions) { options.client = client }
}

// resolveOptions validates the base URL against the policy and returns the
// client the adapter will use. Every connection that client makes, including
// one a redirect opens, is checked again after name resolution.
func resolveOptions(baseURL string, options []Option) (*http.Client, error) {
	resolved := adapterOptions{}
	for _, option := range options {
		option(&resolved)
	}
	if err := resolved.policy.CheckURL(baseURL); err != nil {
		return nil, err
	}
	if resolved.client != nil {
		return resolved.client, nil
	}
	return resolved.policy.Client(providerTimeout), nil
}

// OpenAICompatible speaks the /v1/chat/completions shape.
//
// It covers OpenAI itself and the many self-hosted and hosted services that
// implement the same endpoint, which is why the base URL is configurable — an
// operator running a local model should not need a different adapter.
type OpenAICompatible struct {
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewOpenAICompatible builds the adapter. `name` is what the owner approves and
// what a task configuration names, so a deployment can register two of these
// against different endpoints.
func NewOpenAICompatible(name, baseURL, apiKey string, options ...Option) (*OpenAICompatible, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("an OpenAI-compatible provider needs a name and an API key")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.openai.com/v1"
	}
	client, err := resolveOptions(baseURL, options)
	if err != nil {
		return nil, err
	}
	return &OpenAICompatible{
		name: name, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, client: client,
	}, nil
}

func (provider *OpenAICompatible) Name() string { return provider.name }

func (provider *OpenAICompatible) Complete(
	ctx context.Context, request Request,
) (Response, error) {
	messages := make([]map[string]string, 0, 2)
	if request.System != "" {
		messages = append(messages, map[string]string{"role": "system", "content": request.System})
	}
	messages = append(messages, map[string]string{"role": "user", "content": request.Prompt})

	var answer struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	err := provider.call(ctx, map[string]any{
		"model":       request.Model,
		"messages":    messages,
		"temperature": request.Temperature,
		"max_tokens":  request.MaxTokens,
	}, &answer)
	if err != nil {
		return Response{}, err
	}
	if len(answer.Choices) == 0 {
		return Response{}, fmt.Errorf("%w: no choices returned", ErrProviderRejected)
	}
	return Response{
		Text:         answer.Choices[0].Message.Content,
		InputTokens:  answer.Usage.PromptTokens,
		OutputTokens: answer.Usage.CompletionTokens,
	}, nil
}

func (provider *OpenAICompatible) call(ctx context.Context, body, target any) error {
	return postJSON(ctx, provider.client, provider.baseURL+"/chat/completions",
		http.Header{"Authorization": []string{"Bearer " + provider.apiKey}}, body, target)
}

// Anthropic speaks the /v1/messages shape.
//
// The system prompt is a top-level field rather than a message, which is the
// one structural difference from the OpenAI shape worth noting: the gateway
// keeps System and Prompt separate precisely so each adapter can place them
// where its provider expects.
type Anthropic struct {
	name    string
	baseURL string
	apiKey  string
	version string
	client  *http.Client
}

// NewAnthropic builds the adapter.
func NewAnthropic(name, baseURL, apiKey string, options ...Option) (*Anthropic, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("an Anthropic provider needs a name and an API key")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	client, err := resolveOptions(baseURL, options)
	if err != nil {
		return nil, err
	}
	return &Anthropic{
		name: name, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey,
		// Pinned rather than tracking latest: a provider that changes its
		// response shape under a running installation is a failure nobody
		// deployed.
		version: "2023-06-01",
		client:  client,
	}, nil
}

func (provider *Anthropic) Name() string { return provider.name }

func (provider *Anthropic) Complete(ctx context.Context, request Request) (Response, error) {
	body := map[string]any{
		"model": request.Model,
		"messages": []map[string]string{
			{"role": "user", "content": request.Prompt},
		},
		"max_tokens":  maxTokensOrDefault(request.MaxTokens),
		"temperature": request.Temperature,
	}
	if request.System != "" {
		body["system"] = request.System
	}

	var answer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	err := postJSON(ctx, provider.client, provider.baseURL+"/messages", http.Header{
		"x-api-key":         []string{provider.apiKey},
		"anthropic-version": []string{provider.version},
	}, body, &answer)
	if err != nil {
		return Response{}, err
	}
	text := make([]string, 0, len(answer.Content))
	for _, block := range answer.Content {
		if block.Text != "" {
			text = append(text, block.Text)
		}
	}
	if len(text) == 0 {
		return Response{}, fmt.Errorf("%w: no content returned", ErrProviderRejected)
	}
	return Response{
		Text:         strings.Join(text, "\n"),
		InputTokens:  answer.Usage.InputTokens,
		OutputTokens: answer.Usage.OutputTokens,
	}, nil
}

// Gemini speaks the generateContent shape.
//
// The API key travels in the `x-goog-api-key` header, which the endpoint accepts
// as an alternative to the `key` query parameter. A key in the URL ends up in
// every place a URL is written down — proxy and provider access logs, and the
// text of any transport error, which is how it once reached this application's
// own warn-level log.
type Gemini struct {
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewGemini builds the adapter.
func NewGemini(name, baseURL, apiKey string, options ...Option) (*Gemini, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("a Gemini provider needs a name and an API key")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	client, err := resolveOptions(baseURL, options)
	if err != nil {
		return nil, err
	}
	return &Gemini{
		name: name, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, client: client,
	}, nil
}

func (provider *Gemini) Name() string { return provider.name }

func (provider *Gemini) Complete(ctx context.Context, request Request) (Response, error) {
	body := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]string{{"text": request.Prompt}}},
		},
		"generationConfig": map[string]any{
			"temperature":     request.Temperature,
			"maxOutputTokens": maxTokensOrDefault(request.MaxTokens),
		},
	}
	if request.System != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]string{{"text": request.System}},
		}
	}

	var answer struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	endpoint := fmt.Sprintf("%s/models/%s:generateContent",
		provider.baseURL, url.PathEscape(request.Model))
	headers := http.Header{"x-goog-api-key": []string{provider.apiKey}}
	if err := postJSON(ctx, provider.client, endpoint, headers, body, &answer); err != nil {
		return Response{}, err
	}
	if len(answer.Candidates) == 0 || len(answer.Candidates[0].Content.Parts) == 0 {
		return Response{}, fmt.Errorf("%w: no candidates returned", ErrProviderRejected)
	}
	text := make([]string, 0, len(answer.Candidates[0].Content.Parts))
	for _, part := range answer.Candidates[0].Content.Parts {
		text = append(text, part.Text)
	}
	return Response{
		Text:         strings.Join(text, "\n"),
		InputTokens:  answer.UsageMetadata.PromptTokenCount,
		OutputTokens: answer.UsageMetadata.CandidatesTokenCount,
	}, nil
}

// postJSON is the one HTTP path all three share.
//
// It reads a bounded body and returns a sentinel on a non-2xx answer without
// the provider's message, because that message can echo the prompt and the
// prompt contains customer content.
func postJSON(
	ctx context.Context, client *http.Client, endpoint string,
	headers http.Header, body, target any,
) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	response, err := client.Do(request)
	if err != nil {
		// A *url.Error quotes the whole request URL. Keep the cause, which is
		// what errors.Is needs, and drop the address: the caller logs this.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("ai provider request failed: %w", urlErr.Err)
		}
		return err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d", ErrProviderRejected, response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, maxProviderBody)).Decode(target)
}

// maxTokensOrDefault supplies a ceiling for providers that require one.
//
// The default is small on purpose. A task that has not set a limit should
// produce a short answer rather than an expensive one, and the per-task
// configuration is where an operator raises it deliberately.
func maxTokensOrDefault(limit int) int {
	if limit > 0 {
		return limit
	}
	return 1024
}
