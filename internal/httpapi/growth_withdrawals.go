package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/rbac"
	"github.com/omniflow/omniflow/internal/referralpayout"
	"github.com/omniflow/omniflow/internal/walletpost"
)

// mountWithdrawals registers the operator's withdrawal queue. Reading the
// queue is finance.read; deciding, revealing payout details, and changing the
// policy move or expose money and need finance.write.
func (handlers *AdminHandlers) mountWithdrawals(secure chi.Router) {
	if handlers.growth.Withdrawals == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionFinanceRead)).Group(func(read chi.Router) {
		read.Get("/finance/withdrawals", handlers.withdrawals)
		read.Get("/finance/withdrawals/settings", handlers.withdrawalSettings)
	})
	secure.With(handlers.requirePermission(rbac.PermissionFinanceWrite)).Group(func(write chi.Router) {
		write.Put("/finance/withdrawals/settings", handlers.saveWithdrawalSettings)
		write.Post("/finance/withdrawals/{withdrawalID}/details", handlers.revealWithdrawalDetails)
		write.Post("/finance/withdrawals/{withdrawalID}/approve", handlers.decideWithdrawal("approve"))
		write.Post("/finance/withdrawals/{withdrawalID}/reject", handlers.decideWithdrawal("reject"))
		write.Post("/finance/withdrawals/{withdrawalID}/paid", handlers.decideWithdrawal("paid"))
	})
}

func (handlers *AdminHandlers) withdrawals(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.growth.Withdrawals.List(request.Context(), query(request, "status"), int(queryInt(request, "limit")))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) withdrawalSettings(writer http.ResponseWriter, request *http.Request) {
	settings, err := handlers.growth.Withdrawals.Settings(request.Context())
	handlers.respond(writer, request, settings, err)
}

func (handlers *AdminHandlers) saveWithdrawalSettings(writer http.ResponseWriter, request *http.Request) {
	var body referralpayout.Settings
	if !decodeJSON(writer, request, &body) {
		return
	}
	settings, err := handlers.growth.Withdrawals.SaveSettings(request.Context(), body, actorFrom(request))
	handlers.withdrawalResponse(writer, request, settings, err)
}

func (handlers *AdminHandlers) revealWithdrawalDetails(writer http.ResponseWriter, request *http.Request) {
	details, err := handlers.growth.Withdrawals.RevealDetails(
		request.Context(), chi.URLParam(request, "withdrawalID"), actorFrom(request))
	handlers.withdrawalResponse(writer, request, map[string]string{"details": details}, err)
}

func (handlers *AdminHandlers) decideWithdrawal(decision string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Note string `json:"note"`
		}
		if !decodeJSON(writer, request, &body) {
			return
		}
		id, actor, service := chi.URLParam(request, "withdrawalID"), actorFrom(request), handlers.growth.Withdrawals
		var err error
		switch decision {
		case "approve":
			err = service.Approve(request.Context(), id, body.Note, actor)
		case "reject":
			err = service.Reject(request.Context(), id, body.Note, actor)
		default:
			err = service.MarkPaid(request.Context(), id, body.Note, actor)
		}
		handlers.withdrawalResponse(writer, request, map[string]any{"ok": true}, err)
	}
}

func (handlers *AdminHandlers) withdrawalResponse(writer http.ResponseWriter, request *http.Request, value any, err error) {
	switch {
	case errors.Is(err, referralpayout.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That request does not exist")
	case errors.Is(err, referralpayout.ErrState):
		writeProblem(writer, request, http.StatusConflict, "state_conflict", "That request has already been decided")
	case errors.Is(err, referralpayout.ErrInvalid):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_request", "Those details are not valid, or a reason is required")
	default:
		handlers.respond(writer, request, value, err)
	}
}

// mountAccountWithdrawals registers the customer's side.
func (handlers *AccountHandlers) mountAccountWithdrawals(secure chi.Router) {
	if handlers.growth.Withdrawals == nil {
		return
	}
	secure.Get("/referrals/withdrawals", handlers.withdrawalSummary)
	secure.Post("/referrals/withdrawals", handlers.requestWithdrawal)
	secure.Delete("/referrals/withdrawals/{withdrawalID}", handlers.cancelWithdrawal)
}

func (handlers *AccountHandlers) withdrawalSummary(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	summary, err := handlers.growth.Withdrawals.Summary(request.Context(), principal.Customer.ID)
	if err != nil {
		handlers.logger.Error("withdrawal summary failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "withdrawals_unavailable", "Withdrawals are unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, summary)
}

func (handlers *AccountHandlers) requestWithdrawal(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	key := idempotencyKey(request)
	if key == "" {
		writeProblem(writer, request, http.StatusBadRequest, "idempotency_key_required", "A withdrawal requires an Idempotency-Key header")
		return
	}
	if !handlers.allowPrivacyRate(request, "account_withdrawal", principal.Customer.ID, 10, time.Hour) {
		writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again later")
		return
	}
	var body struct {
		AmountMinor int64  `json:"amountMinor"`
		Method      string `json:"method"`
		Details     string `json:"details"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	created, err := handlers.growth.Withdrawals.RequestWithdrawal(request.Context(), principal.Customer.ID, key,
		referralpayout.Input{AmountMinor: body.AmountMinor, Method: body.Method, Details: body.Details})
	if handlers.writeWithdrawalError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, created)
}

func (handlers *AccountHandlers) cancelWithdrawal(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	err := handlers.growth.Withdrawals.Cancel(request.Context(), principal.Customer.ID, chi.URLParam(request, "withdrawalID"))
	if handlers.writeWithdrawalError(writer, request, err) {
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AccountHandlers) writeWithdrawalError(writer http.ResponseWriter, request *http.Request, err error) bool {
	if err == nil {
		return false
	}
	for _, mapping := range []struct {
		target error
		status int
		code   string
		detail string
	}{
		{referralpayout.ErrDisabled, http.StatusNotFound, "withdrawals_disabled", "Withdrawals are not available"},
		{referralpayout.ErrBelowMinimum, http.StatusUnprocessableEntity, "below_minimum", "The amount is below the minimum"},
		{referralpayout.ErrInsufficient, http.StatusUnprocessableEntity, "insufficient_balance", "That is more than can be withdrawn"},
		{walletpost.ErrInsufficient, http.StatusUnprocessableEntity, "insufficient_balance", "That is more than can be withdrawn"},
		{referralpayout.ErrCooldown, http.StatusConflict, "cooldown", "The next request is not allowed yet"},
		{referralpayout.ErrPending, http.StatusConflict, "request_pending", "A request is already being reviewed"},
		{referralpayout.ErrInvalid, http.StatusUnprocessableEntity, "invalid_request", "Check the amount, method, and details"},
		{referralpayout.ErrNotFound, http.StatusNotFound, "not_found", "That request does not exist"},
	} {
		if errors.Is(err, mapping.target) {
			writeProblem(writer, request, mapping.status, mapping.code, mapping.detail)
			return true
		}
	}
	handlers.logger.Error("withdrawal request failed", "error", err)
	writeProblem(writer, request, http.StatusInternalServerError, "withdrawal_failed", "The request could not be completed; try again")
	return true
}
