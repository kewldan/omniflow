package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/omniflow/omniflow/internal/analytics"
	"github.com/omniflow/omniflow/internal/rbac"
)

// WithAnalytics attaches the business analytics service to the panel. A nil
// service leaves its routes unmounted.
func (handlers *AdminHandlers) WithAnalytics(service *analytics.Service) *AdminHandlers {
	handlers.businessAnalytics = service
	return handlers
}

// WithAnalytics attaches the service that records the purchase funnel's
// steps on the customer panel. Without it nothing is recorded.
func (handlers *AccountHandlers) WithAnalytics(service *analytics.Service) *AccountHandlers {
	handlers.businessAnalytics = service
	return handlers
}

// mountBusinessAnalytics registers the subscription, acquisition, traffic
// history, and support satisfaction reports.
//
// Money sits behind `finance.read` like the sales report, and recording an
// advertising cost behind `finance.write`. Traffic history names nodes only,
// but it sits beside the traffic report and shares its `customers.read`.
// Satisfaction is the support desk's figure and reads with `support.read`.
func (handlers *AdminHandlers) mountBusinessAnalytics(secure chi.Router) {
	if handlers.businessAnalytics == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionFinanceRead)).Group(func(read chi.Router) {
		read.Get("/reports/subscriptions", handlers.subscriptionReport)
		read.Get("/reports/acquisition", handlers.acquisitionReport)
		read.Get("/reports/acquisition/spend", handlers.listAcquisitionSpend)
	})
	secure.With(handlers.requirePermission(rbac.PermissionFinanceWrite)).Group(func(write chi.Router) {
		write.Post("/reports/acquisition/spend", handlers.createAcquisitionSpend)
		write.Delete("/reports/acquisition/spend/{spendID}", handlers.deleteAcquisitionSpend)
	})
	secure.With(handlers.requirePermission(rbac.PermissionCustomersRead)).
		Get("/reports/traffic/history", handlers.trafficHistory)
	secure.With(handlers.requirePermission(rbac.PermissionSupportRead)).
		Get("/reports/support-satisfaction", handlers.supportSatisfaction)
}

func analyticsPeriod(request *http.Request) (analytics.Period, error) {
	since, until, timezone, _ := reportPeriod(request)
	return analytics.NewPeriod(since, until, timezone)
}

func (handlers *AdminHandlers) subscriptionReport(writer http.ResponseWriter, request *http.Request) {
	period, err := analyticsPeriod(request)
	if err != nil {
		handlers.analyticsError(writer, request, err)
		return
	}
	report, err := handlers.businessAnalytics.SubscriptionReport(request.Context(), period)
	handlers.analyticsRespond(writer, request, report, err)
}

func (handlers *AdminHandlers) acquisitionReport(writer http.ResponseWriter, request *http.Request) {
	period, err := analyticsPeriod(request)
	if err != nil {
		handlers.analyticsError(writer, request, err)
		return
	}
	report, err := handlers.businessAnalytics.AcquisitionReport(request.Context(), period)
	handlers.analyticsRespond(writer, request, report, err)
}

func (handlers *AdminHandlers) listAcquisitionSpend(writer http.ResponseWriter, request *http.Request) {
	period, err := analyticsPeriod(request)
	if err != nil {
		handlers.analyticsError(writer, request, err)
		return
	}
	items, err := handlers.businessAnalytics.ListSpend(request.Context(), period)
	handlers.analyticsRespond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) createAcquisitionSpend(writer http.ResponseWriter, request *http.Request) {
	var body analytics.SpendInput
	if !decodeJSON(writer, request, &body) {
		return
	}
	actor := analyticsActor(request)
	actor.IdempotencyKey = request.Header.Get("Idempotency-Key")
	spend, err := handlers.businessAnalytics.CreateSpend(request.Context(), actor, body)
	if err != nil {
		handlers.analyticsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, spend)
}

func (handlers *AdminHandlers) deleteAcquisitionSpend(writer http.ResponseWriter, request *http.Request) {
	err := handlers.businessAnalytics.DeleteSpend(
		request.Context(), analyticsActor(request), chi.URLParam(request, "spendID"))
	if err != nil {
		handlers.analyticsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) trafficHistory(writer http.ResponseWriter, request *http.Request) {
	period, err := analyticsPeriod(request)
	if err != nil {
		handlers.analyticsError(writer, request, err)
		return
	}
	history, err := handlers.businessAnalytics.TrafficHistory(request.Context(), period)
	handlers.analyticsRespond(writer, request, history, err)
}

func (handlers *AdminHandlers) supportSatisfaction(writer http.ResponseWriter, request *http.Request) {
	period, err := analyticsPeriod(request)
	if err != nil {
		handlers.analyticsError(writer, request, err)
		return
	}
	result, err := handlers.businessAnalytics.SupportSatisfaction(request.Context(), period)
	handlers.analyticsRespond(writer, request, result, err)
}

func analyticsActor(request *http.Request) analytics.Actor {
	actor := actorFrom(request)
	return analytics.Actor{
		AdminID: actor.AdminID, RequestID: middleware.GetReqID(request.Context()), Reason: actor.Reason,
	}
}

func (handlers *AdminHandlers) analyticsRespond(writer http.ResponseWriter, request *http.Request, value any, err error) {
	if err != nil {
		handlers.analyticsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (handlers *AdminHandlers) analyticsError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, analytics.ErrInvalid):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_request", err.Error())
	case errors.Is(err, analytics.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That record does not exist")
	default:
		handlers.logger.Error("analytics request failed", "path", request.URL.Path, "error", err)
		writeProblem(writer, request, http.StatusInternalServerError,
			"operation_failed", "That operation could not be completed")
	}
}

// recordFunnel notes a funnel step for the signed-in customer. It never fails
// the request it rides on: a lost funnel step is a slightly low figure, and a
// failed checkout is a lost sale.
func (handlers *AccountHandlers) recordFunnel(ctx context.Context, customerID, step string) {
	if handlers.businessAnalytics == nil {
		return
	}
	if err := handlers.businessAnalytics.RecordFunnelStep(ctx, customerID, step, analytics.ChannelWeb); err != nil {
		handlers.logger.Warn("funnel step not recorded", "step", step, "error", err)
	}
}
