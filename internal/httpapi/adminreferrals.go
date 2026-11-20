package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountReferralAdmin registers Bedolaga's referral administration: top
// referrers, lost-referral diagnostics with back-crediting, manual attach and
// detach, and personal commission rates.
func (handlers *AdminHandlers) mountReferralAdmin(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).Group(func(read chi.Router) {
		read.Get("/referrals/top", handlers.topReferrers)
		read.Get("/referrals/lost", handlers.lostReferrals)
		read.Get("/customers/{customerID}/referral-terms", handlers.referralTerms)
	})
	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Post("/referrals/lost/{customerID}/credit", handlers.backCreditReferral)
		write.Post("/customers/{customerID}/referrer", handlers.attachReferrer)
		write.Delete("/customers/{customerID}/referrer", handlers.detachReferrer)
		write.Put("/customers/{customerID}/referral-terms", handlers.saveReferralTerms)
	})
}

func (handlers *AdminHandlers) topReferrers(writer http.ResponseWriter, request *http.Request) {
	top, err := handlers.operations.TopReferrers(request.Context(), int(queryInt(request, "days")), int32(queryInt(request, "pageSize")))
	handlers.respond(writer, request, map[string]any{"items": top}, err)
}

func (handlers *AdminHandlers) lostReferrals(writer http.ResponseWriter, request *http.Request) {
	lost, err := handlers.operations.LostReferrals(request.Context(), int32(queryInt(request, "pageSize")))
	handlers.respond(writer, request, map[string]any{"items": lost}, err)
}

// backCreditReferral grants the rewards a lost referral missed.
func (handlers *AdminHandlers) backCreditReferral(writer http.ResponseWriter, request *http.Request) {
	if handlers.growth.Referrals == nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "referrals_unavailable", "Back-crediting is not configured")
		return
	}
	err := handlers.growth.Referrals.BackCreditReferral(request.Context(), chi.URLParam(request, "customerID"))
	switch {
	case errors.Is(err, commercepg.ErrReferralNotLost):
		writeProblem(writer, request, http.StatusConflict, "referral_not_lost", err.Error())
	case errors.Is(err, commercepg.ErrReferralNothingGranted):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "referral_nothing_granted", err.Error())
	default:
		handlers.respond(writer, request, map[string]any{"credited": err == nil}, err)
	}
}

func (handlers *AdminHandlers) attachReferrer(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.AttachReferrer(request.Context(), chi.URLParam(request, "customerID"), body.Code, actorFrom(request))
	handlers.respond(writer, request, map[string]any{"attached": err == nil}, err)
}

func (handlers *AdminHandlers) detachReferrer(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DetachReferrer(request.Context(), chi.URLParam(request, "customerID"), actorFrom(request))
	if errors.Is(err, panelpg.ErrReferralLocked) {
		writeProblem(writer, request, http.StatusConflict, "referral_locked",
			"This referral has paid out; reject it in review to reverse the rewards")
		return
	}
	handlers.respond(writer, request, map[string]any{"detached": err == nil}, err)
}

func (handlers *AdminHandlers) referralTerms(writer http.ResponseWriter, request *http.Request) {
	terms, err := handlers.operations.ReferralInviterTerms(request.Context(), chi.URLParam(request, "customerID"))
	handlers.respond(writer, request, terms, err)
}

func (handlers *AdminHandlers) saveReferralTerms(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.InviterTerms
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.SaveReferralInviterTerms(request.Context(), chi.URLParam(request, "customerID"), body, actorFrom(request))
	handlers.respond(writer, request, body, err)
}
