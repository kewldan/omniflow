package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/accountreferral"
)

// mountReferral registers the referral, loyalty, and personal-data routes on
// the authenticated customer router.
//
// Everything here is already behind the customer session and the CSRF token the
// group applies. One route asks for more: requesting deletion is gated on a
// recent sign-in, for the same reason link rotation and removing every device
// are. A session left open on a shared machine must not be enough to start the
// removal of somebody's account.
func (handlers *AccountHandlers) mountReferral(router chi.Router) {
	if handlers.referral == nil {
		return
	}
	router.Get("/referrals", handlers.referrals)
	router.Post("/referrals/attribution", handlers.attributeReferral)
	router.Get("/loyalty", handlers.loyalty)

	router.Get("/contacts", handlers.listContacts)
	router.Post("/contacts", handlers.addContact)
	router.Delete("/contacts/{contactID}", handlers.removeContact)

	router.Get("/privacy", handlers.privacy)
	router.Post("/privacy/export", handlers.exportPersonalData)
	router.With(handlers.requireRecentAuthentication).
		Post("/privacy/deletion", handlers.requestDeletion)
	router.Delete("/privacy/deletion", handlers.cancelDeletion)
}

// ---------------------------------------------------------------------------
// Referrals and loyalty
// ---------------------------------------------------------------------------

// attributeReferral records the inviter behind a web sign-up.
//
// The panel posts the code it found in `?ref=` on the sign-in screen once a
// session exists — the same moment the bot's `/start ref_<code>` fires. The
// answer is always 200 with an outcome: a link that did not count is not an
// error the customer can act on, and a problem would surface as a failed
// sign-in on a screen that just succeeded.
func (handlers *AccountHandlers) attributeReferral(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	result, err := handlers.referral.Attribute(request.Context(), principal.Customer.ID, body.Code)
	if handlers.writeReferralError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"attributed": result.Attributed,
		"reason":     result.Reason,
	})
}

func (handlers *AccountHandlers) referrals(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	summary, err := handlers.referral.Referrals(
		request.Context(), principal.Customer.ID,
		trimmedQuery(request, "cursor"), referralPageLimit(request),
	)
	if handlers.writeReferralError(writer, request, err) {
		return
	}

	program := map[string]any{
		"enabled":                 summary.Program.Enabled,
		"currency":                summary.Program.Currency,
		"inviterRewardMinor":      summary.Program.InviterRewardMinor,
		"inviteeRewardMinor":      summary.Program.InviteeRewardMinor,
		"qualification":           summary.Program.Qualification,
		"attributionValidityDays": summary.Program.AttributionValidityDays,
		"inviterRewardCap":        summary.Program.InviterRewardCap,
		"rewardExpiryDays":        summary.Program.RewardExpiryDays,
	}
	if summary.Program.TermsURL != "" {
		program["termsUrl"] = summary.Program.TermsURL
	}

	payload := map[string]any{
		"program": program,
		"code":    summary.Code,
		// The link is reported alongside the reason it is missing, so the share
		// control can be disabled with an explanation rather than copying a URL
		// that goes nowhere.
		"link":           summary.Link,
		"linkAvailable":  summary.Link != "",
		"invited":        summary.Invited,
		"qualified":      summary.Qualified,
		"pending":        summary.Pending,
		"rejected":       summary.Rejected,
		"rewardedMinor":  summary.RewardedMinor,
		"reversedMinor":  summary.ReversedMinor,
		"rewardCount":    summary.RewardCount,
		"currency":       summary.Currency,
		"remainingSlots": summary.RemainingSlots,
		"rewards":        referralRewardPagePayload(summary.Rewards),
	}
	if summary.LinkReason != "" {
		payload["linkReason"] = summary.LinkReason
	}
	writeJSON(writer, http.StatusOK, payload)
}

func referralRewardPagePayload(page accountreferral.RewardPage) map[string]any {
	items := make([]map[string]any, 0, len(page.Items))
	for _, reward := range page.Items {
		item := map[string]any{
			"id": reward.ID, "role": reward.Role, "state": reward.State,
			"amountMinor": reward.AmountMinor, "currency": reward.Currency,
			"grantedAt": reward.GrantedAt.Format(time.RFC3339),
		}
		if reward.ReversedAt != nil {
			item["reversedAt"] = reward.ReversedAt.Format(time.RFC3339)
		}
		items = append(items, item)
	}
	return map[string]any{"items": items, "nextCursor": page.NextCursor}
}

func (handlers *AccountHandlers) loyalty(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	standing, err := handlers.referral.Loyalty(request.Context(), principal.Customer.ID)
	if handlers.writeReferralError(writer, request, err) {
		return
	}
	if !standing.Enabled {
		// A distinct shape rather than an empty ladder. "Not enabled" and "you
		// are on the bottom rung" look identical in a list of zero tiers, and
		// only one of them is worth showing the customer a screen for.
		writeJSON(writer, http.StatusOK, map[string]any{"enabled": false})
		return
	}

	payload := map[string]any{
		"enabled": true,
		"rules": map[string]any{
			"metric": standing.Rules.Metric, "currency": standing.Rules.Currency,
			"windowDays": standing.Rules.WindowDays, "graceDays": standing.Rules.GraceDays,
			"version": standing.Rules.Version,
		},
		"tiers":     loyaltyTierPayloads(standing.Tiers),
		"evaluated": standing.Evaluated,
	}
	if standing.Evaluated {
		payload["tier"] = loyaltyTierPayload(standing.Tier)
		payload["metric"] = standing.Metric
		payload["remaining"] = standing.Remaining
		payload["percent"] = standing.Percent
		payload["evaluatedAt"] = standing.EvaluatedAt.Format(time.RFC3339)
		if standing.Next != nil {
			payload["next"] = loyaltyTierPayload(*standing.Next)
		}
		if standing.GraceUntil != nil {
			payload["graceUntil"] = standing.GraceUntil.Format(time.RFC3339)
		}
	}
	writeJSON(writer, http.StatusOK, payload)
}

func loyaltyTierPayloads(tiers []accountreferral.Tier) []map[string]any {
	payloads := make([]map[string]any, 0, len(tiers))
	for _, tier := range tiers {
		payloads = append(payloads, loyaltyTierPayload(tier))
	}
	return payloads
}

func loyaltyTierPayload(tier accountreferral.Tier) map[string]any {
	return map[string]any{
		"code": tier.Code, "nameEn": tier.NameEN, "nameRu": tier.NameRU,
		"threshold": tier.Threshold, "discountBps": tier.DiscountBPS, "current": tier.Current,
	}
}

// ---------------------------------------------------------------------------
// Contact channels
// ---------------------------------------------------------------------------

func (handlers *AccountHandlers) listContacts(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	contacts, err := handlers.referral.Contacts(request.Context(), principal.Customer.ID)
	if handlers.writeReferralError(writer, request, err) {
		return
	}
	items := make([]map[string]any, 0, len(contacts))
	for _, contact := range contacts {
		items = append(items, map[string]any{
			"id": contact.ID, "kind": contact.Kind, "value": contact.Value,
			"verified": contact.Verified, "transactional": contact.Transactional,
			"marketing": contact.Marketing,
			"createdAt": contact.CreatedAt.Format(time.RFC3339),
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handlers *AccountHandlers) addContact(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		Kind          string `json:"kind"`
		Value         string `json:"value"`
		Transactional bool   `json:"transactional"`
		Marketing     bool   `json:"marketing"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	// Adding a channel is cheap to attempt and useful to abuse: the response
	// distinguishes "accepted" from "not available", which is enough to test
	// addresses in bulk if it can be repeated freely.
	if !handlers.allowPrivacyRate(request, "account_contact", principal.Customer.ID, 10, time.Hour) {
		writeProblem(
			writer, request, http.StatusTooManyRequests,
			"rate_limited", "Too many attempts. Try again later",
		)
		return
	}
	contact, err := handlers.referral.AddContact(
		request.Context(), principal.Customer.ID,
		accountreferral.ContactInput{
			Kind: body.Kind, Value: body.Value,
			Transactional: body.Transactional, Marketing: body.Marketing,
		},
		handlers.referralRequest(request),
	)
	if handlers.writeReferralError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{
		"id": contact.ID, "kind": contact.Kind, "value": contact.Value,
		"verified": contact.Verified, "transactional": contact.Transactional,
		"marketing": contact.Marketing,
		"createdAt": contact.CreatedAt.Format(time.RFC3339),
	})
}

func (handlers *AccountHandlers) removeContact(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	err := handlers.referral.RemoveContact(
		request.Context(), principal.Customer.ID,
		chi.URLParam(request, "contactID"), handlers.referralRequest(request),
	)
	if handlers.writeReferralError(writer, request, err) {
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Privacy
// ---------------------------------------------------------------------------

func (handlers *AccountHandlers) privacy(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	privacy, err := handlers.referral.Privacy(request.Context(), principal.Customer.ID)
	if handlers.writeReferralError(writer, request, err) {
		return
	}
	history := make([]map[string]any, 0, len(privacy.Consents.History))
	for _, record := range privacy.Consents.History {
		history = append(history, map[string]any{
			"purpose": record.Purpose, "granted": record.Granted,
			"policyVersion": record.PolicyVersion, "source": record.Source,
			"occurredAt": record.OccurredAt.Format(time.RFC3339),
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"retention": map[string]any{
			"status":         privacy.Retention.Status,
			"suspendedAt":    referralOptionalTime(privacy.Retention.SuspendedAt),
			"deletedAt":      referralOptionalTime(privacy.Retention.DeletedAt),
			"anonymizedAt":   referralOptionalTime(privacy.Retention.AnonymizedAt),
			"retentionUntil": referralOptionalTime(privacy.Retention.RetentionUntil),
		},
		"deletion": privacyDeletionPayload(privacy.Deletion),
		"consents": map[string]any{
			"current": privacy.Consents.Current, "history": history,
		},
		"export": map[string]any{
			"sections": privacy.Export.Sections, "redactions": privacy.Export.Redactions,
			"contactValuesAvailable": privacy.Export.ContactValuesAvailable,
		},
	})
}

// exportPersonalData answers with the document itself.
//
// It is a POST rather than a GET even though it changes nothing the customer
// can see. A GET would be cached, prefetched, logged in a proxy's access line,
// and reachable by navigation, and the response is the most sensitive document
// this API produces.
func (handlers *AccountHandlers) exportPersonalData(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	// An export is expensive to produce and maximally sensitive to hold. The
	// limit is per customer rather than per address, because the thing being
	// bounded is how often one account's data can be pulled out of the database.
	if !handlers.allowPrivacyRate(request, "account_export", principal.Customer.ID, 3, time.Hour) {
		writeProblem(
			writer, request, http.StatusTooManyRequests,
			"rate_limited", "An export was already produced recently. Try again later",
		)
		return
	}
	document, err := handlers.referral.Export(
		request.Context(), principal.Customer.ID, handlers.referralRequest(request),
	)
	if handlers.writeReferralError(writer, request, err) {
		return
	}
	// The document itself is never logged and never written to disk. It exists
	// in this response and nowhere else.
	writer.Header().Set("Content-Disposition", `attachment; filename="omniflow-personal-data.json"`)
	writeJSON(writer, http.StatusOK, privacyExportPayload(document))
}

func (handlers *AccountHandlers) requestDeletion(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		Reason string `json:"reason"`
		// Confirm has to be sent explicitly. This is the request that ends an
		// account, and a mis-routed or double-submitted form must not start it.
		Confirm bool `json:"confirm"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if !body.Confirm {
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"confirmation_required", "This action must be confirmed",
		)
		return
	}
	deletion, err := handlers.referral.RequestDeletion(
		request.Context(), principal.Customer.ID, body.Reason, handlers.referralRequest(request),
	)
	if handlers.writeReferralError(writer, request, err) {
		return
	}
	// Every other session ends now. The request is the customer saying they
	// are done with the account; a session left open on another device would
	// otherwise keep using it — or cancel the request — without them. The
	// session that asked is kept, because cancelling is done from it. Best
	// effort: the request is already recorded, and failing the response here
	// would tell the customer it was not.
	if handlers.auth != nil {
		if _, revokeErr := handlers.auth.SignOutEverywhere(
			request.Context(), principal.Customer.ID, principal.SessionID, true, handlers.requestContext(request),
		); revokeErr != nil {
			handlers.logger.Warn("other sessions could not be ended after a deletion request", "error", revokeErr)
		}
	}
	// 202: the request has been recorded and something else will act on it. A
	// 200 would suggest the deletion happened.
	writeJSON(writer, http.StatusAccepted, privacyDeletionPayload(deletion))
}

func (handlers *AccountHandlers) cancelDeletion(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	deletion, err := handlers.referral.CancelDeletion(
		request.Context(), principal.Customer.ID, handlers.referralRequest(request),
	)
	if handlers.writeReferralError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, privacyDeletionPayload(deletion))
}

func privacyDeletionPayload(deletion accountreferral.Deletion) map[string]any {
	return map[string]any{
		"pending":     deletion.Pending,
		"requestedAt": referralOptionalTime(deletion.RequestedAt),
		"cancelledAt": referralOptionalTime(deletion.CancelledAt),
		"reason":      deletion.Reason,
		"executedBy":  deletion.ExecutedBy,
	}
}

// ---------------------------------------------------------------------------
// Export payload
// ---------------------------------------------------------------------------

// privacyExportPayload renders the document.
//
// It is built explicitly rather than by tagging the domain structs, so adding a
// field to a read model can never widen a disclosure by accident: a value
// reaches the customer only because a line here puts it there.
func privacyExportPayload(document accountreferral.ExportDocument) map[string]any {
	identities := make([]map[string]any, 0, len(document.Identities))
	for _, identity := range document.Identities {
		identities = append(identities, map[string]any{
			"provider": identity.Provider, "subject": identity.Subject,
			"status": identity.Status, "verified": identity.Verified,
			"createdAt": identity.CreatedAt.Format(time.RFC3339),
		})
	}
	contacts := make([]map[string]any, 0, len(document.Contacts))
	for _, contact := range document.Contacts {
		contacts = append(contacts, map[string]any{
			"kind": contact.Kind, "value": contact.Value, "verified": contact.Verified,
			"transactional": contact.Transactional, "marketing": contact.Marketing,
			"createdAt": contact.CreatedAt.Format(time.RFC3339),
			"revokedAt": referralOptionalTime(contact.RevokedAt),
		})
	}
	subscriptions := make([]map[string]any, 0, len(document.Subscriptions))
	for _, subscription := range document.Subscriptions {
		subscriptions = append(subscriptions, map[string]any{
			"id": subscription.ID, "slot": subscription.Slot, "label": subscription.Label,
			"status":    subscription.Status,
			"createdAt": subscription.CreatedAt.Format(time.RFC3339),
			"closedAt":  referralOptionalTime(subscription.ClosedAt),
		})
	}
	entitlements := make([]map[string]any, 0, len(document.Entitlements))
	for _, entitlement := range document.Entitlements {
		entitlements = append(entitlements, map[string]any{
			"id": entitlement.ID, "planCode": entitlement.PlanCode, "status": entitlement.Status,
			"startsAt":              entitlement.StartsAt.Format(time.RFC3339),
			"endsAt":                entitlement.EndsAt.Format(time.RFC3339),
			"trafficAllowanceBytes": entitlement.TrafficAllowanceBytes,
			"deviceLimit":           entitlement.DeviceLimit,
			"createdAt":             entitlement.CreatedAt.Format(time.RFC3339),
		})
	}
	orders := make([]map[string]any, 0, len(document.Orders))
	for _, order := range document.Orders {
		orders = append(orders, map[string]any{
			"id": order.ID, "state": order.State, "operation": order.Operation,
			"currency": order.Currency, "subtotalMinor": order.SubtotalMinor,
			"discountMinor": order.DiscountMinor, "walletMinor": order.WalletMinor,
			"externalMinor": order.ExternalMinor, "paidMinor": order.PaidMinor,
			"refundedMinor": order.RefundedMinor,
			"createdAt":     order.CreatedAt.Format(time.RFC3339),
			"updatedAt":     order.UpdatedAt.Format(time.RFC3339),
		})
	}
	payments := make([]map[string]any, 0, len(document.Payments))
	for _, payment := range document.Payments {
		payments = append(payments, map[string]any{
			"orderId": payment.OrderID, "provider": payment.Provider, "status": payment.Status,
			"amountMinor": payment.AmountMinor, "currency": payment.Currency,
			"createdAt": payment.CreatedAt.Format(time.RFC3339),
			"updatedAt": payment.UpdatedAt.Format(time.RFC3339),
		})
	}
	wallet := make([]map[string]any, 0, len(document.Wallet))
	for _, entry := range document.Wallet {
		wallet = append(wallet, map[string]any{
			"type": entry.Type, "referenceType": entry.ReferenceType,
			"currency": entry.Currency, "amountMinor": entry.AmountMinor,
			"expiresAt": referralOptionalTime(entry.ExpiresAt),
			"createdAt": entry.CreatedAt.Format(time.RFC3339),
		})
	}
	support := make([]map[string]any, 0, len(document.Support))
	for _, ticket := range document.Support {
		messages := make([]map[string]any, 0, len(ticket.Messages))
		for _, message := range ticket.Messages {
			messages = append(messages, map[string]any{
				"sender": message.Sender, "body": message.Body,
				"createdAt": message.CreatedAt.Format(time.RFC3339),
			})
		}
		support = append(support, map[string]any{
			"id": ticket.ID, "status": ticket.Status,
			"createdAt": ticket.CreatedAt.Format(time.RFC3339),
			"updatedAt": ticket.UpdatedAt.Format(time.RFC3339),
			"messages":  messages,
		})
	}
	rewards := make([]map[string]any, 0, len(document.Referral.Rewards))
	for _, reward := range document.Referral.Rewards {
		rewards = append(rewards, map[string]any{
			"role": reward.Role, "state": reward.State,
			"amountMinor": reward.AmountMinor, "currency": reward.Currency,
			"grantedAt":  reward.GrantedAt.Format(time.RFC3339),
			"reversedAt": referralOptionalTime(reward.ReversedAt),
		})
	}
	referral := map[string]any{
		"code": document.Referral.Code, "invited": document.Referral.Invited,
		"qualified": document.Referral.Qualified, "rewards": rewards,
	}
	if document.Referral.InvitedBy != nil {
		referral["invitedBy"] = map[string]any{
			"attributedAt": document.Referral.InvitedBy.AttributedAt.Format(time.RFC3339),
			"qualified":    document.Referral.InvitedBy.Qualified,
			"qualifiedAt":  referralOptionalTime(document.Referral.InvitedBy.QualifiedAt),
		}
	}
	loyaltyHistory := make([]map[string]any, 0, len(document.Loyalty.History))
	for _, change := range document.Loyalty.History {
		loyaltyHistory = append(loyaltyHistory, map[string]any{
			"fromTier": change.FromTier, "toTier": change.ToTier,
			"metric": change.Metric, "reason": change.Reason,
			"occurredAt": change.OccurredAt.Format(time.RFC3339),
		})
	}
	consents := make([]map[string]any, 0, len(document.Consents))
	for _, consent := range document.Consents {
		consents = append(consents, map[string]any{
			"purpose": consent.Purpose, "granted": consent.Granted,
			"policyVersion": consent.PolicyVersion, "source": consent.Source,
			"occurredAt": consent.OccurredAt.Format(time.RFC3339),
		})
	}
	lifecycle := make([]map[string]any, 0, len(document.Lifecycle))
	for _, event := range document.Lifecycle {
		lifecycle = append(lifecycle, map[string]any{
			"action": event.Action, "reason": event.Reason, "actorType": event.ActorType,
			"occurredAt": event.OccurredAt.Format(time.RFC3339),
		})
	}

	profile := document.Profile
	return map[string]any{
		"version":     document.Version,
		"generatedAt": document.GeneratedAt.Format(time.RFC3339),
		"profile": map[string]any{
			"id": profile.ID, "status": profile.Status,
			"locale": profile.Locale, "timezone": profile.Timezone,
			"createdAt":      profile.CreatedAt.Format(time.RFC3339),
			"updatedAt":      profile.UpdatedAt.Format(time.RFC3339),
			"suspendedAt":    referralOptionalTime(profile.SuspendedAt),
			"deletedAt":      referralOptionalTime(profile.DeletedAt),
			"anonymizedAt":   referralOptionalTime(profile.AnonymizedAt),
			"retentionUntil": referralOptionalTime(profile.RetentionUntil),
		},
		"identities":    identities,
		"contacts":      contacts,
		"subscriptions": subscriptions,
		"entitlements":  entitlements,
		"orders":        orders,
		"payments":      payments,
		"wallet":        wallet,
		"support":       support,
		"referral":      referral,
		"loyalty": map[string]any{
			"enabled": document.Loyalty.Enabled, "tierCode": document.Loyalty.TierCode,
			"metric":      document.Loyalty.Metric,
			"evaluatedAt": referralOptionalTime(document.Loyalty.EvaluatedAt),
			"graceUntil":  referralOptionalTime(document.Loyalty.GraceUntil),
			"history":     loyaltyHistory,
		},
		"consents":   consents,
		"lifecycle":  lifecycle,
		"redactions": document.Redactions,
		"truncated":  document.Truncated,
	}
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// referralRequest is the transport detail an audit or lifecycle row records. It
// never carries a contact value or an export payload.
func (handlers *AccountHandlers) referralRequest(request *http.Request) accountreferral.RequestContext {
	context := handlers.requestContext(request)
	address := ""
	if context.IP != nil {
		address = context.IP.String()
	}
	return accountreferral.RequestContext{
		IP: address, UserAgent: context.UserAgent, RequestID: context.RequestID,
	}
}

// allowPrivacyRate applies a rate limit, failing closed when the limiter is
// unreachable so an outage cannot silently remove the protection.
func (handlers *AccountHandlers) allowPrivacyRate(
	request *http.Request, scope, subject string, limit int64, window time.Duration,
) bool {
	if handlers.limiter == nil || subject == "" {
		return true
	}
	allowed, err := handlers.limiter.Allow(request.Context(), scope, subject, limit, window)
	if err != nil {
		handlers.logger.Warn("customer rate limiter unavailable", "scope", scope, "error", err)
		return false
	}
	return allowed
}

// referralPageLimit reads an optional page size. An unreadable value is no value: the
// right response to "limit=banana" is the default page, not a failed request.
func referralPageLimit(request *http.Request) int {
	limit, err := strconv.Atoi(trimmedQuery(request, "limit"))
	if err != nil {
		return 0
	}
	return limit
}

// referralOptionalTime renders an optional instant, so a JSON null means "never
// happened" rather than "the zero time".
func referralOptionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}

// writeReferralError maps this surface's errors onto responses, reporting
// whether it wrote one.
//
// The contact conflict is the interesting case. It answers 409 with wording
// that names no account, because telling a caller that an address belongs to
// somebody else turns this route into a way of asking "does this person have an
// account here?". The remedy is a support handoff, where a person can establish
// who is asking before anything is revealed.
func (handlers *AccountHandlers) writeReferralError(
	writer http.ResponseWriter, request *http.Request, err error,
) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, accountreferral.ErrInvalidInput):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_input", err.Error())
	case errors.Is(err, accountreferral.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That record was not found")
	case errors.Is(err, accountreferral.ErrContactConflict):
		writeProblem(
			writer, request, http.StatusConflict, "contact_unavailable",
			"This contact cannot be added here. Contact support to continue",
		)
	case errors.Is(err, accountreferral.ErrDeletionPending):
		writeProblem(
			writer, request, http.StatusConflict, "deletion_pending",
			"A deletion request is already open",
		)
	case errors.Is(err, accountreferral.ErrNoDeletionPending):
		writeProblem(
			writer, request, http.StatusConflict, "no_deletion_pending",
			"There is no deletion request to cancel",
		)
	case errors.Is(err, accountreferral.ErrContactsUnavailable):
		writeProblem(
			writer, request, http.StatusServiceUnavailable, "contacts_unavailable",
			"Contact channels are not configured on this installation",
		)
	default:
		handlers.logger.Error("customer referral request failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "request_failed", "Something went wrong")
	}
	return true
}
