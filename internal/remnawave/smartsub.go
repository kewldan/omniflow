package remnawave

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// The smart subscription reads three things from the panel: its hosts, a
// user's subscription as the panel resolves it, and the subscription body a
// client would get. None of them writes anything.

// Host is one Remnawave host, with the fields the smart subscription places
// servers by.
type Host struct {
	UUID       string   `json:"uuid"`
	Remark     string   `json:"remark"`
	Address    string   `json:"address"`
	Port       int      `json:"port"`
	IsDisabled bool     `json:"isDisabled"`
	IsHidden   bool     `json:"isHidden"`
	Nodes      []string `json:"nodes"`
}

// Hosts lists the panel's hosts.
func (client *Client) Hosts(ctx context.Context) ([]Host, error) {
	var envelope struct {
		Response []Host `json:"response"`
	}
	if err := client.get(ctx, "/api/hosts", &envelope); err != nil {
		return nil, err
	}
	return envelope.Response, nil
}

// ResolvedEntry is one server of a user's subscription as the panel resolved
// it for that user: the remark after templating, and the host it came from.
type ResolvedEntry struct {
	FinalRemark string `json:"finalRemark"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	Metadata    struct {
		UUID       string `json:"uuid"`
		IsHidden   bool   `json:"isHidden"`
		IsDisabled bool   `json:"isDisabled"`
	} `json:"metadata"`
}

// RawSubscription is a user's subscription without a client format applied.
type RawSubscription struct {
	UserID  int64
	Entries []ResolvedEntry
}

// RawSubscription reads a user's resolved servers by the short UUID their
// link carries. It is an authenticated read: unlike the public link, it
// registers no device and records no request in the user's history.
func (client *Client) RawSubscription(ctx context.Context, shortUUID string) (RawSubscription, error) {
	if !ValidShortUUID(shortUUID) {
		return RawSubscription{}, ErrNotFound
	}
	var envelope struct {
		Response struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
			Entries []ResolvedEntry `json:"resolvedProxyConfigs"`
		} `json:"response"`
	}
	path := "/api/subscriptions/by-short-uuid/" + url.PathEscape(shortUUID) + "/raw"
	if err := client.get(ctx, path, &envelope); err != nil {
		return RawSubscription{}, err
	}
	return RawSubscription{UserID: envelope.Response.User.ID, Entries: envelope.Response.Entries}, nil
}

// ValidShortUUID reports whether value can be a panel short UUID: the
// characters the panel mints them from, at a length it could mint.
func ValidShortUUID(value string) bool {
	if len(value) < 4 || len(value) > 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

// SubscriptionFormatXrayJSON asks the panel for Xray JSON whatever its
// response rules would pick for the client's User-Agent.
const SubscriptionFormatXrayJSON = "json"

// maxSubscriptionBytes bounds a subscription body. A few hundred hosts of
// Xray JSON is well under a megabyte; the cap is there for a panel that
// answers with something else entirely.
const maxSubscriptionBytes = 8 << 20

// forwardedRequestHeaders are the client's headers the panel needs to answer
// as it would have answered the client: the User-Agent its response rules
// read, the HWID headers its device limit counts, and the language.
var forwardedRequestHeaders = []string{
	"User-Agent", "Accept", "Accept-Language",
	"X-Hwid", "X-Device-Os", "X-Ver-Os", "X-Device-Model", "X-App-Version",
}

// SubscriptionContent is the panel's answer to a client's subscription
// request.
type SubscriptionContent struct {
	Status int
	Header http.Header
	Body   []byte
}

// FetchSubscription asks the panel's public subscription route for a user's
// subscription on behalf of a client, forwarding the headers the panel reads
// and the client's address. format is empty for the panel's own choice, or
// one of its client types such as SubscriptionFormatXrayJSON.
//
// Any answer the panel gives — a 403 for a blocked client, a 404 for a revoked
// link — is returned as content, because the client should see it; only a
// failure to get an answer at all is an error.
func (client *Client) FetchSubscription(
	ctx context.Context, shortUUID, format string, from http.Header, clientIP string,
) (SubscriptionContent, error) {
	if !ValidShortUUID(shortUUID) {
		return SubscriptionContent{}, ErrNotFound
	}
	endpoint := client.baseURL.JoinPath("api", "sub", shortUUID)
	operation := "GET /api/sub/:id"
	if format != "" {
		endpoint = endpoint.JoinPath(format)
		operation += "/" + format
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return SubscriptionContent{}, fmt.Errorf("create Remnawave request: %w", err)
	}
	for _, name := range forwardedRequestHeaders {
		if value := from.Get(name); value != "" {
			request.Header.Set(name, value)
		}
	}
	// The panel's proxy-facing settings still apply: a panel behind
	// caddy-with-auth or a secret cookie wants them on this route too.
	client.options.authenticate(request, client.token)
	request.Header.Del("Authorization")
	request.Header.Set("X-Forwarded-Proto", "https")
	if clientIP != "" {
		request.Header.Set("X-Forwarded-For", clientIP)
		request.Header.Set("X-Real-IP", clientIP)
	}

	started := time.Now()
	content, err := client.fetchContent(request)
	if client.observer != nil {
		outcome, code := classifyCall(err)
		if err == nil && content.Status >= 500 {
			outcome, code = "failed", fmt.Sprintf("remnawave_http_%d", content.Status)
		}
		client.observer(operation, outcome, code, time.Since(started))
	}
	return content, err
}

func (client *Client) fetchContent(request *http.Request) (SubscriptionContent, error) {
	response, err := client.http.Do(request)
	if err != nil {
		return SubscriptionContent{}, fmt.Errorf("call Remnawave subscription: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxSubscriptionBytes+1))
	if err != nil {
		return SubscriptionContent{}, fmt.Errorf("read Remnawave subscription: %w", err)
	}
	if len(body) > maxSubscriptionBytes {
		return SubscriptionContent{}, errors.New("Remnawave subscription is larger than the limit")
	}
	return SubscriptionContent{Status: response.StatusCode, Header: response.Header.Clone(), Body: body}, nil
}
