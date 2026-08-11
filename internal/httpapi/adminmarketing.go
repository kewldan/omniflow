package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountMarketing registers campaigns, segments, templates, suppressions, and
// the referral programme.
//
// Reading is behind marketing.read and every write behind marketing.write, with
// one exception: the referral programme is money. Changing what an invite pays
// is a commercial decision with a bill attached, so it sits behind
// settings.write alongside the other things that cost the installation money.
func (handlers *AdminHandlers) mountMarketing(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).Group(func(read chi.Router) {
		read.Get("/marketing/segments", handlers.audienceSegments)
		read.Get("/marketing/templates", handlers.messageTemplates)
		read.Get("/marketing/campaigns", handlers.campaigns)
		read.Get("/marketing/suppressions", handlers.suppressions)
		read.Get("/marketing/referrals", handlers.referralProgram)
	})

	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Put("/marketing/segments", handlers.saveSegment)
		write.Put("/marketing/templates", handlers.saveTemplate)
		write.Post("/marketing/campaigns", handlers.createCampaign)
		write.Post("/marketing/campaigns/{campaignID}/state", handlers.setCampaignState)
		write.Put("/marketing/suppressions", handlers.suppress)
		write.Delete("/marketing/suppressions/{customerID}", handlers.unsuppress)
	})

	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).Group(func(write chi.Router) {
		write.Put("/marketing/referrals", handlers.saveReferralProgram)
	})
}

func (handlers *AdminHandlers) audienceSegments(writer http.ResponseWriter, request *http.Request) {
	segments, err := handlers.operations.Segments(request.Context())
	handlers.respond(writer, request, map[string]any{"items": segments}, err)
}

func (handlers *AdminHandlers) saveSegment(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.AudienceSegment
	if !decodeJSON(writer, request, &body) {
		return
	}
	segment, err := handlers.operations.SaveSegment(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, segment, err)
}

func (handlers *AdminHandlers) messageTemplates(writer http.ResponseWriter, request *http.Request) {
	templates, err := handlers.operations.Templates(request.Context())
	handlers.respond(writer, request, map[string]any{"items": templates}, err)
}

func (handlers *AdminHandlers) saveTemplate(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.MessageTemplate
	if !decodeJSON(writer, request, &body) {
		return
	}
	template, err := handlers.operations.SaveTemplate(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, template, err)
}

func (handlers *AdminHandlers) campaigns(writer http.ResponseWriter, request *http.Request) {
	list, err := handlers.operations.Campaigns(request.Context(), queryInt(request, "limit"))
	handlers.respond(writer, request, map[string]any{"items": list}, err)
}

func (handlers *AdminHandlers) createCampaign(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name       string `json:"name"`
		TemplateID string `json:"templateId"`
		SegmentID  string `json:"segmentId"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	// A campaign is created in draft. Scheduling it is a second, separate call,
	// so the estimated audience is something an operator sees before they commit
	// rather than after.
	campaign, err := handlers.operations.CreateCampaign(
		request.Context(), body.Name, body.TemplateID, body.SegmentID, actorFrom(request))
	handlers.respond(writer, request, campaign, err)
}

func (handlers *AdminHandlers) setCampaignState(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		State string `json:"state"`
		// ScheduledFor is required to schedule and ignored otherwise. The
		// service refuses a schedule without one rather than defaulting to now,
		// because "send immediately" and "somebody forgot the date" must not be
		// the same request.
		ScheduledFor *time.Time `json:"scheduledFor"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	campaign, err := handlers.operations.SetCampaignState(
		request.Context(), chi.URLParam(request, "campaignID"),
		body.State, body.ScheduledFor, actorFrom(request))
	handlers.respond(writer, request, campaign, err)
}

func (handlers *AdminHandlers) suppressions(writer http.ResponseWriter, request *http.Request) {
	list, err := handlers.operations.Suppressions(request.Context(), queryInt(request, "limit"))
	handlers.respond(writer, request, map[string]any{"items": list}, err)
}

func (handlers *AdminHandlers) suppress(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		CustomerID string `json:"customerId"`
		Reason     string `json:"reason"`
		Note       string `json:"note"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.Suppress(
		request.Context(), body.CustomerID, body.Reason, body.Note, actorFrom(request))
	handlers.respond(writer, request, map[string]any{"suppressed": true}, err)
}

func (handlers *AdminHandlers) unsuppress(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.Unsuppress(
		request.Context(), chi.URLParam(request, "customerID"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"removed": true}, err)
}

func (handlers *AdminHandlers) referralProgram(writer http.ResponseWriter, request *http.Request) {
	program, err := handlers.operations.ReferralProgram(request.Context())
	handlers.respond(writer, request, program, err)
}

func (handlers *AdminHandlers) saveReferralProgram(
	writer http.ResponseWriter, request *http.Request,
) {
	var body panelpg.ReferralProgram
	if !decodeJSON(writer, request, &body) {
		return
	}
	program, err := handlers.operations.SaveReferralProgram(
		request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, program, err)
}
