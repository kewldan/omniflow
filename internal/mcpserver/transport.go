package mcpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/omniflow/omniflow/internal/mcp"
)

// JSON-RPC error codes. The set is small on purpose: a client learns more from
// the message than from a bespoke code, and inventing codes makes the protocol
// harder to speak rather than easier.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *responseError  `json:"error,omitempty"`
}

type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Handler serves MCP over Streamable HTTP.
//
// It answers with JSON rather than an event stream. Streaming exists for
// servers that send progress notifications and server-initiated requests;
// Omniflow's tools complete or fail, and a transport that only does what it
// needs is a transport with less to get wrong.
func (server *Server) Handler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, httpRequest *http.Request) {
		if httpRequest.Method != http.MethodPost {
			// GET opens a server-to-client stream in the full transport. There
			// is nothing to stream, and answering 405 says so plainly rather
			// than holding a connection open forever.
			writer.Header().Set("Allow", http.MethodPost)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		credential := credentialFrom(httpRequest)
		if credential == "" {
			// The challenge names the scheme so a standards-based client knows
			// what to present, and says nothing about why.
			writer.Header().Set("WWW-Authenticate", `Bearer realm="omniflow"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		principal, err := server.options.Authenticator.Authenticate(
			httpRequest.Context(), credential)
		if err != nil {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="omniflow"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}

		var message request
		if err := json.NewDecoder(httpRequest.Body).Decode(&message); err != nil {
			writeResponse(writer, response{
				JSONRPC: "2.0",
				Error:   &responseError{Code: codeParse, Message: "the request is not json"},
			})
			return
		}
		if message.Method == "" {
			writeResponse(writer, response{
				JSONRPC: "2.0", ID: message.ID,
				Error: &responseError{Code: codeInvalidRequest, Message: "no method"},
			})
			return
		}
		// A notification has no id and expects no answer.
		if len(message.ID) == 0 {
			writer.WriteHeader(http.StatusAccepted)
			return
		}

		result, rpcErr := server.dispatch(httpRequest, principal, message)
		writeResponse(writer, response{
			JSONRPC: "2.0", ID: message.ID, Result: result, Error: rpcErr,
		})
	})
}

func (server *Server) dispatch(
	httpRequest *http.Request, principal Principal, message request,
) (any, *responseError) {
	switch message.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcp.ProtocolVersion,
			"serverInfo":      map[string]any{"name": "omniflow", "version": "0.8"},
			"capabilities": map[string]any{
				"tools":     map[string]any{},
				"resources": map[string]any{},
			},
			// The instructions state the contract rather than trying to steer a
			// model. A server that wrote behavioural instructions here would be
			// asking a client to trust text over its own configuration.
			"instructions": "Omniflow admin tools. Every tool is authorised as the " +
				"operator whose token you presented; tools they cannot use are not " +
				"listed. Mutating tools require an idempotency key.",
		}, nil

	case "tools/list":
		tools := server.Available(principal)
		listed := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			entry := map[string]any{
				"name": tool.Name, "description": tool.Description,
				"inputSchema": schemaOrEmpty(tool.InputSchema),
				// The hint is advisory in the protocol and accurate here. It is
				// published so a well-behaved client can prompt before a write,
				// while the actual gate stays on this side.
				"annotations": map[string]any{"readOnlyHint": !tool.Mutates},
			}
			listed = append(listed, entry)
		}
		return map[string]any{"tools": listed}, nil

	case "tools/call":
		return server.callTool(httpRequest, principal, message)

	case "resources/list":
		resources := server.Resources(principal)
		listed := make([]map[string]any, 0, len(resources))
		for _, resource := range resources {
			listed = append(listed, map[string]any{
				"uri": resource.URI, "name": resource.Name,
				"description": resource.Description, "mimeType": mimeOrDefault(resource.MimeType),
			})
		}
		return map[string]any{"resources": listed}, nil

	case "resources/read":
		var params struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return nil, &responseError{Code: codeInvalidParams, Message: "no uri"}
		}
		text, err := server.ReadResource(httpRequest.Context(), principal, params.URI)
		if err != nil {
			return nil, rpcErrorFor(err)
		}
		return map[string]any{"contents": []map[string]any{{
			"uri": params.URI, "mimeType": "text/markdown", "text": text,
		}}}, nil

	case "prompts/list":
		// Omniflow publishes no prompts. Answering an empty list rather than
		// method-not-found keeps a client's capability probe cheap.
		return map[string]any{"prompts": []any{}}, nil

	default:
		return nil, &responseError{Code: codeMethodNotFound, Message: message.Method}
	}
}

func (server *Server) callTool(
	httpRequest *http.Request, principal Principal, message request,
) (any, *responseError) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
		Meta      struct {
			IdempotencyKey string `json:"idempotencyKey"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return nil, &responseError{Code: codeInvalidParams, Message: "unreadable parameters"}
	}

	// The header wins over the parameter. A key in the transport survives a
	// client that reconstructs the body on retry, which is exactly the case the
	// key exists for.
	key := strings.TrimSpace(httpRequest.Header.Get("Mcp-Idempotency-Key"))
	if key == "" {
		key = strings.TrimSpace(params.Meta.IdempotencyKey)
	}

	result, err := server.Invoke(
		httpRequest.Context(), principal, params.Name, params.Arguments, key)
	if err != nil {
		return nil, rpcErrorFor(err)
	}

	payload := map[string]any{
		"content": []map[string]any{{"type": "text", "text": result.Text}},
		"isError": result.IsError,
	}
	if result.Structured != nil {
		payload["structuredContent"] = result.Structured
	}
	return payload, nil
}

// rpcErrorFor maps a refusal to a code without telling the caller more than
// they are entitled to know.
func rpcErrorFor(err error) *responseError {
	switch {
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrMutationsDisabled):
		// Both are reported as invalid-request rather than as a distinct
		// "forbidden" code, because a caller enumerating tools should not learn
		// which ones exist but are out of reach.
		return &responseError{Code: codeInvalidRequest, Message: err.Error()}
	case errors.Is(err, mcp.ErrToolUnknown):
		return &responseError{Code: codeMethodNotFound, Message: err.Error()}
	case errors.Is(err, mcp.ErrArgumentsInvalid),
		errors.Is(err, ErrIdempotencyRequired),
		errors.Is(err, ErrIdempotencyConflict):
		return &responseError{Code: codeInvalidParams, Message: err.Error()}
	default:
		return &responseError{Code: codeInternal, Message: err.Error()}
	}
}

func schemaOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{"type":"object"}`)
	}
	return raw
}

func mimeOrDefault(mime string) string {
	if mime == "" {
		return "text/markdown"
	}
	return mime
}

func writeResponse(writer http.ResponseWriter, message response) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(message)
}
