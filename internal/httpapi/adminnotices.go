package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountNotices registers the transactional-notice wording surfaces.
//
// It is `settings` rather than `marketing`, which is the distinction that
// matters here. Marketing permissions govern campaigns: messages an operator
// composes and sends to an audience they chose. These are the messages the
// installation sends on its own initiative because something happened to a
// subscription, they reach every customer, and there is no audience decision to
// make — that is a configuration of how the installation speaks, and it belongs
// with the settings that decide what it looks like.
func (handlers *AdminHandlers) mountNotices(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).Group(func(read chi.Router) {
		read.Get("/notices", handlers.notices)
		read.Get("/notices/{code}/tests", handlers.noticeTests)
	})

	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).Group(func(write chi.Router) {
		write.Put("/notices/{code}", handlers.saveNotice)
		write.Delete("/notices/{code}", handlers.revertNotice)
		// A preview is a write even though it stores nothing: it renders a body
		// the caller supplied, and a read permission should not be a way to run
		// the renderer over arbitrary text.
		write.Post("/notices/{code}/preview", handlers.previewNotice)
		write.Post("/notices/{code}/test", handlers.sendNoticeTest)
	})
}

// notices lists every overridable message with its shipped wording, its
// variables, and whatever an operator has written.
func (handlers *AdminHandlers) notices(writer http.ResponseWriter, request *http.Request) {
	list, err := handlers.operations.Notices(request.Context())
	handlers.respond(writer, request, map[string]any{"items": list}, err)
}

// saveNotice stores one body for one locale.
//
// One locale at a time, deliberately. An operator who rewrites the English
// expiry warning has not thereby decided anything about the Russian one, and a
// document-shaped save would let this screen overwrite a translation somebody
// else was working on.
func (handlers *AdminHandlers) saveNotice(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Locale string `json:"locale"`
		Body   string `json:"body"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	saved, err := handlers.operations.SaveNotice(
		request.Context(), chi.URLParam(request, "code"), body.Locale, body.Body,
		actorFrom(request))
	handlers.respond(writer, request, saved, err)
}

// revertNotice removes an override so the shipped wording applies again.
func (handlers *AdminHandlers) revertNotice(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.RevertNotice(
		request.Context(), chi.URLParam(request, "code"), query(request, "locale"),
		actorFrom(request))
	if err != nil {
		handlers.respond(writer, request, nil, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// previewNotice renders a body against the notice's sample values.
//
// It refuses exactly what a save refuses, so a preview can never show an
// operator a message that could not then be stored — and the samples come from
// the notice definition rather than from the request, so what is previewed is
// what a test send produces.
func (handlers *AdminHandlers) previewNotice(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Locale string `json:"locale"`
		Body   string `json:"body"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	preview, err := handlers.operations.PreviewNotice(
		request.Context(), chi.URLParam(request, "code"), body.Locale, body.Body)
	handlers.respond(writer, request, preview, err)
}

// sendNoticeTest queues one rendered copy for the operator group.
//
// Never for a customer. A transactional notice has a trigger, and manufacturing
// one against a real subscription to see how the wording reads would tell
// somebody their access is about to end when it is not.
func (handlers *AdminHandlers) sendNoticeTest(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Locale string `json:"locale"`
		Body   string `json:"body"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	test, err := handlers.operations.SendNoticeTest(
		request.Context(), chi.URLParam(request, "code"), body.Locale, body.Body,
		actorFrom(request))
	if err != nil {
		handlers.respond(writer, request, nil, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, test)
}

// noticeTests reports what has been queued, so a preview in flight looks like
// one rather than like a button that did nothing.
func (handlers *AdminHandlers) noticeTests(writer http.ResponseWriter, request *http.Request) {
	tests, err := handlers.operations.NoticeTests(
		request.Context(), chi.URLParam(request, "code"), queryInt(request, "limit"))
	handlers.respond(writer, request, map[string]any{"items": tests}, err)
}
