package botapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/omniflow/omniflow/internal/remnawave"
)

const (
	callbackPrefix    = "nav:"
	actionPrefix      = "act:"
	routeHome         = "home"
	routeSubscription = "subscription"
	routeConnect      = "connect"
	routeDevices      = "devices"
	routeSupport      = "support"
	routeSettings     = "settings"
	routeReferral     = "referral"
	routePlans        = "plans"
	routeOrders       = "orders"
	routeWallet       = "wallet"
	routeNews         = "news"
	routeAutoRenew    = "autorenew"
	routeMethods      = "methods"
	routeShop         = "shop"
	routeShopOrders   = "shoporders"
	routeGifts        = "gifts"
	routeOffers       = "offers"
	// v0.5 surfaces.
	routeTopUp         = "topup"
	routeCart          = "cart"
	routeSubscriptions = "subs"
)

// callbackWindow and callbackBudget bound how many interactions one Telegram
// account may drive per minute.
const (
	callbackWindow  = time.Minute
	callbackBudget  = 40
	checkoutBudget  = 6
	promoBudget     = 5
	messageBudget   = 20
	replayRetention = 24 * time.Hour
)

type App struct {
	logger      *slog.Logger
	store       Store
	remnawave   remnawave.Service
	supportURL  string
	termsURL    string
	botUsername string

	// The commerce surface is optional. Without it the bot keeps its v0.2
	// self-service behaviour and the commerce routes report that purchases are
	// not configured.
	customers *PostgresStore
	commerce  *Commerce
	limiter   *platform.RateLimiter
	sender    *Sender
	settings  CommerceSettings

	// operators is the Telegram backup/restore surface. It stays nil until an
	// installation names its operator accounts.
	operators *OperatorTools

	// membership answers channel questions at checkout. It stays nil until the
	// process wires one in, and a nil verifier gates nothing — an installation
	// that requires no channels wanted exactly that.
	membership MembershipVerifier

	// webSignIn issues one-time links into the customer web panel. It stays nil
	// unless the operator enabled the magic-link fallback.
	webSignIn WebSignIn
}

func New(logger *slog.Logger, store Store, remnawaveService remnawave.Service, supportURL string) *App {
	return &App{
		logger:     logger,
		store:      store,
		remnawave:  remnawaveService,
		supportURL: strings.TrimSpace(supportURL),
	}
}

// EnableCommerce turns on the v0.4 purchase, lifecycle, support, and
// communication surface.
func (app *App) EnableCommerce(customers *PostgresStore, commerceService *Commerce, limiter *platform.RateLimiter, sender *Sender, settings CommerceSettings) {
	app.customers, app.commerce, app.limiter, app.sender, app.settings = customers, commerceService, limiter, sender, settings
	app.termsURL = strings.TrimSpace(settings.TermsURL)
}

func (app *App) SetBotUsername(username string) {
	app.botUsername = strings.TrimPrefix(strings.TrimSpace(username), "@")
}

// allow applies a per-user, per-action Valkey rate limit. A limiter outage fails
// closed for money-moving actions and open for navigation, so a Valkey incident
// degrades browsing instead of allowing unbounded checkout attempts.
func (app *App) allow(ctx context.Context, scope string, telegramID int64, budget int64) bool {
	if app.limiter == nil {
		return true
	}
	allowed, err := app.limiter.Allow(ctx, scope, strconv.FormatInt(telegramID, 10), budget, callbackWindow)
	if err != nil {
		app.logger.Warn("rate limiter unavailable", "scope", scope, "error", err)
		return scope == "bot:nav"
	}
	return allowed
}

// claimCallback consumes a one-shot token for a consequential callback. A
// redelivered Telegram update or a second tap on the same button resolves to
// false instead of creating a second order, payment, or deletion.
func (app *App) claimCallback(ctx context.Context, queryID string) bool {
	if app.limiter == nil {
		return true
	}
	claimed, err := app.limiter.Once(ctx, "callback", queryID, replayRetention)
	if err != nil {
		app.logger.Error("callback replay protection unavailable", "error", err)
		return false
	}
	return claimed
}

// consequentialActions must never run twice for one Telegram callback.
var consequentialActions = map[string]bool{
	"confirm": true, "order-cancel": true, "pm": true, "wallet-toggle": true,
	"pay": true, "order-pm": true,
	"promo-clear": true, "invoice": true, "device-delete": true, "devices-delete": true,
	"revoke": true, "ticket-close": true, "ticket-open": true, "autorenew": true,
	// v0.5 actions that move money, provisioning, or database state.
	"topup-pm": true, "cart-buy": true, "cart-clear": true, "cart-save": true,
	"cart-auto": true, "addon-buy": true, "sub-revoke": true,
	"renew-funding": true, "renew-lead": true, "method-default": true, "method-remove": true,
	"ops-backup": true, "ops-restore": true,
}

// checkoutBudgetActions are the callbacks that create a provider payment. They
// share the checkout budget with the free-text top-up amount, so a customer
// cannot start payments faster than they could confirm checkouts.
var checkoutBudgetActions = map[string]bool{"confirm": true, "pay": true, "order-pm": true}

func (app *App) Register(client *telegram.Bot) {
	client.RegisterHandler(telegram.HandlerTypeMessageText, "start", telegram.MatchTypeCommandStartOnly, app.HandleStart)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "menu", telegram.MatchTypeCommandStartOnly, app.HandleStart)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "settings", telegram.MatchTypeCommandStartOnly, app.HandleSettings)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "support", telegram.MatchTypeCommandStartOnly, app.HandleSupport)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "plans", telegram.MatchTypeCommandStartOnly, app.HandlePlans)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "orders", telegram.MatchTypeCommandStartOnly, app.HandleOrders)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "wallet", telegram.MatchTypeCommandStartOnly, app.HandleWallet)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "ops", telegram.MatchTypeCommandStartOnly, app.HandleOps)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "login", telegram.MatchTypeCommandStartOnly, app.HandleWebLogin)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "cancel", telegram.MatchTypeCommandStartOnly, app.HandleCancel)
	client.RegisterHandler(telegram.HandlerTypeCallbackQueryData, callbackPrefix, telegram.MatchTypePrefix, app.HandleCallback)
	client.RegisterHandler(telegram.HandlerTypeCallbackQueryData, actionPrefix, telegram.MatchTypePrefix, app.HandleCallback)
}

func (app *App) HandleStart(ctx context.Context, client *telegram.Bot, update *models.Update) {
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != models.ChatTypePrivate {
		return
	}
	if !app.allow(ctx, "bot:message", message.From.ID, messageBudget) {
		app.notifyRateLimited(ctx, client, message.Chat.ID, app.locale(ctx, message.From.ID, message.From.LanguageCode))
		return
	}
	locale := app.locale(ctx, message.From.ID, message.From.LanguageCode)
	_, _ = client.SendChatAction(ctx, &telegram.SendChatActionParams{ChatID: message.Chat.ID, Action: models.ChatActionTyping})
	payload := parseStartPayload(message.Text)
	switch payload.Kind {
	case startPayloadReferral:
		app.attributeReferral(ctx, message.From.ID, message.From.LanguageCode, payload.Value)
	case startPayloadLogin:
		// The web sign-in page sends a customer here with `?start=login`. It has
		// to behave exactly like /login, or the button on the website is a dead
		// end that opens the menu.
		app.HandleWebLogin(ctx, client, update)
		return
	case startPayloadPay:
		if app.commerceEnabled() {
			app.handleStartPayment(ctx, client, update, locale, payload.Value)
			return
		}
	}
	view := app.loadView(ctx, message.From.ID, locale, routeHome)
	if _, err := client.SendMessage(ctx, sendParams(message.Chat.ID, view)); err != nil {
		app.withCorrelation(update).Error("telegram view send failed", "view", routeHome, "error", err)
	}
}

// startPayload is the argument Telegram appends to /start from a deep link.
type startPayload struct {
	Kind  string
	Value string
}

// The deep-link payloads the bot understands. Anything else opens the menu.
const (
	startPayloadReferral = "ref"
	startPayloadLogin    = "login"
	startPayloadPay      = "pay"
)

// parseStartPayload reads the deep-link argument of a /start command.
//
// Three forms are recognised: `ref_<code>` records referral attribution,
// `login` mints a web sign-in link, and `pay_<orderID>` resumes a Telegram
// Stars payment the customer began in the browser. The order identifier has to
// parse as a UUID so that whatever else a link carries is never used to
// address an order.
func parseStartPayload(messageText string) startPayload {
	parts := strings.Fields(messageText)
	if len(parts) != 2 {
		return startPayload{}
	}
	argument := parts[1]
	switch {
	case argument == "login":
		return startPayload{Kind: startPayloadLogin}
	case strings.HasPrefix(argument, "ref_"):
		if code := strings.TrimPrefix(argument, "ref_"); code != "" {
			return startPayload{Kind: startPayloadReferral, Value: code}
		}
	case strings.HasPrefix(argument, "pay_"):
		if parsed, err := databaseutil.ParseUUIDs([]string{strings.TrimPrefix(argument, "pay_")}); err == nil {
			return startPayload{Kind: startPayloadPay, Value: databaseutil.UUIDStrings(parsed)[0]}
		}
	}
	return startPayload{}
}

// handleStartPayment resumes a payment the customer started on the web.
//
// The browser cannot send a Telegram Stars invoice; only the bot can, into the
// chat of the account that owns the order. So the web hands the customer to
// `t.me/<bot>?start=pay_<orderID>`, and this is the other end: for the caller's
// own order that is still open, priced in Stars, with something left to pay and
// no settled payment, the invoice is sent and the order screen follows. In any
// other state only the order screen is shown. An order that is not the
// caller's renders the home screen — "not yours" and "does not exist" must look
// the same from the outside.
func (app *App) handleStartPayment(ctx context.Context, client *telegram.Bot, update *models.Update, locale Locale, orderID string) {
	message := update.Message
	logger := app.withCorrelation(update).With("action", "start-pay")
	session, err := app.commerceContext(ctx, message.From.ID, message.From.LanguageCode, message.From.Username)
	if err != nil {
		logger.Error("customer resolution failed", "error", err)
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, app.errorView(locale, routeHome)))
		return
	}
	order, err := app.customers.Order(ctx, session.Customer.ID, orderID, session.Locale)
	if errors.Is(err, ErrOrderNotFound) {
		view := app.loadView(ctx, message.From.ID, session.Locale, routeHome)
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, view))
		return
	}
	if err != nil {
		logger.Error("order lookup failed", "error", err)
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, app.errorView(session.Locale, routeOrders)))
		return
	}
	if starsInvoiceDue(order) {
		// The invoice settles against a payment intent, so one is created or
		// resumed first. Both steps are idempotent: a second tap on the link
		// finds the same intent and sends a second copy of the same invoice,
		// and Telegram's pre-checkout probe refuses an order that was paid
		// in between.
		if _, err = app.commerce.StartPayment(ctx, order, "telegram_stars", session.TelegramID, order.PlanName); err != nil {
			logger.Error("Telegram Stars payment could not be resumed", "error", err)
			_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: text(session.Locale, "error.payment")}))
		} else if err = app.StarsInvoice(ctx, client, session, message.Chat.ID, order.ID); err != nil {
			logger.Error("Telegram Stars invoice failed", "error", err)
			_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: text(session.Locale, "error.payment")}))
		}
	}
	view := app.orderScreen(ctx, session, order.ID)
	if _, err := client.SendMessage(ctx, sendParams(message.Chat.ID, view)); err != nil {
		logger.Error("telegram view send failed", "view", "order", "error", err)
	}
}

// starsInvoiceDue reports whether an order can still be paid with a Telegram
// Stars invoice: open, priced in Stars, with an external amount left, and no
// payment already settled against it.
func starsInvoiceDue(order OrderSummary) bool {
	open := order.State == commerce.OrderPending || order.State == commerce.OrderDraft
	return open && order.Currency == "XTR" && order.ExternalMinor > 0 && order.PaymentStatus != "succeeded"
}

// attributeReferral records the immutable inviter/invitee pair. With commerce
// enabled the canonical customer owns the attribution; otherwise the v0.2
// Telegram-keyed path still applies.
func (app *App) attributeReferral(ctx context.Context, telegramID int64, telegramLocale, code string) {
	if app.commerceEnabled() {
		session, err := app.commerceContext(ctx, telegramID, telegramLocale)
		if err == nil {
			if err = app.customers.AttributeReferralForCustomer(ctx, session.Customer.ID, code); err != nil {
				app.logger.Warn("referral attribution failed", "error", err)
			}
			return
		}
		app.logger.Warn("customer resolution failed during referral attribution", "error", err)
	}
	if err := app.store.AttributeReferral(ctx, telegramID, code); err != nil {
		app.logger.Warn("referral attribution failed", "error", err)
	}
}

func (app *App) HandleDefault(ctx context.Context, client *telegram.Bot, update *models.Update) {
	if update.CallbackQuery != nil {
		app.HandleCallback(ctx, client, update)
		return
	}
	if update.PreCheckoutQuery != nil {
		app.HandlePreCheckout(ctx, client, update.PreCheckoutQuery)
		return
	}
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != models.ChatTypePrivate {
		return
	}
	if message.SuccessfulPayment != nil {
		app.HandleSuccessfulPayment(ctx, client, update)
		return
	}
	if strings.HasPrefix(message.Text, "/") {
		app.HandleStart(ctx, client, update)
		return
	}
	if !app.allow(ctx, "bot:message", message.From.ID, messageBudget) {
		app.notifyRateLimited(ctx, client, message.Chat.ID, app.locale(ctx, message.From.ID, message.From.LanguageCode))
		return
	}
	if app.commerceEnabled() {
		if handled := app.handleFlowMessage(ctx, client, update); handled {
			return
		}
	}
	state, err := app.store.Session(ctx, message.From.ID)
	if err != nil {
		app.logger.Error("telegram session lookup failed", "error", err)
	}
	if state == "support_message" {
		locale := app.locale(ctx, message.From.ID, message.From.LanguageCode)
		if err := app.store.SubmitSupport(ctx, message.From.ID, message.ID, message.Text); err != nil {
			_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, supportSubmitErrorView(locale)))
			return
		}
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, supportSubmittedView(locale)))
		return
	}
	app.HandleStart(ctx, client, update)
}

// handleFlowMessage completes a multi-step flow — promo entry or a support
// message with attachments. It reports whether it consumed the update.
func (app *App) handleFlowMessage(ctx context.Context, client *telegram.Bot, update *models.Update) bool {
	message := update.Message
	state, flowContext, err := app.customers.SessionState(ctx, message.From.ID)
	if err != nil {
		app.logger.Error("bot session lookup failed", "error", err)
		return false
	}
	switch state {
	case "promo_code", "support_reply", "topup_amount", "subscription_label",
		"goods_recipient", "goods_promo", "gift_message", "gift_claim":
	default:
		return false
	}
	session, err := app.commerceContext(ctx, message.From.ID, message.From.LanguageCode, message.From.Username)
	if err != nil {
		app.logger.Error("customer resolution failed", "error", err)
		return false
	}
	switch state {
	case "promo_code":
		app.completePromoEntry(ctx, client, session, message)
	case "support_reply":
		ticketID, _ := flowContext["ticketId"].(string)
		app.completeSupportMessage(ctx, client, session, message, ticketID)
	case "topup_amount":
		if !app.allow(ctx, "bot:checkout", session.TelegramID, checkoutBudget) {
			_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: text(session.Locale, "menu.rateLimit")}))
			return true
		}
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, app.completeTopUpAmount(ctx, session, message.Text)))
	case "subscription_label":
		subscriptionID, _ := flowContext["subscriptionId"].(string)
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, app.completeSubscriptionRename(ctx, session, subscriptionID, message.Text)))
	case "goods_recipient":
		productID, _ := flowContext["productId"].(string)
		if err := app.customers.CancelSession(ctx, session.TelegramID); err != nil {
			app.logger.Warn("session cleanup failed", "error", err)
		}
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID,
			app.SubmitShopRecipient(ctx, session, productID, message.Text)))
	case "goods_promo":
		productID, _ := flowContext["productId"].(string)
		recipient, _ := flowContext["recipient"].(string)
		if err := app.customers.CancelSession(ctx, session.TelegramID); err != nil {
			app.logger.Warn("session cleanup failed", "error", err)
		}
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID,
			app.SubmitShopPromo(ctx, session, productID, recipient, message.Text)))
	case "gift_message":
		token, _ := flowContext["gift"].(string)
		if err := app.customers.CancelSession(ctx, session.TelegramID); err != nil {
			app.logger.Warn("session cleanup failed", "error", err)
		}
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID,
			app.SubmitGiftMessage(ctx, session, token, message.Text)))
	case "gift_claim":
		if err := app.customers.CancelSession(ctx, session.TelegramID); err != nil {
			app.logger.Warn("session cleanup failed", "error", err)
		}
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID,
			app.SubmitGiftCode(ctx, session, message.Text)))
	}
	return true
}

func (app *App) completePromoEntry(ctx context.Context, client *telegram.Bot, session commerceContext, message *models.Message) {
	if !app.allow(ctx, "bot:promo", session.TelegramID, promoBudget) {
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: text(session.Locale, "promo.rateLimited")}))
		return
	}
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		_ = app.customers.CancelSession(ctx, session.TelegramID)
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}))
		return
	}
	updated, err := app.commerce.ApplyPromo(ctx, checkout, message.Text)
	if err != nil {
		app.logger.Error("promo validation failed", "error", err)
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: text(session.Locale, "error.generic")}))
		return
	}
	if err = app.customers.CancelSession(ctx, session.TelegramID); err != nil {
		app.logger.Warn("session cleanup failed", "error", err)
	}
	if updated.PromoRejection != "" {
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: text(session.Locale, "promo.rejected", text(session.Locale, "promo."+updated.PromoRejection))}))
	} else {
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: text(session.Locale, "promo.applied", updated.PromoCode)}))
	}
	_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, app.checkoutScreen(ctx, session)))
}

func (app *App) completeSupportMessage(ctx context.Context, client *telegram.Bot, session commerceContext, message *models.Message, ticketID string) {
	body := message.Text
	if body == "" {
		body = message.Caption
	}
	attachments, err := collectAttachments(message)
	if err != nil {
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: attachmentErrorText(session.Locale, err)}))
		return
	}
	resolved, err := app.customers.AppendCustomerMessage(ctx, session.Customer.ID, ticketID, firstLine(body), body, message.ID, attachments)
	switch {
	case errors.Is(err, ErrTicketClosed):
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: text(session.Locale, "support.closedHint")}))
		return
	case errors.Is(err, ErrTicketNotFound):
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: text(session.Locale, "support.notFound")}))
		return
	case errors.Is(err, ErrAttachmentTooBig), errors.Is(err, ErrAttachmentKind):
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: attachmentErrorText(session.Locale, err)}))
		return
	case err != nil:
		app.logger.Error("support message could not be stored", "error", err)
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: text(session.Locale, "support.tooLong")}))
		return
	}
	if err = app.customers.CancelSession(ctx, session.TelegramID); err != nil {
		app.logger.Warn("session cleanup failed", "error", err)
	}
	_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{Text: text(session.Locale, "support.sent"), Keyboard: keyboard(row(actionButton(text(session.Locale, "support.open"), "ticket:"+resolved)))}))
}

func attachmentErrorText(locale Locale, err error) string {
	switch {
	case errors.Is(err, ErrAttachmentTooBig):
		return text(locale, "support.tooBig")
	case errors.Is(err, ErrAttachmentKind):
		return text(locale, "support.badKind")
	default:
		return text(locale, "support.tooLong")
	}
}

// collectAttachments extracts the Telegram file references a customer attached.
// Only the reference and safe metadata are kept; Omniflow never downloads or
// stores the file itself.
func collectAttachments(message *models.Message) ([]Attachment, error) {
	attachments := make([]Attachment, 0, 2)
	if len(message.Photo) > 0 {
		largest := message.Photo[len(message.Photo)-1]
		attachments = append(attachments, Attachment{Kind: "photo", TelegramFileID: largest.FileID, SizeBytes: int64(largest.FileSize)})
	}
	if message.Document != nil {
		attachments = append(attachments, Attachment{
			Kind: "document", TelegramFileID: message.Document.FileID,
			FileName: message.Document.FileName, MimeType: message.Document.MimeType,
			SizeBytes: message.Document.FileSize,
		})
	}
	if message.Video != nil || message.Audio != nil || message.Voice != nil || message.Animation != nil || message.Sticker != nil {
		return nil, ErrAttachmentKind
	}
	for _, attachment := range attachments {
		if err := attachment.Validate(); err != nil {
			return nil, err
		}
	}
	return attachments, nil
}

func (app *App) notifyRateLimited(ctx context.Context, client *telegram.Bot, chatID int64, locale Locale) {
	if _, err := client.SendMessage(ctx, sendParams(chatID, View{Text: text(locale, "menu.rateLimit")})); err != nil {
		app.logger.Debug("rate-limit notice delivery failed", "error", err)
	}
}

func (app *App) HandleSettings(ctx context.Context, client *telegram.Bot, update *models.Update) {
	app.handleCommandRoute(ctx, client, update, routeSettings)
}

func (app *App) HandleSupport(ctx context.Context, client *telegram.Bot, update *models.Update) {
	app.handleCommandRoute(ctx, client, update, routeSupport)
}

func (app *App) HandlePlans(ctx context.Context, client *telegram.Bot, update *models.Update) {
	app.handleCommandRoute(ctx, client, update, routePlans)
}

func (app *App) HandleOrders(ctx context.Context, client *telegram.Bot, update *models.Update) {
	app.handleCommandRoute(ctx, client, update, routeOrders)
}

func (app *App) HandleWallet(ctx context.Context, client *telegram.Bot, update *models.Update) {
	app.handleCommandRoute(ctx, client, update, routeWallet)
}

func (app *App) HandleCancel(ctx context.Context, client *telegram.Bot, update *models.Update) {
	if update.Message == nil || update.Message.From == nil {
		return
	}
	_ = app.store.CancelSession(ctx, update.Message.From.ID)
	app.handleCommandRoute(ctx, client, update, routeHome)
}

func (app *App) handleCommandRoute(ctx context.Context, client *telegram.Bot, update *models.Update, route string) {
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != models.ChatTypePrivate {
		return
	}
	locale := app.locale(ctx, message.From.ID, message.From.LanguageCode)
	_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, app.loadView(ctx, message.From.ID, locale, route)))
}

func (app *App) HandleCallback(ctx context.Context, client *telegram.Bot, update *models.Update) {
	query := update.CallbackQuery
	if query == nil {
		return
	}
	locale := app.locale(ctx, query.From.ID, query.From.LanguageCode)
	if !app.allow(ctx, "bot:nav", query.From.ID, callbackBudget) {
		_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{CallbackQueryID: query.ID, Text: text(locale, "menu.rateLimit"), ShowAlert: true})
		return
	}
	if query.Message.Message == nil || query.Message.Message.Chat.Type != models.ChatTypePrivate {
		_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{CallbackQueryID: query.ID})
		return
	}
	message := query.Message.Message
	// A tap while a free-text prompt is open is the customer leaving it — every
	// prompt's Cancel button is a callback, and so is any other button they
	// might reach for instead. The open flow is discarded before the tap is
	// handled, so the next ordinary message is not parsed as an amount, a
	// promo code, a gift code, or a name for half an hour. A button that opens
	// a prompt re-creates the state afterwards, so nothing is lost there.
	if err := app.store.CancelSession(ctx, query.From.ID); err != nil {
		app.logger.Warn("flow state cleanup failed", "error", err)
	}
	if strings.HasPrefix(query.Data, actionPrefix) {
		action := strings.SplitN(strings.TrimPrefix(query.Data, actionPrefix), ":", 2)[0]
		// A consequential action is claimed once per callback, so a redelivered
		// update or a double tap cannot repeat a payment or a deletion.
		if consequentialActions[action] && !app.claimCallback(ctx, query.ID) {
			_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{CallbackQueryID: query.ID, Text: text(locale, "menu.replay")})
			return
		}
		if checkoutBudgetActions[action] && !app.allow(ctx, "bot:checkout", query.From.ID, checkoutBudget) {
			_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{CallbackQueryID: query.ID, Text: text(locale, "menu.rateLimit"), ShowAlert: true})
			return
		}
		_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{CallbackQueryID: query.ID})
		app.handleAction(ctx, client, query, message.Chat.ID, message.ID, locale, update)
		return
	}
	_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{CallbackQueryID: query.ID})
	route := strings.TrimPrefix(query.Data, callbackPrefix)
	if !knownRoute(route) {
		route = routeHome
	}
	if _, err := client.EditMessageText(ctx, editParams(message.Chat.ID, message.ID, loadingView(locale))); err != nil {
		app.logger.Debug("telegram loading state edit failed", "error", err)
	}
	view := app.loadView(ctx, query.From.ID, locale, route)
	if _, err := client.EditMessageText(ctx, editParams(message.Chat.ID, message.ID, view)); err != nil {
		app.withCorrelation(update).Error("telegram view edit failed", "view", route, "error", err)
	}
}

func (app *App) handleAction(ctx context.Context, client *telegram.Bot, query *models.CallbackQuery, chatID int64, messageID int, locale Locale, update *models.Update) {
	parts := strings.Split(strings.TrimPrefix(query.Data, actionPrefix), ":")
	if handled := app.dispatchCommerceAction(ctx, client, query, chatID, messageID, parts, update); handled {
		return
	}
	var view View
	var err error
	switch parts[0] {
	case "support":
		err = app.store.BeginSupport(ctx, query.From.ID)
		view = supportComposeView(locale)
	case "lang":
		if len(parts) != 2 {
			err = errors.New("missing locale")
			break
		}
		err = app.setLocalePreference(ctx, query.From.ID, query.From.LanguageCode, parts[1])
		locale = app.locale(ctx, query.From.ID, query.From.LanguageCode)
		view = app.loadView(ctx, query.From.ID, locale, routeSettings)
	case "notify":
		if len(parts) != 2 {
			err = errors.New("missing notification kind")
			break
		}
		err = app.toggleNotification(ctx, query.From.ID, query.From.LanguageCode, parts[1])
		view = app.loadView(ctx, query.From.ID, locale, routeSettings)
	case "device-confirm":
		view = deviceDeleteConfirmView(locale, actionIndex(parts), false)
	case "devices-confirm":
		view = deviceDeleteConfirmView(locale, 0, true)
	case "device-delete":
		err = app.deleteDevice(ctx, query.From.ID, actionIndex(parts))
		view = app.loadView(ctx, query.From.ID, locale, routeDevices)
	case "devices-delete":
		var userID int64
		userID, err = app.store.RemnawaveUserID(ctx, query.From.ID)
		if err == nil {
			err = app.remnawave.DeleteAllDevices(ctx, userID)
		}
		view = app.loadView(ctx, query.From.ID, locale, routeDevices)
	case "revoke-confirm":
		view = revokeConfirmView(locale)
	case "revoke":
		var userID int64
		userID, err = app.store.RemnawaveUserID(ctx, query.From.ID)
		if err == nil {
			err = app.remnawave.RevokeSubscription(ctx, userID)
		}
		view = app.loadView(ctx, query.From.ID, locale, routeSubscription)
	default:
		err = errors.New("unknown action")
	}
	if err != nil {
		app.logger.Error("telegram action failed", "action", parts[0], "error", err)
		view = actionErrorView(locale)
	}
	if _, editErr := client.EditMessageText(ctx, editParams(chatID, messageID, view)); editErr != nil {
		app.logger.Error("telegram action view edit failed", "error", editErr)
	}
}

// setLocalePreference stores the interface language against the canonical
// customer when commerce is enabled, and against the Telegram-linked account
// otherwise.
func (app *App) setLocalePreference(ctx context.Context, telegramID int64, telegramLocale, locale string) error {
	if !app.commerceEnabled() {
		return app.store.SetLocale(ctx, telegramID, locale)
	}
	session, err := app.commerceContext(ctx, telegramID, telegramLocale)
	if err != nil {
		return err
	}
	return app.customers.SetCustomerLocale(ctx, session.Customer.ID, locale)
}

func (app *App) toggleNotification(ctx context.Context, telegramID int64, telegramLocale, kind string) error {
	if !app.commerceEnabled() {
		return app.store.ToggleNotification(ctx, telegramID, kind)
	}
	session, err := app.commerceContext(ctx, telegramID, telegramLocale)
	if err != nil {
		return err
	}
	_, err = app.customers.ToggleCustomerNotification(ctx, session.Customer.ID, kind)
	return err
}

func (app *App) deleteDevice(ctx context.Context, telegramID int64, index int) error {
	userID, err := app.store.RemnawaveUserID(ctx, telegramID)
	if err != nil {
		return err
	}
	devices, err := app.remnawave.Devices(ctx, userID)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(devices.Devices) {
		return errors.New("device list changed; refresh and try again")
	}
	return app.remnawave.DeleteDevice(ctx, userID, devices.Devices[index].HWID)
}

func actionIndex(parts []string) int {
	if len(parts) != 2 {
		return -1
	}
	value, err := strconv.Atoi(parts[1])
	if err != nil {
		return -1
	}
	return value
}

func (app *App) loadView(ctx context.Context, telegramID int64, locale Locale, route string) View {
	if app.commerceEnabled() {
		return app.loadCustomerView(ctx, telegramID, locale, route)
	}
	if commerceOnlyRoute(route) {
		return View{Text: text(locale, "menu.unavail"), Keyboard: keyboard(row(callbackButton(text(locale, "action.back"), routeHome)))}
	}
	if route == routeSupport {
		return supportView(locale, app.supportURL)
	}
	return app.loadRemnawaveView(ctx, telegramID, locale, route, MenuState{})
}

// loadCustomerView renders a screen for an installation with commerce enabled.
// The canonical customer is resolved first, so purchase, order, wallet, support,
// and news screens work before Remnawave has ever provisioned a VPN user.
func (app *App) loadCustomerView(ctx context.Context, telegramID int64, locale Locale, route string) View {
	session, err := app.commerceContext(ctx, telegramID, string(locale))
	if err != nil {
		app.logger.Error("customer resolution failed", "error", err)
		return app.errorView(locale, route)
	}
	if route == routeSettings {
		return app.settingsScreen(ctx, session)
	}
	if view, handled := app.loadCommerceView(ctx, session, route); handled {
		return view
	}
	if route == routeConnect {
		subscription, subscriptionErr := app.subscriptionFor(ctx, session)
		if subscriptionErr != nil {
			app.logger.Error("Remnawave subscription lookup failed", "error", subscriptionErr)
			return app.errorView(session.Locale, routeConnect)
		}
		return app.connectPlatformsScreen(ctx, session.Locale, subscription)
	}
	menu := app.menuState(ctx, session.Customer.ID, session.Locale)
	if session.Customer.RemnawaveID <= 0 {
		return app.noSubscriptionView(ctx, session, route, menu)
	}
	return app.loadRemnawaveView(ctx, telegramID, session.Locale, route, menu)
}

// noSubscriptionView is what a customer sees before their first purchase: the
// full purchase surface, not a dead end.
func (app *App) noSubscriptionView(ctx context.Context, session commerceContext, route string, menu MenuState) View {
	entitlement, err := app.customers.Entitlement(ctx, session.Customer.ID, session.Locale, app.settings.Currency)
	if err != nil {
		app.logger.Error("entitlement lookup failed", "error", err)
		return app.errorView(session.Locale, route)
	}
	notice := text(session.Locale, "life.none")
	if entitlement.Found {
		phase := commerce.EvaluatePhase(time.Now().UTC(), commerce.Subscription{Status: entitlement.Status, EndsAt: entitlement.EndsAt, GracePeriod: entitlement.GracePeriod})
		if explanation := lifecycleNotice(session.Locale, phase, entitlement); explanation != "" {
			notice = explanation
		}
	}
	return View{
		Text:       "🌊 <b>Omniflow</b>\n\n" + notice,
		Keyboard:   commerceMainKeyboard(session.Locale, menu),
		RetryRoute: routeHome,
	}
}

func (app *App) loadRemnawaveView(ctx context.Context, telegramID int64, locale Locale, route string, menu MenuState) View {
	userID, err := app.store.RemnawaveUserID(ctx, telegramID)
	if errors.Is(err, ErrNotLinked) {
		user, lookupErr := app.remnawave.UserByTelegramID(ctx, telegramID)
		if errors.Is(lookupErr, remnawave.ErrNotFound) {
			return notLinkedView(locale, app.supportURL)
		}
		if lookupErr != nil {
			app.logger.Error("Remnawave Telegram identity lookup failed", "error", lookupErr)
			return errorView(locale, route)
		}
		userID, err = app.store.Link(ctx, telegramID, user.ID)
		if err != nil {
			app.logger.Error("Telegram identity link failed", "error", err)
			return errorView(locale, route)
		}
	}
	if err != nil {
		app.logger.Error("telegram identity lookup failed", "error", err)
		return errorView(locale, route)
	}

	switch route {
	case routeSettings:
		preferences, preferenceErr := app.store.Preferences(ctx, telegramID)
		if preferenceErr != nil {
			return errorView(locale, route)
		}
		return settingsView(locale, preferences)
	case routeReferral:
		code, count, referralErr := app.store.Referral(ctx, telegramID)
		if referralErr != nil {
			return errorView(locale, route)
		}
		return referralView(locale, app.botUsername, code, count)
	case routeSubscription, routeConnect:
		subscription, err := app.remnawave.Subscription(ctx, userID)
		if err != nil {
			app.logger.Error("Remnawave subscription lookup failed", "error", err)
			return errorView(locale, route)
		}
		if route == routeConnect {
			return connectView(locale, subscription)
		}
		return subscriptionView(locale, subscription)
	case routeDevices:
		user, err := app.remnawave.User(ctx, userID)
		if err != nil {
			app.logger.Error("Remnawave user lookup failed", "error", err)
			return errorView(locale, route)
		}
		devices, err := app.remnawave.Devices(ctx, userID)
		if err != nil {
			app.logger.Error("Remnawave device lookup failed", "error", err)
			return errorView(locale, route)
		}
		return devicesView(locale, devices, user.HWIDDeviceLimit)
	default:
		user, err := app.remnawave.User(ctx, userID)
		if err != nil {
			app.logger.Error("Remnawave user lookup failed", "error", err)
			return errorView(locale, routeHome)
		}
		return homeMenuView(locale, user, menu, app.lifecycleNoticeFor(ctx, telegramID, locale, user))
	}
}

// lifecycleNoticeFor explains a grace period, a limited state, or a stalled
// provisioning run on the account screen. It is empty when the subscription is
// simply healthy.
func (app *App) lifecycleNoticeFor(ctx context.Context, telegramID int64, locale Locale, user remnawave.User) string {
	if !app.commerceEnabled() {
		return ""
	}
	session, err := app.commerceContext(ctx, telegramID, string(locale))
	if err != nil {
		return ""
	}
	entitlement, err := app.customers.Entitlement(ctx, session.Customer.ID, locale, app.settings.Currency)
	if err != nil || !entitlement.Found {
		return ""
	}
	phase := commerce.EvaluatePhase(time.Now().UTC(), commerce.Subscription{
		Status: entitlement.Status, EndsAt: entitlement.EndsAt, GracePeriod: entitlement.GracePeriod,
		TrafficUsedBytes: user.Traffic.UsedBytes, TrafficLimitBytes: user.TrafficLimitBytes,
	})
	return lifecycleNotice(locale, phase, entitlement)
}

// commerceOnlyRoute reports whether a route needs the commerce surface. Without
// it the bot explains that purchases are not configured rather than failing.
func commerceOnlyRoute(route string) bool {
	switch route {
	case routePlans, routeOrders, routeWallet, routeNews, routeAutoRenew, routeMethods, routeShop, routeShopOrders, routeGifts, routeOffers, routeTopUp, routeCart, routeSubscriptions:
		return true
	default:
		return false
	}
}

func knownRoute(route string) bool {
	switch route {
	case routeHome, routeSubscription, routeConnect, routeDevices, routeSupport, routeSettings, routeReferral,
		routePlans, routeOrders, routeWallet, routeNews, routeAutoRenew, routeMethods, routeShop, routeShopOrders, routeGifts, routeOffers,
		routeTopUp, routeCart, routeSubscriptions:
		return true
	default:
		return false
	}
}

func (app *App) locale(ctx context.Context, telegramID int64, telegramLocale string) Locale {
	if app.commerceEnabled() {
		if session, err := app.commerceContext(ctx, telegramID, telegramLocale); err == nil {
			return session.Locale
		}
	}
	preferences, err := app.store.Preferences(ctx, telegramID)
	if err == nil {
		switch preferences.Locale {
		case "ru":
			return LocaleRussian
		case "en":
			return LocaleEnglish
		}
	}
	return localeFrom(telegramLocale)
}

func referralURL(username, code string) string {
	if username == "" || code == "" {
		return ""
	}
	return fmt.Sprintf("https://t.me/%s?start=%s", url.PathEscape(username), url.QueryEscape("ref_"+code))
}

// sendParams builds one outbound message.
//
// The keyboard is assigned only when there is one, and that conditional is the
// whole point of it. `ReplyMarkup` is an `any`, so a nil `*InlineKeyboardMarkup`
// assigned to it is a non-nil interface holding a nil pointer, which marshals to
// `"reply_markup": null` — and Telegram answers `Bad Request: object expected as
// reply markup` and drops the message.
//
// Every message the bot sends without a keyboard went out that way: the web
// sign-in link, all four of its notices, the rate-limit and promo replies, the
// support hints, and the operator's test notification. None of them were ever
// delivered, on any installation, and the failure was invisible because these
// are precisely the sends whose error was discarded.
func sendParams(chatID int64, view View) *telegram.SendMessageParams {
	disabled := true
	params := &telegram.SendMessageParams{
		ChatID:             chatID,
		Text:               view.Text,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disabled},
		ProtectContent:     true,
	}
	if view.Keyboard != nil {
		params.ReplyMarkup = view.Keyboard
	}
	return params
}

// editParams rewrites a message in place, with the same nil-keyboard rule as
// sendParams: an edit that sent `"reply_markup": null` would be refused rather
// than clearing the keyboard.
func editParams(chatID int64, messageID int, view View) *telegram.EditMessageTextParams {
	disabled := true
	params := &telegram.EditMessageTextParams{
		ChatID:             chatID,
		MessageID:          messageID,
		Text:               view.Text,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disabled},
	}
	if view.Keyboard != nil {
		params.ReplyMarkup = view.Keyboard
	}
	return params
}
