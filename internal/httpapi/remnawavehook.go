package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/omniflow/omniflow/internal/remnawavesync"
)

// RemnawaveWebhookHandler receives the panel's own event deliveries.
//
// It is unauthenticated in the session sense: the panel proves itself with an
// HMAC of the body under the shared secret, exactly as a payment provider does.
// A delivery that does not verify is refused before anything is parsed.
type RemnawaveWebhookHandler struct {
	intake *remnawavesync.Intake
	logger *slog.Logger
}

// NewRemnawaveWebhookHandler builds the handler, or returns nil when there is
// no secret: an endpoint that would refuse everything is better left
// unmounted.
func NewRemnawaveWebhookHandler(intake *remnawavesync.Intake, logger *slog.Logger) *RemnawaveWebhookHandler {
	if !intake.Configured() {
		return nil
	}
	return &RemnawaveWebhookHandler{intake: intake, logger: logger}
}

// Mount registers POST /v1/remnawave/webhook.
func (handler *RemnawaveWebhookHandler) Mount(router chi.Router) {
	router.Post("/v1/remnawave/webhook", handler.receive)
}

func (handler *RemnawaveWebhookHandler) receive(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, remnawave.MaxWebhookBody))
	if err != nil {
		writeProblem(writer, request, http.StatusRequestEntityTooLarge, "payload_too_large", "Webhook is too large")
		return
	}
	outcome, err := handler.intake.Accept(request.Context(), body, request.Header.Get(remnawave.WebhookSignatureHeader))
	switch {
	case errors.Is(err, remnawavesync.ErrBadSignature):
		handler.logger.Warn("remnawave webhook signature rejected")
		writeProblem(writer, request, http.StatusUnauthorized, "invalid_signature", "The signature does not verify")
		return
	case errors.Is(err, remnawave.ErrWebhookMalformed):
		writeProblem(writer, request, http.StatusBadRequest, "invalid_payload", "The body is not a Remnawave event")
		return
	case errors.Is(err, remnawavesync.ErrNotConfigured):
		writeProblem(writer, request, http.StatusServiceUnavailable, "not_configured", "Webhooks are not configured")
		return
	case err != nil:
		// The panel retries a failed delivery, and the body digest makes the
		// retry safe, so a storage failure is answered with a retryable status.
		handler.logger.Error("remnawave webhook could not be recorded", "error", err)
		writeProblem(writer, request, http.StatusServiceUnavailable, "webhook_retry", "The event could not be recorded yet")
		return
	}
	handler.logger.Info("remnawave webhook received", "event", outcome.Event,
		"duplicate", outcome.Duplicate, "customer", outcome.Customer, "operator", outcome.Operator)
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
}
