package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ErrNoAvatar is what an AvatarSource returns for a customer with no photo
// the bot can see.
var ErrNoAvatar = errors.New("no avatar")

// AvatarSource reads a Telegram account's current profile photo.
type AvatarSource interface {
	Avatar(ctx context.Context, telegramID int64) ([]byte, error)
}

// customerAvatar serves the customer's Telegram profile photo to an operator.
//
// It is read from Telegram on demand and never stored. The response is private
// and short-lived, so a shared proxy cannot keep it and an operator's browser
// forgets it within the hour. A customer without a Telegram account, or whose
// photo the bot cannot see, answers 404 and the card shows initials.
func (handlers *AdminHandlers) customerAvatar(writer http.ResponseWriter, request *http.Request) {
	profile, err := handlers.operations.CustomerProfile(request.Context(), chi.URLParam(request, "customerID"))
	if err != nil {
		handlers.respond(writer, request, nil, err)
		return
	}
	if profile.TelegramID == nil {
		writeProblem(writer, request, http.StatusNotFound, "no_avatar", "This customer has no Telegram photo")
		return
	}
	data, err := handlers.avatars.Avatar(request.Context(), *profile.TelegramID)
	if errors.Is(err, ErrNoAvatar) {
		writeProblem(writer, request, http.StatusNotFound, "no_avatar", "This customer has no Telegram photo")
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
