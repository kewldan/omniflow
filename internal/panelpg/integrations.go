package panelpg

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/egress"
	"github.com/omniflow/omniflow/internal/rbac"
)

// WebhookTopics are the events an outgoing webhook can subscribe to: the
// outbox topics the installation publishes.
var WebhookTopics = []string{"order.paid", "wallet.credited", "addon.purchased"}

// APIToken is a named token for the /v1/admin surface, without its secret.
type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	// FullAccess is a token with no permission list: every /v1/admin route.
	// Permissions is the list otherwise.
	FullAccess  bool     `json:"fullAccess"`
	Permissions []string `json:"permissions"`
	// Token is set once, in the answer to its creation, and never again.
	Token string `json:"token,omitempty"`
}

// HashAPIToken is how a token is stored and looked up.
func HashAPIToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// CreateAPIToken mints a named token. Its value is in the answer and nowhere
// else; only its hash is stored.
//
// A token is scoped to permissions from the operators' catalogue, or asks for
// full access explicitly; a token with neither could do nothing and is
// refused.
func (service *Service) CreateAPIToken(ctx context.Context, name string, permissions []string, fullAccess bool, actor Actor) (APIToken, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return APIToken{}, ErrValidaton
	}
	var scoped []string
	if !fullAccess {
		if len(permissions) == 0 || len(permissions) > 64 {
			return APIToken{}, ErrValidaton
		}
		seen := map[string]bool{}
		for _, permission := range permissions {
			permission = strings.TrimSpace(permission)
			if !rbac.Known(rbac.Permission(permission)) {
				return APIToken{}, ErrValidaton
			}
			if !seen[permission] {
				seen[permission] = true
				scoped = append(scoped, permission)
			}
		}
		sort.Strings(scoped)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return APIToken{}, err
	}
	token := "oft_" + base64.RawURLEncoding.EncodeToString(raw)
	createdBy := pgtype.UUID{}
	if actor.AdminID != "" {
		createdBy, _ = parseUUID(actor.AdminID)
	}
	var created APIToken
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.CreateAPIToken(ctx, dbgen.CreateAPITokenParams{
			Name: name, TokenHash: HashAPIToken(token), Prefix: token[:10], CreatedBy: createdBy,
			Permissions: scoped,
		})
		if err != nil {
			return err
		}
		created = apiTokenFrom(row)
		created.Token = token
		return appendAudit(ctx, queries, actor.audit("system.api_token.created", "system", "api_token", created.ID,
			map[string]any{"name": name, "fullAccess": scoped == nil, "permissions": scoped}))
	})
	return created, err
}

// APITokens lists the named tokens.
func (service *Service) APITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := service.queries().ListAPITokens(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]APIToken, 0, len(rows))
	for _, row := range rows {
		out = append(out, apiTokenFrom(row))
	}
	return out, nil
}

// RevokeAPIToken ends a token at once.
func (service *Service) RevokeAPIToken(ctx context.Context, tokenID string, actor Actor) error {
	id, err := parseUUID(tokenID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		revoked, err := queries.RevokeAPIToken(ctx, id)
		if err != nil {
			return err
		}
		if revoked == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit("system.api_token.revoked", "system", "api_token", tokenID, nil))
	})
}

// APITokenRequest is one request a named token made.
type APITokenRequest struct {
	Method     string    `json:"method"`
	Route      string    `json:"route"`
	Status     int32     `json:"status"`
	OccurredAt time.Time `json:"occurredAt"`
}

// APITokenRequests reads a token's latest requests.
func (service *Service) APITokenRequests(ctx context.Context, tokenID string) ([]APITokenRequest, error) {
	id, err := parseUUID(tokenID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListAPITokenRequests(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]APITokenRequest, 0, len(rows))
	for _, row := range rows {
		out = append(out, APITokenRequest{Method: row.Method, Route: row.Route, Status: row.Status, OccurredAt: row.OccurredAt.Time})
	}
	return out, nil
}

func apiTokenFrom(row dbgen.ApiToken) APIToken {
	return APIToken{
		ID: uuidString(row.ID), Name: row.Name, Prefix: row.Prefix, CreatedAt: row.CreatedAt.Time,
		LastUsedAt: timePointer(row.LastUsedAt), RevokedAt: timePointer(row.RevokedAt),
		FullAccess: row.Permissions == nil, Permissions: row.Permissions,
	}
}

// WebhookEndpoint is an outgoing webhook as the panel shows it.
type WebhookEndpoint struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	URL             string     `json:"url"`
	Events          []string   `json:"events"`
	Enabled         bool       `json:"enabled"`
	FailedCount     int64      `json:"failedCount"`
	LastDeliveredAt *time.Time `json:"lastDeliveredAt,omitempty"`
	// Secret is set once, in the answer to its creation.
	Secret string `json:"secret,omitempty"`
}

// WebhookInput creates an endpoint.
type WebhookInput struct {
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

const webhookSecretLabel = "webhook_endpoint"

// CreateWebhook registers an endpoint. The address must be public https —
// the same egress policy every outbound call follows — and the signing
// secret is generated here, sealed, and shown once.
func (service *Service) CreateWebhook(ctx context.Context, input WebhookInput, actor Actor) (WebhookEndpoint, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Events) == 0 || (egress.Policy{}).CheckURL(input.URL) != nil {
		return WebhookEndpoint{}, ErrValidaton
	}
	for _, event := range input.Events {
		if !slices.Contains(WebhookTopics, event) {
			return WebhookEndpoint{}, ErrValidaton
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return WebhookEndpoint{}, err
	}
	secret := "whsec_" + hex.EncodeToString(raw)
	sealed, err := service.sealSecret(secret, webhookSecretLabel)
	if err != nil {
		return WebhookEndpoint{}, err
	}
	createdBy := pgtype.UUID{}
	if actor.AdminID != "" {
		createdBy, _ = parseUUID(actor.AdminID)
	}
	var created WebhookEndpoint
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.CreateWebhookEndpoint(ctx, dbgen.CreateWebhookEndpointParams{
			Name: input.Name, Url: input.URL, Events: input.Events, SecretCiphertext: sealed, CreatedBy: createdBy,
		})
		if err != nil {
			return err
		}
		created = WebhookEndpoint{ID: uuidString(row.ID), Name: row.Name, URL: row.Url, Events: row.Events, Enabled: row.Enabled, Secret: secret}
		return appendAudit(ctx, queries, actor.audit("system.webhook.created", "system", "webhook_endpoint", created.ID,
			map[string]any{"events": input.Events}))
	})
	return created, err
}

// Webhooks lists the endpoints with their delivery health.
func (service *Service) Webhooks(ctx context.Context) ([]WebhookEndpoint, error) {
	rows, err := service.queries().ListWebhookEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]WebhookEndpoint, 0, len(rows))
	for _, row := range rows {
		out = append(out, WebhookEndpoint{
			ID: uuidString(row.ID), Name: row.Name, URL: row.Url, Events: row.Events, Enabled: row.Enabled,
			FailedCount: row.FailedCount, LastDeliveredAt: timePointer(row.LastDeliveredAt),
		})
	}
	return out, nil
}

// SetWebhookEnabled pauses or resumes an endpoint; DeleteWebhook removes it.
func (service *Service) SetWebhookEnabled(ctx context.Context, endpointID string, enabled bool, actor Actor) error {
	id, err := parseUUID(endpointID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		changed, err := queries.SetWebhookEndpointEnabled(ctx, dbgen.SetWebhookEndpointEnabledParams{EndpointID: id, Enabled: enabled})
		if err != nil {
			return err
		}
		if changed == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit("system.webhook.enabled_set", "system", "webhook_endpoint", endpointID,
			map[string]any{"enabled": enabled}))
	})
}

// DeleteWebhook removes an endpoint and its delivery log.
func (service *Service) DeleteWebhook(ctx context.Context, endpointID string, actor Actor) error {
	id, err := parseUUID(endpointID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, err := queries.DeleteWebhookEndpoint(ctx, id)
		if err != nil {
			return err
		}
		if removed == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit("system.webhook.deleted", "system", "webhook_endpoint", endpointID, nil))
	})
}

// WebhookDelivery is one delivery in an endpoint's log.
type WebhookDelivery struct {
	ID             string     `json:"id"`
	Topic          string     `json:"topic"`
	Status         string     `json:"status"`
	Attempts       int32      `json:"attempts"`
	ResponseStatus *int32     `json:"responseStatus,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
}

// WebhookDeliveries reads an endpoint's latest deliveries.
func (service *Service) WebhookDeliveries(ctx context.Context, endpointID string) ([]WebhookDelivery, error) {
	id, err := parseUUID(endpointID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListWebhookDeliveries(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]WebhookDelivery, 0, len(rows))
	for _, row := range rows {
		out = append(out, WebhookDelivery{
			ID: uuidString(row.ID), Topic: row.Topic, Status: row.Status, Attempts: row.Attempts,
			ResponseStatus: int32Pointer(row.ResponseStatus), CreatedAt: row.CreatedAt.Time, DeliveredAt: timePointer(row.DeliveredAt),
		})
	}
	return out, nil
}

// webhookMaxAttempts bounds retries: with the backoff below, about a day.
const webhookMaxAttempts = 10

// SignWebhook is the signature a receiver checks:
// hex(HMAC-SHA256(secret, timestamp + "." + body)).
func SignWebhook(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// DispatchWebhooks fans new outbox events out to subscribed endpoints and
// sends what is due, signed, with exponential backoff. The worker runs it.
func (service *Service) DispatchWebhooks(ctx context.Context, client *http.Client) error {
	if _, err := service.queries().FanOutOutboxEvents(ctx); err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		due, err := queries.DueWebhookDeliveries(ctx)
		if err != nil {
			return err
		}
		for _, delivery := range due {
			secret, openErr := service.OpenSecret(delivery.SecretCiphertext, webhookSecretLabel)
			status, code := "pending", pgtype.Int4{}
			if openErr == nil {
				code = service.sendWebhook(ctx, client, delivery.Url, secret, uuidString(delivery.ID), delivery.Payload)
			}
			attempts := delivery.Attempts + 1
			next := service.now().Add(time.Duration(1<<min(attempts, 16)) * 10 * time.Second)
			switch {
			case code.Valid && code.Int32 >= 200 && code.Int32 < 300:
				status = "delivered"
			case attempts >= webhookMaxAttempts:
				status = "failed"
			}
			if err = queries.RecordWebhookAttempt(ctx, dbgen.RecordWebhookAttemptParams{
				DeliveryID: delivery.ID, ResponseStatus: code, Status: status, NextAttemptAt: timestamp(next),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (service *Service) sendWebhook(ctx context.Context, client *http.Client, url, secret, deliveryID string, body []byte) pgtype.Int4 {
	now := service.now().Unix()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return pgtype.Int4{}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Omniflow-Webhooks/1")
	request.Header.Set("X-Omniflow-Delivery", deliveryID)
	request.Header.Set("X-Omniflow-Timestamp", strconv.FormatInt(now, 10))
	request.Header.Set("X-Omniflow-Signature", "sha256="+SignWebhook(secret, now, body))
	response, err := client.Do(request)
	if err != nil {
		return pgtype.Int4{}
	}
	_ = response.Body.Close()
	return pgtype.Int4{Int32: int32(response.StatusCode), Valid: true}
}
