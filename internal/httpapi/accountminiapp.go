package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// MiniAppSettings is how the customer panel behaves when Telegram opens it as
// a Mini App. Both come from the bot section of the installation settings.
type MiniAppSettings struct {
	// Fullscreen asks Telegram to open the Mini App full screen rather than
	// as a sheet; clients older than Bot API 8.0 ignore it and expand.
	Fullscreen bool `json:"fullscreen"`
	// Lite reduces the panel's navigation inside the Mini App to the errands
	// a customer opens it for: their subscription, buying, and support.
	Lite bool `json:"lite"`
}

// SetMiniApp attaches the reader of the Mini App settings. It must be called
// before Mount; without it the route is not mounted and the panel uses its
// defaults.
func (handlers *AccountHandlers) SetMiniApp(read func(context.Context) MiniAppSettings) {
	handlers.miniApp = read
}

// mountMiniApp serves the Mini App settings. The route is public: the panel
// asks for them before a Mini App visitor has finished signing in, and they
// describe the installation, not anybody's account.
func (handlers *AccountHandlers) mountMiniApp(account chi.Router) {
	if handlers.miniApp == nil {
		return
	}
	account.Get("/miniapp", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusOK, handlers.miniApp(request.Context()))
	})
}
