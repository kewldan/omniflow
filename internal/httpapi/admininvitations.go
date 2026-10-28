package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// Operator invitations.
//
// An owner names an address and a role set; the API returns a single-use link
// token exactly once, and the person it is meant for redeems it on a public
// screen by choosing their own name and password. There is no email transport,
// so the panel shows the link and the owner passes it on — the same delivery
// model as a password reset, minus the server log, because the inviter is
// signed in and can be handed the link directly.

func (handlers *AdminHandlers) mountInvitations(secure chi.Router) {
	secure.With(handlers.requirePermission(rbac.PermissionAdminsRead)).
		Get("/admins/invitations", handlers.listInvitations)
	secure.With(handlers.requirePermission(rbac.PermissionAdminsWrite)).
		Post("/admins/invitations", handlers.createInvitation)
	secure.With(handlers.requirePermission(rbac.PermissionAdminsWrite)).
		Delete("/admins/invitations/{invitationID}", handlers.revokeInvitation)
}

func (handlers *AdminHandlers) mountInvitationsPublic(panel chi.Router) {
	// The token travels in a body rather than a path, so it never lands in an
	// access log or a proxy's request line.
	panel.Post("/invitations/preview", handlers.previewInvitation)
	panel.Post("/invitations/accept", handlers.acceptInvitation)
}

func invitationPayload(invitation adminauthpg.Invitation) map[string]any {
	return map[string]any{
		"id":            invitation.ID,
		"email":         invitation.Email,
		"roles":         roleStrings(invitation.Roles),
		"createdAt":     invitation.CreatedAt.UTC(),
		"expiresAt":     invitation.ExpiresAt.UTC(),
		"invitedBy":     invitation.InvitedBy,
		"invitedByName": invitation.InvitedByName,
	}
}

func (handlers *AdminHandlers) listInvitations(writer http.ResponseWriter, request *http.Request) {
	invitations, err := handlers.service.ListInvitations(request.Context())
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "invitations_unavailable", "Invitations are unavailable")
		return
	}
	items := make([]map[string]any, 0, len(invitations))
	for _, invitation := range invitations {
		items = append(items, invitationPayload(invitation))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handlers *AdminHandlers) createInvitation(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	var body struct {
		Email string   `json:"email"`
		Roles []string `json:"roles"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	roles, err := parseRoles(body.Roles)
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "unknown_role", err.Error())
		return
	}
	issued, err := handlers.service.Invite(request.Context(), adminauthpg.InviteInput{
		Email: body.Email, Roles: roles,
	}, principal, handlers.requestContext(request))
	switch {
	case errors.Is(err, adminauthpg.ErrValidation):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invitation_invalid",
			"An address and at least one role are required")
		return
	case errors.Is(err, adminauthpg.ErrForbidden):
		writeProblem(writer, request, http.StatusForbidden, "role_escalation",
			"You cannot invite somebody with authority you do not hold yourself")
		return
	case errors.Is(err, adminauthpg.ErrConflict):
		writeProblem(writer, request, http.StatusConflict, "email_in_use", "That address already has an account")
		return
	case err != nil:
		writeProblem(writer, request, http.StatusInternalServerError, "invitation_failed", "The invitation could not be created")
		return
	}
	// The only response that ever carries the token. Nothing stores it and
	// nothing logs it; the panel builds the link from it and shows it once.
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusCreated, map[string]any{
		"invitation": invitationPayload(issued.Invitation),
		"token":      issued.Token,
	})
}

func (handlers *AdminHandlers) revokeInvitation(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	err := handlers.service.RevokeInvitation(
		request.Context(), chi.URLParam(request, "invitationID"), principal, handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, adminauthpg.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "invitation_not_found", "No open invitation has that identifier")
		return
	case err != nil:
		writeProblem(writer, request, http.StatusInternalServerError, "invitation_revoke_failed", "The invitation could not be revoked")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) previewInvitation(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	// A token has 256 bits, so guessing is not the threat; the limit is there
	// so the endpoint cannot be used as a cheap probe at all.
	if !handlers.allow(request, "admin-invitation", clientKey(handlers.requestContext(request)), 30, time.Minute) {
		writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many attempts")
		return
	}
	preview, err := handlers.service.PreviewInvitation(request.Context(), body.Token)
	switch {
	case errors.Is(err, adminauthpg.ErrInvitationInvalid):
		writeProblem(writer, request, http.StatusNotFound, "invitation_invalid",
			"This invitation link is not valid. It may have expired, been revoked, or been used already.")
		return
	case err != nil:
		writeProblem(writer, request, http.StatusInternalServerError, "invitation_unavailable", "The invitation could not be read")
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]any{
		"email":     preview.Email,
		"roles":     roleStrings(preview.Roles),
		"expiresAt": preview.ExpiresAt.UTC(),
	})
}

func (handlers *AdminHandlers) acceptInvitation(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	var body struct {
		Token       string `json:"token"`
		DisplayName string `json:"displayName"`
		Password    string `json:"password"`
		Locale      string `json:"locale"`
		Timezone    string `json:"timezone"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if !handlers.allow(request, "admin-invitation", clientKey(handlers.requestContext(request)), 30, time.Minute) {
		writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many attempts")
		return
	}
	reqCtx := handlers.requestContext(request)
	account, err := handlers.service.AcceptInvitation(request.Context(), adminauthpg.AcceptInvitationInput{
		Token: body.Token, DisplayName: body.DisplayName, Password: body.Password,
		Locale: body.Locale, Timezone: body.Timezone,
	}, reqCtx)
	switch {
	case errors.Is(err, adminauthpg.ErrInvitationInvalid):
		writeProblem(writer, request, http.StatusNotFound, "invitation_invalid",
			"This invitation link is not valid. It may have expired, been revoked, or been used already.")
		return
	case errors.Is(err, adminauth.ErrPasswordTooShort), errors.Is(err, adminauth.ErrPasswordTooLong):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "weak_password", err.Error())
		return
	case errors.Is(err, adminauthpg.ErrValidation):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "display_name_required", "A display name is required")
		return
	case errors.Is(err, adminauthpg.ErrConflict):
		writeProblem(writer, request, http.StatusConflict, "email_in_use", "That address already has an account")
		return
	case err != nil:
		handlers.logger.Error("invitation acceptance failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "invitation_failed", "The account could not be created")
		return
	}

	// The account exists; sign the new operator in with the password they
	// just chose, through the ordinary sign-in path, so the session is
	// indistinguishable from any other and the lockout and audit rules apply.
	// A failure here is not a failure of the invitation: the account stands
	// and the person can sign in from the login screen.
	result, err := handlers.service.Authenticate(request.Context(), account.Email, body.Password, reqCtx)
	if err != nil {
		handlers.logger.Warn("signing in a newly invited operator failed", "adminUserId", account.ID, "error", err)
		writeJSON(writer, http.StatusCreated, map[string]any{
			"signedIn": false, "account": accountPayload(account),
		})
		return
	}
	handlers.setSessionCookie(writer, result.Token, result.ExpiresAt)
	writeJSON(writer, http.StatusCreated, map[string]any{
		"signedIn":  true,
		"csrfToken": result.CSRFToken,
		"expiresAt": result.ExpiresAt.UTC(),
		"account":   accountPayload(result.Account),
	})
}
