package httpapi

import (
	"errors"
	"net/http"

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
	})
	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Put("/marketing/referral-levels", handlers.saveReferralLevels)
		write.Post("/marketing/referral-contests", handlers.createContest)
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

func (handlers *AdminHandlers) referralPlusResponse(writer http.ResponseWriter, request *http.Request, value any, err error) {
	switch {
	case errors.Is(err, referralplus.ErrInvalid):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_request", "Those details are not valid")
	case errors.Is(err, referralplus.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That contest does not exist")
	case errors.Is(err, referralplus.ErrState):
		writeProblem(writer, request, http.StatusConflict, "state_conflict", "The contest has not ended, or was already finished")
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
