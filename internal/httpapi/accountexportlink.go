package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"time"
)

// The personal-data export, delivered by a one-time link.
//
// Inside the Telegram Mini App a page cannot save a file it assembled itself:
// Telegram's web view drops the download. Telegram offers a native download
// instead (downloadFile), but its client fetches the address itself, without
// the customer's session cookie. So the signed-in page asks for a link, and
// the link is the credential: random, single-use, five minutes long, minted
// only from a session and counted against the same export limit as the direct
// download. Nothing of the document is stored to serve it; the export is built
// when the link is fetched, as the POST builds it.

const (
	exportLinkTTL    = 5 * time.Minute
	exportLinkPrefix = "account_export_link:"
)

func exportLinkKey(token string) string {
	digest := sha256.Sum256([]byte(token))
	return exportLinkPrefix + hex.EncodeToString(digest[:])
}

// issueExportLink answers a signed-in customer with a one-time download path.
func (handlers *AccountHandlers) issueExportLink(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	store := handlers.limiter.TemporaryStore()
	if store == nil {
		writeProblem(writer, request, http.StatusServiceUnavailable,
			"export_link_unavailable", "A download link cannot be issued right now")
		return
	}
	if !handlers.allowPrivacyRate(request, "account_export", principal.Customer.ID, 3, time.Hour) {
		writeProblem(
			writer, request, http.StatusTooManyRequests,
			"rate_limited", "An export was already produced recently. Try again later",
		)
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "internal_error", "Internal error")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	claimed, err := store.Claim(request.Context(), exportLinkKey(token), principal.Customer.ID, exportLinkTTL)
	if err != nil || !claimed {
		handlers.logger.Warn("export link could not be stored", "error", err)
		writeProblem(writer, request, http.StatusServiceUnavailable,
			"export_link_unavailable", "A download link cannot be issued right now")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"path":      "/v1/account/privacy/export/file?token=" + token,
		"expiresAt": time.Now().Add(exportLinkTTL).UTC(),
	})
}

// exportByLink spends a link and answers with the document as a file.
func (handlers *AccountHandlers) exportByLink(writer http.ResponseWriter, request *http.Request) {
	store := handlers.limiter.TemporaryStore()
	token := request.URL.Query().Get("token")
	if store == nil || token == "" {
		writeProblem(writer, request, http.StatusNotFound, "link_invalid", "That download link is not valid")
		return
	}
	key := exportLinkKey(token)
	customerID, found, err := store.Lookup(request.Context(), key)
	if err != nil || !found {
		writeProblem(writer, request, http.StatusNotFound, "link_invalid", "That download link is not valid")
		return
	}
	// Spent before the document is built, so a second fetch racing this one
	// finds nothing once this line has run.
	if err := store.Forget(request.Context(), key); err != nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "export_link_unavailable", "Try again")
		return
	}
	document, err := handlers.referral.Export(request.Context(), customerID, handlers.referralRequest(request))
	if handlers.writeReferralError(writer, request, err) {
		return
	}
	// Telegram Web fetches the file from its own origin.
	writer.Header().Set("Access-Control-Allow-Origin", "https://web.telegram.org")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Disposition", `attachment; filename="omniflow-personal-data.json"`)
	writeJSON(writer, http.StatusOK, privacyExportPayload(document))
}
