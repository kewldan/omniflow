package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capture records what an adapter actually put on the wire, so a test can check
// the mapping rather than only the return value.
type capture struct {
	path    string
	query   string
	headers http.Header
	body    map[string]any
}

func stub(t *testing.T, reply string, recorded *capture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			payload, _ := io.ReadAll(request.Body)
			recorded.path = request.URL.Path
			recorded.query = request.URL.RawQuery
			recorded.headers = request.Header.Clone()
			_ = json.Unmarshal(payload, &recorded.body)

			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(reply))
		}))
}

// The OpenAI shape puts the system prompt in the message list.
func TestOpenAICompatibleMapsBothWays(t *testing.T) {
	recorded := &capture{}
	server := stub(t, `{
		"choices": [{"message": {"content": "a summary"}}],
		"usage": {"prompt_tokens": 12, "completion_tokens": 7}
	}`, recorded)
	defer server.Close()

	provider, err := NewOpenAICompatible("openai", server.URL, "test-key")
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	response, err := provider.Complete(context.Background(), Request{
		Model: "gpt-test", System: "You are terse.", Prompt: "Summarise this.",
		Temperature: 0.2, MaxTokens: 500,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if response.Text != "a summary" {
		t.Fatalf("text = %q", response.Text)
	}
	if response.InputTokens != 12 || response.OutputTokens != 7 {
		t.Fatalf("usage was not mapped: %+v", response)
	}
	if !strings.HasSuffix(recorded.path, "/chat/completions") {
		t.Fatalf("wrong endpoint: %s", recorded.path)
	}
	if got := recorded.headers.Get("Authorization"); got != "Bearer test-key" {
		t.Fatalf("authorization header = %q", got)
	}

	messages, _ := recorded.body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected a system and a user message, got %d", len(messages))
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "You are terse." {
		t.Fatalf("the system prompt was not placed as a message: %v", first)
	}
}

// Anthropic takes the system prompt as a top-level field rather than a message.
// The gateway keeps System and Prompt apart precisely so each adapter can put
// them where its provider expects.
func TestAnthropicPlacesTheSystemPromptAtTopLevel(t *testing.T) {
	recorded := &capture{}
	server := stub(t, `{
		"content": [{"text": "a summary"}],
		"usage": {"input_tokens": 20, "output_tokens": 9}
	}`, recorded)
	defer server.Close()

	provider, err := NewAnthropic("anthropic", server.URL, "test-key")
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	response, err := provider.Complete(context.Background(), Request{
		Model: "claude-test", System: "You are terse.", Prompt: "Summarise this.",
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if response.Text != "a summary" {
		t.Fatalf("text = %q", response.Text)
	}
	if response.InputTokens != 20 || response.OutputTokens != 9 {
		t.Fatalf("usage was not mapped: %+v", response)
	}
	if recorded.body["system"] != "You are terse." {
		t.Fatalf("the system prompt was not sent as a top-level field: %v", recorded.body)
	}
	if recorded.headers.Get("x-api-key") != "test-key" {
		t.Fatal("the API key header is missing")
	}
	if recorded.headers.Get("anthropic-version") == "" {
		t.Fatal("the pinned API version was not sent")
	}
	// A provider that requires a token ceiling gets one even when the task did
	// not set it, so an unconfigured task produces a short answer rather than
	// an error or an expensive one.
	if recorded.body["max_tokens"] == nil {
		t.Fatal("no max_tokens default was supplied")
	}
}

func TestGeminiMapsBothWays(t *testing.T) {
	recorded := &capture{}
	server := stub(t, `{
		"candidates": [{"content": {"parts": [{"text": "a summary"}]}}],
		"usageMetadata": {"promptTokenCount": 30, "candidatesTokenCount": 4}
	}`, recorded)
	defer server.Close()

	provider, err := NewGemini("gemini", server.URL, "test-key")
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	response, err := provider.Complete(context.Background(), Request{
		Model: "gemini-test", System: "You are terse.", Prompt: "Summarise this.",
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if response.Text != "a summary" {
		t.Fatalf("text = %q", response.Text)
	}
	if response.InputTokens != 30 || response.OutputTokens != 4 {
		t.Fatalf("usage was not mapped: %+v", response)
	}
	if !strings.Contains(recorded.path, "gemini-test:generateContent") {
		t.Fatalf("the model was not placed in the path: %s", recorded.path)
	}
	if !strings.Contains(recorded.query, "key=test-key") {
		t.Fatalf("the key was not placed in the query: %s", recorded.query)
	}
}

// A provider's own error message can echo the prompt, and the prompt contains
// customer content. It must not travel back to a caller that will render it in
// an operator's browser.
func TestAProvidersErrorMessageIsNotPropagated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(
				`{"error":{"message":"invalid prompt: the customer said secret-content-here"}}`))
		}))
	defer server.Close()

	provider, err := NewOpenAICompatible("openai", server.URL, "test-key")
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	_, err = provider.Complete(context.Background(), Request{Model: "m", Prompt: "hello"})
	if !errors.Is(err, ErrProviderRejected) {
		t.Fatalf("expected ErrProviderRejected, got %v", err)
	}
	if strings.Contains(err.Error(), "secret-content-here") {
		t.Fatalf("the provider echoed content into the error: %v", err)
	}
}

// An empty answer is an error rather than an empty string, so a caller never
// renders "" as a suggestion.
func TestAnEmptyAnswerIsAnError(t *testing.T) {
	recorded := &capture{}
	server := stub(t, `{"choices": []}`, recorded)
	defer server.Close()

	provider, _ := NewOpenAICompatible("openai", server.URL, "test-key")
	if _, err := provider.Complete(
		context.Background(), Request{Model: "m", Prompt: "hello"},
	); !errors.Is(err, ErrProviderRejected) {
		t.Fatalf("expected ErrProviderRejected, got %v", err)
	}
}

// A provider cannot be built without the credentials it needs, so a
// misconfiguration fails at start-up rather than at the first customer request.
func TestAdaptersRefuseIncompleteConfiguration(t *testing.T) {
	if _, err := NewOpenAICompatible("", "", "key"); err == nil {
		t.Fatal("a nameless provider was accepted")
	}
	if _, err := NewAnthropic("anthropic", "", ""); err == nil {
		t.Fatal("a provider with no key was accepted")
	}
	if _, err := NewGemini("gemini", "", ""); err == nil {
		t.Fatal("a provider with no key was accepted")
	}
}
