package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/engagement"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountGames registers the daily games in the panel. Reading is
// marketing.read; configuring a game and writing questions marketing.write,
// as the wheel's prize table is.
func (handlers *AdminHandlers) mountGames(secure chi.Router) {
	if handlers.growth.Engagement == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).Get("/marketing/games", handlers.games)
	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Put("/marketing/games/{code}", handlers.saveGame)
		write.Post("/marketing/games/questions", handlers.createGameQuestion)
		write.Post("/marketing/games/questions/{questionID}/archive", handlers.archiveGameQuestion)
	})
}

func (handlers *AdminHandlers) games(writer http.ResponseWriter, request *http.Request) {
	games, err := handlers.growth.Engagement.GameConfigs(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	questions, err := handlers.growth.Engagement.GameQuestions(request.Context())
	handlers.respond(writer, request, map[string]any{"games": games, "questions": questions}, err)
}

func (handlers *AdminHandlers) saveGame(writer http.ResponseWriter, request *http.Request) {
	var body engagement.GameConfig
	if !decodeJSON(writer, request, &body) {
		return
	}
	body.Code = chi.URLParam(request, "code")
	err := handlers.growth.Engagement.SaveGame(request.Context(), body, actorFrom(request))
	handlers.engagementResponse(writer, request, map[string]bool{"ok": true}, err)
}

func (handlers *AdminHandlers) createGameQuestion(writer http.ResponseWriter, request *http.Request) {
	var body engagement.GameQuestion
	if !decodeJSON(writer, request, &body) {
		return
	}
	question, err := handlers.growth.Engagement.CreateGameQuestion(request.Context(), body, actorFrom(request))
	handlers.engagementResponse(writer, request, question, err)
}

func (handlers *AdminHandlers) archiveGameQuestion(writer http.ResponseWriter, request *http.Request) {
	err := handlers.growth.Engagement.ArchiveGameQuestion(request.Context(), chi.URLParam(request, "questionID"), actorFrom(request))
	handlers.engagementResponse(writer, request, map[string]bool{"ok": true}, err)
}

// mountAccountGames registers the customer's daily games.
func (handlers *AccountHandlers) mountAccountGames(secure chi.Router) {
	if handlers.growth.Engagement == nil {
		return
	}
	secure.Get("/engagement/games", handlers.accountGames)
	secure.Post("/engagement/games/{code}/play", handlers.playGame)
}

func (handlers *AccountHandlers) accountGames(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	games, recent, err := handlers.growth.Engagement.CustomerGames(request.Context(), principal.Customer.ID)
	handlers.writeGame(writer, request, http.StatusOK, map[string]any{"items": games, "recent": recent}, err)
}

func (handlers *AccountHandlers) playGame(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if !handlers.allowPrivacyRate(request, "game_play", principal.Customer.ID, 60, time.Hour) {
		writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many plays; try again later")
		return
	}
	var body engagement.PlayInput
	if !decodeJSON(writer, request, &body) {
		return
	}
	play, err := handlers.growth.Engagement.PlayGame(request.Context(), principal.Customer.ID,
		chi.URLParam(request, "code"), body, idempotencyKey(request))
	handlers.writeGame(writer, request, http.StatusCreated, play, err)
}

func (handlers *AccountHandlers) writeGame(writer http.ResponseWriter, request *http.Request, status int, value any, err error) {
	for _, mapping := range []struct {
		target error
		status int
		code   string
		detail string
	}{
		{engagement.ErrGameDisabled, http.StatusNotFound, "game_disabled", "That game is not available"},
		{engagement.ErrNoQuestion, http.StatusNotFound, "no_question", "There is no question to answer now"},
		{engagement.ErrLimit, http.StatusConflict, "play_limit", "No plays are left today"},
		{engagement.ErrGameClosed, http.StatusConflict, "game_closed", "The game has given out today's prizes"},
		{engagement.ErrQuestionChanged, http.StatusConflict, "question_changed", "The question has changed; open the game again"},
		{engagement.ErrAnswered, http.StatusConflict, "already_answered", "You have already answered this question"},
		{engagement.ErrPrizeHeld, http.StatusConflict, "prize_held", "You already hold this game's offer; use it first"},
		{engagement.ErrTooNew, http.StatusConflict, "account_too_new", "The account is too new for this"},
		{engagement.ErrInsufficient, http.StatusUnprocessableEntity, "insufficient_balance", "The balance does not cover the entry"},
		{engagement.ErrInvalid, http.StatusUnprocessableEntity, "invalid_request", "That request is not valid"},
	} {
		if errors.Is(err, mapping.target) {
			writeProblem(writer, request, mapping.status, mapping.code, mapping.detail)
			return
		}
	}
	if err != nil {
		handlers.logger.Error("daily game request failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "game_failed", "Try again later")
		return
	}
	writeJSON(writer, status, value)
}
