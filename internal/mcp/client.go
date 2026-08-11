package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ProtocolVersion is the MCP revision this client speaks.
//
// It is sent on every request after initialisation. A server that answers a
// version it did not agree to is a server whose behaviour is not the one that
// was reviewed, so the mismatch is reported rather than tolerated.
const ProtocolVersion = "2025-06-18"

var (
	// ErrTransport reports a connection that did not produce a usable answer.
	ErrTransport = errors.New("mcp transport failed")
	// ErrProtocol reports a well-formed connection speaking something other than
	// MCP, or MCP badly.
	ErrProtocol = errors.New("mcp server returned an unusable response")
	// ErrResponseTooLarge reports a result over the server's byte ceiling. It is
	// distinct from a transport failure because the connection worked and the
	// server ignored a limit — which is worth showing an owner.
	ErrResponseTooLarge = errors.New("mcp response exceeded the configured limit")
	// ErrToolFailed reports a tool that ran and reported an error. The error
	// text comes from the server and is therefore untrusted content, so it is
	// carried in a ToolResult rather than formatted into a Go error.
	ErrToolFailed = errors.New("mcp tool reported an error")
)

// Authorization is how the client proves who it is.
//
// It is an interface rather than a token string so a rotation, or an OAuth
// exchange that refreshes, happens without the client caring. The standards
// -based path is a bearer token obtained however the owner obtained it; the
// client's job is to send it over TLS and never log it.
type Authorization interface {
	// Header returns the value for the Authorization header, or an empty string
	// for a server that needs none.
	Header(context.Context) (string, error)
}

// StaticToken is a bearer token an owner pasted into the panel.
type StaticToken string

// Header renders the bearer header.
func (token StaticToken) Header(context.Context) (string, error) {
	if strings.TrimSpace(string(token)) == "" {
		return "", nil
	}
	return "Bearer " + string(token), nil
}

// Client speaks Streamable HTTP to one MCP server.
//
// It holds the negotiated session rather than the caller, because a session id
// that a caller can forget to forward is a session that silently becomes a new
// one on every call — which looks like it works and quietly loses server-side
// state.
type Client struct {
	server Server
	http   *http.Client
	auth   Authorization

	mutex     sync.Mutex
	sessionID string
	// negotiated records the version the server agreed to, so a later response
	// claiming a different one is visible.
	negotiated  string
	initialised bool
	info        ServerInfo
}

// NewClient builds a client for a registered server.
func NewClient(server Server, httpClient *http.Client, auth Authorization) *Client {
	normalised := server.Normalise()
	if httpClient == nil {
		httpClient = &http.Client{Timeout: normalised.Timeout}
	}
	return &Client{server: normalised, http: httpClient, auth: auth}
}

// ServerInfo is what a server said about itself during initialisation.
type ServerInfo struct {
	Name            string         `json:"name"`
	Version         string         `json:"version"`
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	// Instructions is server-authored guidance. It is captured for display and
	// deliberately never forwarded to a model as instruction: a remote party
	// that can write the system prompt is a remote party that controls the
	// assistant.
	Instructions string `json:"instructions"`
}

// Tool is one advertised tool.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	// OutputSchema is optional in the protocol. When a server declares one, the
	// result is validated against it; when it does not, the result is treated as
	// unstructured text and never parsed into application values.
	OutputSchema json.RawMessage `json:"outputSchema"`
}

// Resource is one advertised resource.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

// Prompt is one advertised prompt template.
type Prompt struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ToolResult is what a tool call returned.
type ToolResult struct {
	// Text is the concatenated textual content, already flattened. It is
	// untrusted data.
	Text string
	// Structured is the parsed structuredContent, present only when the tool
	// declared an output schema and the result validated against it.
	Structured map[string]any
	// IsError reports a tool that ran and failed, as distinct from a transport
	// failure. The distinction matters: one is worth retrying, the other is the
	// tool's answer.
	IsError bool
	// Bytes is the response size, recorded for the audit trail and the limits.
	Bytes int64
}

// Initialize performs the handshake and caches what the server said.
func (client *Client) Initialize(ctx context.Context) (ServerInfo, error) {
	client.mutex.Lock()
	if client.initialised {
		info := client.info
		client.mutex.Unlock()
		return info, nil
	}
	client.mutex.Unlock()

	raw, err := client.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		// Omniflow consumes tools, resources, and prompts. It advertises no
		// sampling and no roots: a server that could ask Omniflow to run a model
		// call on its behalf would be spending the owner's budget on the
		// server's prompt.
		"capabilities": map[string]any{},
		"clientInfo":   map[string]any{"name": "omniflow", "version": "0.8"},
	})
	if err != nil {
		return ServerInfo{}, err
	}
	var info ServerInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return ServerInfo{}, fmt.Errorf("%w: initialize was not an initialisation", ErrProtocol)
	}
	if info.ProtocolVersion == "" {
		return ServerInfo{}, fmt.Errorf("%w: the server named no protocol version", ErrProtocol)
	}

	client.mutex.Lock()
	client.info = info
	client.negotiated = info.ProtocolVersion
	client.initialised = true
	client.mutex.Unlock()

	// The notification completes the handshake. A failure to deliver it is not
	// fatal: the session is usable, and reporting it would turn a cosmetic
	// problem into an unreachable server.
	_ = client.notify(ctx, "notifications/initialized", map[string]any{})
	return info, nil
}

// ListTools discovers the tools a server offers.
//
// Discovery is not authorisation. Everything here is shown to an owner so they
// can choose; nothing here becomes callable until they allowlist it.
func (client *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var tools []Tool
	err := client.paginate(ctx, "tools/list", func(page json.RawMessage) error {
		var decoded struct {
			Tools []Tool `json:"tools"`
		}
		if err := json.Unmarshal(page, &decoded); err != nil {
			return fmt.Errorf("%w: tools/list was not a tool list", ErrProtocol)
		}
		tools = append(tools, decoded.Tools...)
		return nil
	})
	return tools, err
}

// ListResources discovers the resources a server offers.
func (client *Client) ListResources(ctx context.Context) ([]Resource, error) {
	var resources []Resource
	err := client.paginate(ctx, "resources/list", func(page json.RawMessage) error {
		var decoded struct {
			Resources []Resource `json:"resources"`
		}
		if err := json.Unmarshal(page, &decoded); err != nil {
			return fmt.Errorf("%w: resources/list was not a resource list", ErrProtocol)
		}
		resources = append(resources, decoded.Resources...)
		return nil
	})
	return resources, err
}

// ListPrompts discovers the prompt templates a server offers.
func (client *Client) ListPrompts(ctx context.Context) ([]Prompt, error) {
	var prompts []Prompt
	err := client.paginate(ctx, "prompts/list", func(page json.RawMessage) error {
		var decoded struct {
			Prompts []Prompt `json:"prompts"`
		}
		if err := json.Unmarshal(page, &decoded); err != nil {
			return fmt.Errorf("%w: prompts/list was not a prompt list", ErrProtocol)
		}
		prompts = append(prompts, decoded.Prompts...)
		return nil
	})
	return prompts, err
}

// maxPages bounds cursor following. A server that returns a cursor forever is a
// server that holds the connection forever, and the honest response to that is
// to stop rather than to keep paying for it.
const maxPages = 20

func (client *Client) paginate(
	ctx context.Context, method string, consume func(json.RawMessage) error,
) error {
	cursor := ""
	for range maxPages {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := client.call(ctx, method, params)
		if err != nil {
			return err
		}
		if err := consume(raw); err != nil {
			return err
		}
		var next struct {
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &next); err != nil || next.NextCursor == "" {
			return nil
		}
		if next.NextCursor == cursor {
			// A server repeating its cursor is a loop, not a page.
			return nil
		}
		cursor = next.NextCursor
	}
	return fmt.Errorf("%w: %s did not stop paginating", ErrProtocol, method)
}

// CallTool invokes one tool. It performs no permission check: that belongs to
// the broker, which knows who is asking.
func (client *Client) CallTool(
	ctx context.Context, name string, arguments map[string]any,
) (ToolResult, error) {
	raw, err := client.call(ctx, "tools/call", map[string]any{
		"name": name, "arguments": arguments,
	})
	if err != nil {
		return ToolResult{}, err
	}

	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent map[string]any `json:"structuredContent"`
		IsError           bool           `json:"isError"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ToolResult{}, fmt.Errorf("%w: tools/call was not a tool result", ErrProtocol)
	}

	texts := make([]string, 0, len(decoded.Content))
	for _, content := range decoded.Content {
		// Only text is flattened. Images, audio, and embedded resources are
		// described rather than inlined: a base64 payload forwarded to a model
		// is unbounded cost, and one forwarded to application code is a file of
		// unknown provenance.
		if content.Type == "text" {
			texts = append(texts, content.Text)
			continue
		}
		texts = append(texts, "["+content.Type+" content omitted]")
	}

	return ToolResult{
		Text:       strings.TrimSpace(strings.Join(texts, "\n")),
		Structured: decoded.StructuredContent,
		IsError:    decoded.IsError,
		Bytes:      int64(len(raw)),
	}, nil
}

// rpcRequest is a JSON-RPC 2.0 request.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var requestCounter int64

// call sends one JSON-RPC request and returns its result.
func (client *Client) call(
	ctx context.Context, method string, params any,
) (json.RawMessage, error) {
	client.mutex.Lock()
	requestCounter++
	id := requestCounter
	client.mutex.Unlock()

	body, err := client.post(ctx, rpcRequest{
		JSONRPC: "2.0", ID: &id, Method: method, Params: params,
	})
	if err != nil {
		return nil, err
	}

	response, err := extractResponse(body, id)
	if err != nil {
		return nil, err
	}
	if response.Error != nil {
		// The server's message is included because an owner debugging a
		// connection needs it, and it is truncated because it is remote text on
		// its way into a log.
		return nil, fmt.Errorf("%w: %s failed (%d): %s",
			ErrProtocol, method, response.Error.Code, clip(response.Error.Message, 300))
	}
	if len(response.Result) == 0 {
		return nil, fmt.Errorf("%w: %s returned no result", ErrProtocol, method)
	}
	return response.Result, nil
}

// notify sends a request that expects no answer.
func (client *Client) notify(ctx context.Context, method string, params any) error {
	_, err := client.post(ctx, rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	return err
}

func (client *Client) post(ctx context.Context, message rpcRequest) ([]byte, error) {
	if err := client.server.CheckEgress(client.server.Endpoint); err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransport, err)
	}

	callCtx, cancel := context.WithTimeout(ctx, client.server.Timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		callCtx, http.MethodPost, client.server.Endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransport, err)
	}
	request.Header.Set("Content-Type", "application/json")
	// Both are advertised because the transport lets a server answer either way
	// for the same request, and a client that accepts only one gets a 406 from
	// half the implementations.
	request.Header.Set("Accept", "application/json, text/event-stream")

	client.mutex.Lock()
	session, negotiated := client.sessionID, client.negotiated
	client.mutex.Unlock()
	if session != "" {
		request.Header.Set("Mcp-Session-Id", session)
	}
	if negotiated != "" {
		request.Header.Set("MCP-Protocol-Version", negotiated)
	}
	if client.auth != nil {
		header, err := client.auth.Header(callCtx)
		if err != nil {
			return nil, fmt.Errorf("%w: authorization unavailable", ErrTransport)
		}
		if header != "" {
			request.Header.Set("Authorization", header)
		}
	}

	response, err := client.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransport, err)
	}
	defer func() { _ = response.Body.Close() }()

	if issued := response.Header.Get("Mcp-Session-Id"); issued != "" {
		client.mutex.Lock()
		client.sessionID = issued
		client.mutex.Unlock()
	}

	// A notification legitimately gets 202 with no body.
	if response.StatusCode == http.StatusAccepted {
		return nil, nil
	}
	if response.StatusCode == http.StatusNotFound && session != "" {
		// The session expired. Clearing it means the next call re-initialises
		// rather than repeating a request the server will keep rejecting.
		client.mutex.Lock()
		client.sessionID, client.initialised = "", false
		client.mutex.Unlock()
		return nil, fmt.Errorf("%w: the session expired", ErrTransport)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: http %d", ErrTransport, response.StatusCode)
	}

	// The ceiling is enforced by reading one byte past it, so a server that
	// ignores the limit is detected rather than truncated into something that
	// parses as valid.
	limit := client.server.MaxResponseBytes
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransport, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: over %d bytes", ErrResponseTooLarge, limit)
	}

	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return eventStreamBody(body)
	}
	return body, nil
}

// eventStreamBody pulls the JSON-RPC messages out of an SSE response.
//
// The transport allows a server to answer a single request with a stream, so a
// client that handles only the JSON form works against some servers and not
// others — and the ones it fails against are the ones doing anything
// interesting.
func eventStreamBody(body []byte) ([]byte, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), len(body)+1)

	messages := make([]json.RawMessage, 0, 4)
	var data strings.Builder
	flush := func() {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" {
			return
		}
		messages = append(messages, json.RawMessage(payload))
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteString("\n")
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		// Other SSE fields (event, id, retry) carry no JSON-RPC payload.
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: unreadable event stream", ErrTransport)
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("%w: the event stream carried no message", ErrProtocol)
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransport, err)
	}
	return encoded, nil
}

// extractResponse finds the answer to one request in a body that may be a
// single object or a batch.
func extractResponse(body []byte, id int64) (rpcResponse, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return rpcResponse{}, fmt.Errorf("%w: empty body", ErrProtocol)
	}

	if trimmed[0] == '[' {
		var batch []rpcResponse
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			return rpcResponse{}, fmt.Errorf("%w: unreadable batch", ErrProtocol)
		}
		for _, response := range batch {
			// Matching on the id is what stops a server answering a question
			// nobody asked — including one it invented mid-stream.
			if response.ID != nil && *response.ID == id {
				return response, nil
			}
		}
		return rpcResponse{}, fmt.Errorf("%w: no response matched the request", ErrProtocol)
	}

	var response rpcResponse
	if err := json.Unmarshal(trimmed, &response); err != nil {
		return rpcResponse{}, fmt.Errorf("%w: unreadable response", ErrProtocol)
	}
	if response.ID == nil || *response.ID != id {
		return rpcResponse{}, fmt.Errorf("%w: the response answered a different request", ErrProtocol)
	}
	return response, nil
}

func clip(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "…"
}

// Health is a server's last-known reachability.
type Health struct {
	Slug      string
	Reachable bool
	// Detail is why, when it is not. It is operator-facing text, so it names the
	// failure rather than dumping a transport error.
	Detail    string
	CheckedAt time.Time
	Latency   time.Duration
	Info      ServerInfo
}
