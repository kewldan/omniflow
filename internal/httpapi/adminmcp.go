package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/mcp"
	"github.com/omniflow/omniflow/internal/mcpserver"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// MCPHandler builds Omniflow's own MCP server: the read side of the
// automation surface, offered as tools an assistant can call.
//
// It is served at /v1/mcp behind the same named-token check and request log
// as /v1/admin, and authorises each call as the operator who made the token,
// within the token's permissions. Every tool here only reads; nothing an
// assistant does through it changes Omniflow, so the server's mutation switch
// has nothing to enable. Writes go through /v1/admin, where the caller states
// each one explicitly.
func (handlers *AdminHandlers) MCPHandler() (http.Handler, error) {
	if handlers.operations == nil {
		return nil, errors.New("the mcp server needs the operator panel's services")
	}
	server, err := mcpserver.New(mcpserver.Options{
		Authenticator: mcpTokenAuthenticator{service: handlers.service},
		Audit:         mcpAudit{service: handlers.service},
		Tools:         handlers.mcpServerTools(),
	})
	if err != nil {
		return nil, err
	}
	return server.Handler(), nil
}

// mcpTokenAuthenticator reads the named token the /v1/admin middleware has
// already resolved; the credential itself is not looked up twice.
type mcpTokenAuthenticator struct {
	service *adminauthpg.Service
}

func (authenticator mcpTokenAuthenticator) Authenticate(ctx context.Context, _ string) (mcpserver.Principal, error) {
	name, createdBy, permissions, fullAccess, ok := APITokenFrom(ctx)
	if !ok {
		return mcpserver.Principal{}, mcpserver.ErrUnauthorized
	}
	principal, err := authenticator.service.PrincipalForAPIToken(ctx, createdBy, permissions, fullAccess)
	if err != nil {
		return mcpserver.Principal{}, mcpserver.ErrUnauthorized
	}
	return mcpserver.Principal{
		ID: principal.Account.ID, Label: "api-token:" + name, Grant: mcpGrant{grant: principal.Grant},
	}, nil
}

type mcpGrant struct{ grant rbac.Grant }

func (grant mcpGrant) Allows(permission string) bool {
	return grant.grant.Allows(rbac.Permission(permission))
}

// mcpAudit writes each tool call, refusals included, to the audit trail
// under the operator the token acts as.
type mcpAudit struct {
	service *adminauthpg.Service
}

func (audit mcpAudit) Record(ctx context.Context, event mcp.Event) error {
	outcome, category := "success", "system"
	switch event.Outcome {
	case "refused":
		outcome, category = "denied", "authorization"
	case "failed":
		outcome = "failure"
	}
	return audit.service.AppendAudit(context.WithoutCancel(ctx), adminauthpg.AuditEntry{
		ActorType: "admin", ActorID: event.Operator,
		Action: "mcp.tool_call", Category: category, Outcome: outcome,
		TargetType: "mcp_tool", TargetID: event.Tool,
		Metadata: map[string]any{"detail": event.Detail, "outcome": event.Outcome},
	})
}

const (
	mcpPeriodSchema = `{"type":"object","additionalProperties":false,"properties":{
		"since":{"type":"string","description":"RFC 3339 start; default thirty days before until"},
		"until":{"type":"string","description":"RFC 3339 end; default now"},
		"timezone":{"type":"string","maxLength":64},
		"currency":{"type":"string","pattern":"^[A-Za-z]{3}$"}}}`
	mcpTicketSearchSchema = `{"type":"object","additionalProperties":false,"properties":{
		"status":{"type":"string","maxLength":32},
		"queueId":{"type":"string","maxLength":64},
		"customerId":{"type":"string","maxLength":64},
		"updatedSince":{"type":"string","description":"RFC 3339; only tickets changed since"},
		"cursor":{"type":"string","maxLength":256},
		"pageSize":{"type":"integer","minimum":1,"maximum":100}}}`
	mcpTicketSchema = `{"type":"object","additionalProperties":false,"required":["ticketId"],
		"properties":{"ticketId":{"type":"string","maxLength":64}}}`
	mcpAudienceSchema = `{"type":"object","additionalProperties":false,"required":["filters"],
		"properties":{"filters":{"type":"object","description":"A segment's filter set, as saved segments carry it"}}}`
)

func (handlers *AdminHandlers) mcpServerTools() []mcpserver.Tool {
	operations := handlers.operations
	return []mcpserver.Tool{
		{
			Name: "support_tickets_search", Permission: string(rbac.PermissionSupportRead),
			Description: "Lists support tickets, newest activity first, by status, queue, customer, or last change.",
			InputSchema: json.RawMessage(mcpTicketSearchSchema),
			Run: func(ctx context.Context, call mcpserver.Call) (mcpserver.Result, error) {
				page, err := operations.SearchTickets(ctx, panelpg.TicketFilter{
					Status: argString(call, "status"), QueueID: argString(call, "queueId"),
					CustomerID: argString(call, "customerId"), UpdatedSince: argTime(call, "updatedSince"),
					Cursor: argString(call, "cursor"), PageSize: int32(argInt(call, "pageSize")),
				})
				return mcpResult(page, err)
			},
		},
		{
			Name: "support_ticket_get", Permission: string(rbac.PermissionSupportRead),
			Description: "Reads one support ticket with its messages and internal notes.",
			InputSchema: json.RawMessage(mcpTicketSchema),
			Run: func(ctx context.Context, call mcpserver.Call) (mcpserver.Result, error) {
				detail, err := operations.Ticket(ctx, argString(call, "ticketId"))
				return mcpResult(detail, err)
			},
		},
		{
			Name: "sales_report", Permission: string(rbac.PermissionFinanceRead),
			Description: "Sales for a period: by operation, plan, and day, refunds, trials, returning payers, and the previous bot's payments.",
			InputSchema: json.RawMessage(mcpPeriodSchema),
			Run: func(ctx context.Context, call mcpserver.Call) (mcpserver.Result, error) {
				since, until, timezone, currency := mcpPeriod(call)
				report, err := operations.SalesReport(ctx, since, until, timezone, currency)
				return mcpResult(report, err)
			},
		},
		{
			Name: "payment_health", Permission: string(rbac.PermissionFinanceRead),
			Description: "Payment success and failure by provider for a period.",
			InputSchema: json.RawMessage(mcpPeriodSchema),
			Run: func(ctx context.Context, call mcpserver.Call) (mcpserver.Result, error) {
				since, until, timezone, _ := mcpPeriod(call)
				report, err := operations.PaymentHealth(ctx, since, until, timezone)
				return mcpResult(report, err)
			},
		},
		{
			Name: "audience_segments", Permission: string(rbac.PermissionMarketingRead),
			Description: "Lists saved audience segments with their size and the customers whose chat is closed.",
			Run: func(ctx context.Context, _ mcpserver.Call) (mcpserver.Result, error) {
				segments, err := operations.Segments(ctx)
				return mcpResult(map[string]any{"items": segments}, err)
			},
		},
		{
			Name: "audience_preview", Permission: string(rbac.PermissionMarketingRead),
			Description: "Counts the customers a filter set selects, without saving it, and lists up to a thousand of them.",
			InputSchema: json.RawMessage(mcpAudienceSchema),
			Run: func(ctx context.Context, call mcpserver.Call) (mcpserver.Result, error) {
				filters, _ := call.Arguments["filters"].(map[string]any)
				preview, err := operations.PreviewAudience(ctx, filters)
				return mcpResult(preview, err)
			},
		},
	}
}

// mcpResult renders a service answer as the tool's text and structured
// result.
func mcpResult(value any, err error) (mcpserver.Result, error) {
	if err != nil {
		if errors.Is(err, panelpg.ErrNotFound) || errors.Is(err, panelpg.ErrValidaton) {
			return mcpserver.Result{Text: err.Error(), IsError: true}, nil
		}
		return mcpserver.Result{}, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return mcpserver.Result{}, err
	}
	var structured map[string]any
	_ = json.Unmarshal(encoded, &structured)
	return mcpserver.Result{Text: string(encoded), Structured: structured}, nil
}

func argString(call mcpserver.Call, name string) string {
	value, _ := call.Arguments[name].(string)
	return value
}

func argInt(call mcpserver.Call, name string) int {
	value, _ := call.Arguments[name].(float64)
	return int(value)
}

func argTime(call mcpserver.Call, name string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, argString(call, name))
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}

func mcpPeriod(call mcpserver.Call) (since, until time.Time, timezone, currency string) {
	until = time.Now().UTC()
	if parsed := argTime(call, "until"); parsed != nil {
		until = *parsed
	}
	since = until.Add(-defaultReportWindow)
	if parsed := argTime(call, "since"); parsed != nil {
		since = *parsed
	}
	timezone = argString(call, "timezone")
	if timezone == "" {
		timezone = "UTC"
	}
	return since, until, timezone, strings.ToUpper(argString(call, "currency"))
}
