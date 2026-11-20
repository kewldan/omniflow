package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/engagement"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountEngagement registers the wheel and polls in the panel. Reading is
// marketing.read; the prize table and polls are marketing.write.
func (handlers *AdminHandlers) mountEngagement(secure chi.Router) {
	if handlers.growth.Engagement == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).Get("/marketing/engagement", handlers.engagementState)
	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Put("/marketing/engagement/wheel", handlers.saveWheel)
		write.Post("/marketing/engagement/polls", handlers.createPoll)
		write.Post("/marketing/engagement/polls/{pollID}/close", handlers.closePoll)
	})
}

func (handlers *AdminHandlers) engagementState(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	settings, err := handlers.growth.Engagement.Settings(ctx)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	prizes, err := handlers.growth.Engagement.Prizes(ctx)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	active := []engagement.Prize{}
	for _, prize := range prizes {
		if prize.Active {
			active = append(active, prize)
		}
	}
	polls, err := handlers.growth.Engagement.Polls(ctx, "")
	handlers.respond(writer, request, map[string]any{
		"settings": settings, "prizes": active, "polls": polls,
		"rtpPercent": engagement.RTPPercent(active, settings.DayValueMinor, settings.SpinCostMinor),
	}, err)
}

func (handlers *AdminHandlers) saveWheel(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Settings engagement.Settings `json:"settings"`
		Prizes   []engagement.Prize  `json:"prizes"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.growth.Engagement.SaveWheel(request.Context(), body.Settings, body.Prizes, actorFrom(request))
	handlers.engagementResponse(writer, request, map[string]bool{"ok": true}, err)
}

func (handlers *AdminHandlers) createPoll(writer http.ResponseWriter, request *http.Request) {
	var body engagement.Poll
	if !decodeJSON(writer, request, &body) {
		return
	}
	poll, err := handlers.growth.Engagement.CreatePoll(request.Context(), body, actorFrom(request))
	handlers.engagementResponse(writer, request, poll, err)
}

func (handlers *AdminHandlers) closePoll(writer http.ResponseWriter, request *http.Request) {
	err := handlers.growth.Engagement.ClosePoll(request.Context(), chi.URLParam(request, "pollID"), actorFrom(request))
	handlers.engagementResponse(writer, request, map[string]bool{"ok": true}, err)
}

func (handlers *AdminHandlers) engagementResponse(writer http.ResponseWriter, request *http.Request, value any, err error) {
	switch {
	case errors.Is(err, engagement.ErrRTP):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "rtp_exceeded", "The prizes pay out more than the target return")
	case errors.Is(err, engagement.ErrInvalid):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_request", "Those details are not valid")
	case errors.Is(err, engagement.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That poll does not exist or is closed")
	default:
		handlers.respond(writer, request, value, err)
	}
}

// mountAccountEngagement registers the customer's wheel and polls.
func (handlers *AccountHandlers) mountAccountEngagement(secure chi.Router) {
	if handlers.growth.Engagement == nil {
		return
	}
	secure.Get("/engagement/wheel", handlers.wheel)
	secure.Post("/engagement/wheel/spin", handlers.spinWheel)
	secure.Get("/engagement/polls", handlers.polls)
	secure.Post("/engagement/polls/{pollID}/answer", handlers.answerPoll)
}

func (handlers *AccountHandlers) wheel(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	state, err := handlers.growth.Engagement.Wheel(request.Context(), principal.Customer.ID)
	handlers.writeEngagement(writer, request, http.StatusOK, state, err)
}

func (handlers *AccountHandlers) spinWheel(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if !handlers.allowPrivacyRate(request, "wheel_spin", principal.Customer.ID, 30, time.Hour) {
		writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many spins; try again later")
		return
	}
	spin, err := handlers.growth.Engagement.SpinWheel(request.Context(), principal.Customer.ID, idempotencyKey(request))
	handlers.writeEngagement(writer, request, http.StatusCreated, spin, err)
}

func (handlers *AccountHandlers) polls(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	polls, err := handlers.growth.Engagement.Polls(request.Context(), principal.Customer.ID)
	handlers.writeEngagement(writer, request, http.StatusOK, map[string]any{"items": polls}, err)
}

func (handlers *AccountHandlers) answerPoll(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		Option int `json:"option"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	rewarded, err := handlers.growth.Engagement.Answer(request.Context(), principal.Customer.ID, chi.URLParam(request, "pollID"), body.Option)
	handlers.writeEngagement(writer, request, http.StatusOK, map[string]bool{"rewarded": rewarded}, err)
}

func (handlers *AccountHandlers) writeEngagement(writer http.ResponseWriter, request *http.Request, status int, value any, err error) {
	for _, mapping := range []struct {
		target error
		status int
		code   string
		detail string
	}{
		{engagement.ErrDisabled, http.StatusNotFound, "wheel_disabled", "The wheel is not available"},
		{engagement.ErrLimit, http.StatusConflict, "spin_limit", "No spins are left today"},
		{engagement.ErrTooNew, http.StatusConflict, "account_too_new", "The account is too new for this"},
		{engagement.ErrInsufficient, http.StatusUnprocessableEntity, "insufficient_balance", "The balance does not cover a spin"},
		{engagement.ErrAnswered, http.StatusConflict, "already_answered", "You have already answered this poll"},
		{engagement.ErrClosed, http.StatusNotFound, "poll_closed", "That poll is closed"},
		{engagement.ErrInvalid, http.StatusUnprocessableEntity, "invalid_request", "That request is not valid"},
	} {
		if errors.Is(err, mapping.target) {
			writeProblem(writer, request, mapping.status, mapping.code, mapping.detail)
			return
		}
	}
	if err != nil {
		handlers.logger.Error("engagement request failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "engagement_failed", "Try again later")
		return
	}
	writeJSON(writer, status, value)
}
