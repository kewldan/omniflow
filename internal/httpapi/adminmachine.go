package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// MountMachine serves a set of the panel's own handlers under /v1/admin, to
// named API tokens, so an automation can do what an operator does in the
// panel without a browser session: read and annotate tickets, pull reports,
// estimate an audience and draft a broadcast, run a bulk action.
//
// The handler is the panel's, so the rules are the panel's: the request acts
// as the operator who made the token, with that operator's roles narrowed to
// the token's permission list, and every state change is audited under their
// name as well as under the token's. The shared APP_OPERATOR_TOKEN names no
// operator and is refused here; it keeps the routes it always had.
func (handlers *AdminHandlers) MountMachine(admin chi.Router) {
	if handlers.operations == nil {
		return
	}
	admin.Group(func(machine chi.Router) {
		machine.Use(handlers.tokenPrincipal)

		machine.With(handlers.requirePermission(rbac.PermissionSupportRead)).Group(func(read chi.Router) {
			read.Get("/support/tickets", handlers.searchTickets)
			read.Get("/support/tickets/{ticketID}", handlers.supportTicket)
		})
		machine.With(handlers.requirePermission(rbac.PermissionSupportWrite)).Group(func(write chi.Router) {
			write.Post("/support/tickets/{ticketID}/notes", handlers.addTicketNote)
			write.Post("/support/tickets/{ticketID}/status", handlers.setTicketStatus)
		})

		machine.With(handlers.requirePermission(rbac.PermissionFinanceRead)).Group(func(read chi.Router) {
			read.Get("/reports/sales", handlers.salesReport)
			read.Get("/reports/sales/export", handlers.exportSalesReport)
			read.Get("/reports/payments", handlers.paymentHealth)
			read.Get("/reports/customer-payments", handlers.customerPayments)
		})

		machine.With(handlers.requirePermission(rbac.PermissionMarketingRead)).Group(func(read chi.Router) {
			read.Get("/marketing/segments", handlers.audienceSegments)
			read.Get("/marketing/templates", handlers.messageTemplates)
			read.Get("/marketing/campaigns", handlers.campaigns)
			read.Post("/marketing/audience/preview", handlers.previewAudience)
		})
		machine.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
			write.Put("/marketing/segments", handlers.saveSegment)
			write.Post("/marketing/campaigns", handlers.createCampaign)
			write.Post("/marketing/campaigns/{campaignID}/state", handlers.setCampaignState)
		})

		machine.With(handlers.requirePermission(rbac.PermissionCustomersRead)).Group(func(read chi.Router) {
			read.Get("/bulk", handlers.listBulkOperations)
			read.Get("/bulk/{operationID}/items", handlers.bulkItems)
		})
		machine.With(handlers.requirePermission(rbac.PermissionCustomersWrite)).Group(func(write chi.Router) {
			write.Post("/bulk", handlers.previewBulk)
			write.Post("/bulk/{operationID}/start", handlers.startBulk)
			write.Post("/bulk/{operationID}/cancel", handlers.cancelBulk)
		})
	})
}

// tokenPrincipal turns the named token on a /v1/admin request into the
// principal the panel's handlers expect.
func (handlers *AdminHandlers) tokenPrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, createdBy, permissions, fullAccess, ok := APITokenFrom(request.Context())
		if !ok {
			writeProblem(writer, request, http.StatusForbidden, "named_token_required",
				"This route acts as an operator; call it with a named API token from the panel")
			return
		}
		principal, err := handlers.service.PrincipalForAPIToken(request.Context(), createdBy, permissions, fullAccess)
		if errors.Is(err, adminauthpg.ErrTokenOwnerInactive) {
			writeProblem(writer, request, http.StatusForbidden, "token_owner_inactive",
				"The operator who made this token is no longer active; make a new token")
			return
		}
		if err != nil {
			handlers.operationsError(writer, request, err)
			return
		}
		ctx := context.WithValue(request.Context(), principalContextKey{}, principal)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// previewAudience compiles a filter set without saving it and answers its
// size and the customers it selects.
func (handlers *AdminHandlers) previewAudience(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Filters map[string]any `json:"filters"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	preview, err := handlers.operations.PreviewAudience(request.Context(), body.Filters)
	handlers.respond(writer, request, preview, err)
}

// customerPayments lists each customer who paid in the period, as JSON or,
// with format=csv, as a file with a stable header.
func (handlers *AdminHandlers) customerPayments(writer http.ResponseWriter, request *http.Request) {
	since, until, _, currency := reportPeriod(request)
	rows, err := handlers.operations.CustomerPaymentsReport(request.Context(), since, until, currency)
	if err != nil || query(request, "format") != "csv" {
		handlers.respond(writer, request, map[string]any{"customers": rows}, err)
		return
	}
	writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="customer-payments.csv"`)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("customer_id,currency,payments,amount_minor,legacy_payments," +
		"legacy_amount_minor,first_paid_at,last_paid_at\n"))
	for _, row := range rows {
		fields := []string{
			row.CustomerID, row.Currency,
			strconv.FormatInt(row.Payments, 10), strconv.FormatInt(row.AmountMinor, 10),
			strconv.FormatInt(row.LegacyPayments, 10), strconv.FormatInt(row.LegacyAmountMinor, 10),
			row.FirstPaidAt.Format("2006-01-02T15:04:05Z"), row.LastPaidAt.Format("2006-01-02T15:04:05Z"),
		}
		for index, field := range fields {
			fields[index] = csvField(field)
		}
		_, _ = writer.Write([]byte(strings.Join(fields, ",") + "\n"))
	}
}
