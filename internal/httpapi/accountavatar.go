package httpapi

import (
	"errors"
	"net/http"

	"github.com/omniflow/omniflow/internal/customerauthpg"
)

// accountAvatar serves the signed-in customer their own Telegram profile
// photo, from the installation: Telegram's picture hosts are not reachable
// from every network. 404 for a customer without Telegram or a photo the bot
// can see; the panel then draws initials.
func (handlers *AccountHandlers) accountAvatar(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if handlers.avatars == nil || handlers.auth == nil {
		writeProblem(writer, request, http.StatusNotFound, "no_avatar", "No Telegram photo")
		return
	}
	telegramID, found, err := handlers.auth.CustomerTelegramID(request.Context(), principal.Customer.ID)
	if err != nil {
		handlers.logger.Error("customer telegram id lookup failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "avatar_unavailable", "Unavailable")
		return
	}
	if !found {
		writeProblem(writer, request, http.StatusNotFound, "no_avatar", "No Telegram photo")
		return
	}
	handlers.writeAvatar(writer, request, telegramID)
}

// miniAppAvatar serves the picture of the person a Mini App's launch data
// names, for the sign-in screen's "Sign in as" before a session exists. The
// launch data is verified exactly as sign-in verifies it and travels in the
// body, never the URL; the screen shows the answer as a blob.
func (handlers *AccountHandlers) miniAppAvatar(writer http.ResponseWriter, request *http.Request) {
	if handlers.avatars == nil || handlers.auth == nil {
		writeProblem(writer, request, http.StatusNotFound, "no_avatar", "No Telegram photo")
		return
	}
	var body struct {
		InitData string `json:"initData"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	telegramID, err := handlers.auth.MiniAppTelegramID(body.InitData)
	switch {
	case errors.Is(err, customerauthpg.ErrTelegramUnset):
		writeProblem(writer, request, http.StatusNotFound, "no_avatar", "No Telegram photo")
		return
	case err != nil:
		writeProblem(writer, request, http.StatusUnauthorized, "invalid_credentials", "That launch data was not accepted")
		return
	}
	handlers.writeAvatar(writer, request, telegramID)
}

func (handlers *AccountHandlers) writeAvatar(writer http.ResponseWriter, request *http.Request, telegramID int64) {
	data, err := handlers.avatars.Avatar(request.Context(), telegramID)
	if errors.Is(err, ErrNoAvatar) {
		writeProblem(writer, request, http.StatusNotFound, "no_avatar", "No Telegram photo")
		return
	}
	if err != nil {
		handlers.logger.Warn("telegram avatar could not be read", "error", err)
		writeProblem(writer, request, http.StatusBadGateway, "avatar_unavailable", "Telegram did not return the photo")
		return
	}
	writer.Header().Set("Content-Type", "image/jpeg")
	writer.Header().Set("Cache-Control", "private, max-age=3600")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = writer.Write(data)
}
