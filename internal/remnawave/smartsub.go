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

// The smart subscription reads from the panel: its hosts and internal
// squads, the user a link belongs to, and the subscription body a client
// would get. None of them writes anything.

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
	Inbound    struct {
		ConfigProfileInboundUUID string `json:"configProfileInboundUuid"`
	} `json:"inbound"`
	// InternalSquads narrows which squads get the host: ALLOW_ONLY those
	// listed, or EXCLUDE them.
	InternalSquads struct {
		Mode   string   `json:"mode"`
		Squads []string `json:"squads"`
	} `json:"internalSquads"`
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

// UserByShortUUID reads the user a subscription link belongs to, by the
// short UUID the link ends in. It is a plain user read: unlike the panel's
// subscription routes it runs no device check and records no request in the
// user's history.
func (client *Client) UserByShortUUID(ctx context.Context, shortUUID string) (User, error) {
	if !ValidShortUUID(shortUUID) {
		return User{}, ErrNotFound
	}
	var envelope struct {
		Response User `json:"response"`
	}
	if err := client.get(ctx, "/api/users/by-short-uuid/"+url.PathEscape(shortUUID), &envelope); err != nil {
		return User{}, err
	}
	return envelope.Response, nil
}

// InternalSquadInbounds maps each internal squad to the inbounds it grants,
// which is what decides the hosts a squad's members get, and each of those
// inbounds to its protocol ("vless", "hysteria", …), which decides whether a
// client reaches it over TCP or UDP.
func (client *Client) InternalSquadInbounds(ctx context.Context) (map[string][]string, map[string]string, error) {
	var envelope struct {
		Response struct {
			InternalSquads []struct {
				UUID     string `json:"uuid"`
				Inbounds []struct {
					UUID string `json:"uuid"`
					Type string `json:"type"`
				} `json:"inbounds"`
			} `json:"internalSquads"`
		} `json:"response"`
	}
	if err := client.get(ctx, "/api/internal-squads", &envelope); err != nil {
		return nil, nil, err
	}
	squads := make(map[string][]string, len(envelope.Response.InternalSquads))
	types := map[string]string{}
	for _, squad := range envelope.Response.InternalSquads {
		inbounds := make([]string, 0, len(squad.Inbounds))
		for _, inbound := range squad.Inbounds {
			inbounds = append(inbounds, inbound.UUID)
			if inbound.Type != "" {
				types[inbound.UUID] = inbound.Type
			}
		}
		squads[squad.UUID] = inbounds
	}
	return squads, types, nil
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
