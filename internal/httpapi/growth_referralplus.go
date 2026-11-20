package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/rbac"
	"github.com/omniflow/omniflow/internal/referralplus"
)

// mountReferralPlus registers the second referral level and contests.
// Configuring them is marketing.write; finishing a contest pays its prizes,
// so it needs finance.write.
func (handlers *AdminHandlers) mountReferralPlus(secure chi.Router) {
	if handlers.growth.ReferralPlus == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).Group(func(read chi.Router) {
		read.Get("/marketing/referral-levels", handlers.referralLevels)
		read.Get("/marketing/referral-contests/{contestID}/leaderboard", handlers.contestLeaderboard)
		read.Get("/marketing/partner-applications", handlers.partnerApplications)
		read.Get("/customers/{customerID}/referral-network", handlers.referralNetwork)
	})
	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Put("/marketing/referral-levels", handlers.saveReferralLevels)
		write.Post("/marketing/referral-contests", handlers.createContest)
		// Approving sets a personal commission rate, which the customer card
		// already lets marketing.write set directly.
		write.Post("/marketing/partner-applications/{applicationID}/decision", handlers.decidePartner)
	})
	secure.With(handlers.requirePermission(rbac.PermissionFinanceWrite)).
		Post("/marketing/referral-contests/{contestID}/finish", handlers.finishContest)
}

func (handlers *AdminHandlers) referralLevels(writer http.ResponseWriter, request *http.Request) {
	settings, err := handlers.growth.ReferralPlus.LevelSettings(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	contests, err := handlers.growth.ReferralPlus.Contests(request.Context(), false)
	handlers.respond(writer, request, map[string]any{"level2": settings, "contests": contests}, err)
}

func (handlers *AdminHandlers) saveReferralLevels(writer http.ResponseWriter, request *http.Request) {
	var body referralplus.LevelSettings
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.growth.ReferralPlus.SaveLevelSettings(request.Context(), body, actorFrom(request))
	handlers.referralPlusResponse(writer, request, body, err)
}

func (handlers *AdminHandlers) createContest(writer http.ResponseWriter, request *http.Request) {
	var body referralplus.Contest
	if !decodeJSON(writer, request, &body) {
		return
	}
	contest, err := handlers.growth.ReferralPlus.CreateContest(request.Context(), body, actorFrom(request))
	handlers.referralPlusResponse(writer, request, contest, err)
}

func (handlers *AdminHandlers) contestLeaderboard(writer http.ResponseWriter, request *http.Request) {
	standings, err := handlers.growth.ReferralPlus.Leaderboard(request.Context(), chi.URLParam(request, "contestID"), 50)
	handlers.referralPlusResponse(writer, request, map[string]any{"items": standings}, err)
}

func (handlers *AdminHandlers) finishContest(writer http.ResponseWriter, request *http.Request) {
	winners, err := handlers.growth.ReferralPlus.FinishContest(request.Context(), chi.URLParam(request, "contestID"), actorFrom(request))
	handlers.referralPlusResponse(writer, request, map[string]any{"items": winners}, err)
}

func (handlers *AdminHandlers) partnerApplications(writer http.ResponseWriter, request *http.Request) {
	list, err := handlers.growth.ReferralPlus.PartnerApplications(request.Context(), request.URL.Query().Get("status"),
		int(queryInt(request, "pageSize")))
	handlers.referralPlusResponse(writer, request, map[string]any{"items": list}, err)
}

func (handlers *AdminHandlers) decidePartner(writer http.ResponseWriter, request *http.Request) {
	var body referralplus.PartnerDecision
	if !decodeJSON(writer, request, &body) {
		return
	}
	application, err := handlers.growth.ReferralPlus.DecidePartner(request.Context(),
		chi.URLParam(request, "applicationID"), body, actorFrom(request))
	handlers.referralPlusResponse(writer, request, application, err)
}

func (handlers *AdminHandlers) referralNetwork(writer http.ResponseWriter, request *http.Request) {
	network, err := handlers.growth.ReferralPlus.ReferralNetwork(request.Context(), chi.URLParam(request, "customerID"),
		int(queryInt(request, "pageSize")))
	handlers.referralPlusResponse(writer, request, network, err)
}

func (handlers *AdminHandlers) referralPlusResponse(writer http.ResponseWriter, request *http.Request, value any, err error) {
	switch {
	case errors.Is(err, referralplus.ErrInvalid):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_request", "Those details are not valid")
	case errors.Is(err, referralplus.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That contest does not exist")
	case errors.Is(err, referralplus.ErrState):
		writeProblem(writer, request, http.StatusConflict, "state_conflict", "The contest has not ended, or was already finished")
	case errors.Is(err, referralplus.ErrApplicationNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That application or customer does not exist")
	case errors.Is(err, referralplus.ErrApplicationDecided):
		writeProblem(writer, request, http.StatusConflict, "already_decided", "That application was already decided")
	default:
		handlers.respond(writer, request, value, err)
	}
}

// mountAccountReferralPlus lets a customer see running contests and their place.
func (handlers *AccountHandlers) mountAccountReferralPlus(secure chi.Router) {
	if handlers.growth.ReferralPlus == nil {
		return
	}
	secure.Get("/referrals/contests", handlers.accountContests)
	secure.Get("/referrals/partner", handlers.accountPartner)
	secure.Post("/referrals/partner", handlers.applyPartner)
}

func (handlers *AccountHandlers) accountPartner(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	state, err := handlers.growth.ReferralPlus.PartnerStatus(request.Context(), principal.Customer.ID)
	if err != nil {
		handlers.logger.Error("partner status failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "partner_unavailable", "Try again later")
		return
	}
	writeJSON(writer, http.StatusOK, state)
}

// applyPartner records the customer's partner application. The customer's
// words go to the operator only; they are never shown to anyone else.
func (handlers *AccountHandlers) applyPartner(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if !handlers.allowPrivacyRate(request, "partner_apply", principal.Customer.ID, 5, time.Hour) {
		writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many applications; try again later")
		return
	}
	var body struct {
		Channel string `json:"channel"`
		Message string `json:"message"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	application, err := handlers.growth.ReferralPlus.ApplyPartner(request.Context(), principal.Customer.ID,
		referralplus.SourceWeb, body.Channel, body.Message)
	switch {
	case errors.Is(err, referralplus.ErrInvalid):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_request", "The application is too long")
	case errors.Is(err, referralplus.ErrPartnerPending):
		writeProblem(writer, request, http.StatusConflict, "application_pending", "An application is already waiting for a decision")
	case errors.Is(err, referralplus.ErrAlreadyPartner):
		writeProblem(writer, request, http.StatusConflict, "already_partner", "You are already a partner")
	case err != nil:
		handlers.logger.Error("partner application failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "partner_unavailable", "Try again later")
	default:
		writeJSON(writer, http.StatusCreated, application)
	}
}

func (handlers *AccountHandlers) accountContests(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	contests, err := handlers.growth.ReferralPlus.Contests(request.Context(), true)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "contests_unavailable", "Contests are unavailable")
		return
	}
	items := make([]map[string]any, 0, len(contests))
	for _, contest := range contests {
		standings, err := handlers.growth.ReferralPlus.CustomerView(request.Context(), principal.Customer.ID, contest.ID)
		if err != nil {
			writeProblem(writer, request, http.StatusInternalServerError, "contests_unavailable", "Contests are unavailable")
			return
		}
		items = append(items, map[string]any{"contest": contest, "standings": standings})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}
