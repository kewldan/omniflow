package botapp

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/payments"
)

// phrase is one message in both supported interface languages. Keeping the pair
// together makes a missing translation impossible to merge.
type phrase struct{ ru, en string }

func (message phrase) get(locale Locale) string {
	if locale == LocaleRussian {
		return message.ru
	}
	return message.en
}

// text renders a catalog message. An unknown key returns the key itself, which
// is visible in review and in tests rather than silently rendering an empty
// screen.
func text(locale Locale, key string, args ...any) string {
	message, found := catalog[key]
	if !found {
		return key
	}
	rendered := message.get(locale)
	if len(args) == 0 {
		return rendered
	}
	return fmt.Sprintf(rendered, args...)
}

// catalog holds every v0.4 customer-facing string. Success, empty, pending, and
// failure states are all represented here in Russian and English.
var catalog = map[string]phrase{
	// Navigation and shared actions.
	"action.back":     {ru: "‹ Назад", en: "‹ Back"},
	"action.menu":     {ru: "‹ В меню", en: "‹ Menu"},
	"action.refresh":  {ru: "🔄 Обновить", en: "🔄 Refresh"},
	"action.retry":    {ru: "🔄 Повторить", en: "🔄 Try again"},
	"action.cancel":   {ru: "Отмена", en: "Cancel"},
	"action.confirm":  {ru: "Подтвердить", en: "Confirm"},
	"action.continue": {ru: "Продолжить", en: "Continue"},
	"action.terms":    {ru: "📄 Условия обслуживания", en: "📄 Terms of service"},

	// Main menu additions.
	"menu.plans":   {ru: "🛒 Тарифы", en: "🛒 Plans"},
	"menu.orders":  {ru: "🧾 Мои заказы", en: "🧾 My orders"},
	"menu.wallet":  {ru: "👛 Кошелёк", en: "👛 Wallet"},
	"menu.news":    {ru: "📰 Новости", en: "📰 News"},
	"menu.support": {ru: "💬 Поддержка", en: "💬 Support"},
	"menu.renew":   {ru: "♻️ Продлить", en: "♻️ Renew"},
	"menu.upgrade": {ru: "⬆️ Сменить тариф", en: "⬆️ Change plan"},
	"menu.shop":    {ru: "🛍 Магазин", en: "🛍 Shop"},
	"menu.gifts":   {ru: "🎁 Подарки", en: "🎁 Gifts"},
	"menu.offers":  {ru: "✨ Ваше предложение", en: "✨ Your offer"},
	"menu.badge":   {ru: " · %d новых", en: " · %d new"},
	"menu.unavail": {ru: "⚠️ <b>Раздел недоступен</b>\n\nПокупки ещё не настроены администратором сервиса. Обратитесь в поддержку.", en: "⚠️ <b>This section is unavailable</b>\n\nPurchases are not configured yet. Please contact support."},
	"menu.rateLimit": {ru: "⏳ Слишком много запросов. Подождите немного и попробуйте снова.",
		en: "⏳ Too many requests. Please wait a moment and try again."},
	"menu.replay": {ru: "Это действие уже выполнено.", en: "This action has already been completed."},

	// Customer web sign-in. The link is a bearer credential for ten minutes, so
	// the copy says so plainly rather than leaving the customer to guess whether
	// forwarding it is safe.
	"weblogin.link": {
		ru: "🔐 <b>Вход в личный кабинет</b>\n\nСсылка действует 10 минут и сработает один раз. " +
			"Не пересылайте её — тот, кто откроет ссылку, войдёт в ваш аккаунт.\n\n%s",
		en: "🔐 <b>Sign in to your account</b>\n\nThis link works once and expires in 10 minutes. " +
			"Do not forward it — whoever opens it signs in as you.\n\n%s"},
	"weblogin.unavailable": {
		ru: "Вход на сайте через ссылку сейчас недоступен.",
		en: "Link sign-in on the website is not available right now."},
	"weblogin.throttled": {
		ru: "Вы запросили слишком много ссылок. Попробуйте позже.",
		en: "Too many sign-in links requested. Please try again later."},
	"weblogin.failed": {
		ru: "Не удалось создать ссылку. Попробуйте ещё раз.",
		en: "The sign-in link could not be created. Please try again."},

	// Catalog.
	"plans.title": {ru: "🛒 <b>Тарифы</b>\n\nВыберите тариф. Все цены указаны за один период и без скрытых платежей.",
		en: "🛒 <b>Plans</b>\n\nChoose a plan. Every price covers one full period with no hidden fees."},
	"plans.empty": {ru: "🛒 <b>Тарифы</b>\n\nСейчас нет доступных тарифов в валюте %s. Загляните позже или напишите в поддержку.",
		en: "🛒 <b>Plans</b>\n\nNo plans are available in %s right now. Check back later or contact support."},
	"plans.row":        {ru: "%s · %s", en: "%s · %s"},
	"plans.duration":   {ru: "⏳ Период: <b>%s</b>", en: "⏳ Period: <b>%s</b>"},
	"plans.traffic":    {ru: "📡 Трафик: <b>%s</b>", en: "📡 Traffic: <b>%s</b>"},
	"plans.devices":    {ru: "📱 Устройства: <b>%s</b>", en: "📱 Devices: <b>%s</b>"},
	"plans.price":      {ru: "💳 Цена: <b>%s</b>", en: "💳 Price: <b>%s</b>"},
	"plans.unlimited":  {ru: "безлимит", en: "unlimited"},
	"plans.trialBadge": {ru: "🎁 Пробный период", en: "🎁 Free trial"},
	"plans.compare":    {ru: "Сравнение тарифов", en: "Plan comparison"},

	// Plan details.
	"plan.title":              {ru: "📦 <b>%s</b>", en: "📦 <b>%s</b>"},
	"plan.buy":                {ru: "🛒 Купить", en: "🛒 Buy"},
	"plan.renew":              {ru: "♻️ Продлить этот тариф", en: "♻️ Extend this plan"},
	"plan.upgrade":            {ru: "⬆️ Перейти на этот тариф", en: "⬆️ Switch to this plan"},
	"plan.downgrade":          {ru: "⬇️ Перейти на этот тариф", en: "⬇️ Switch to this plan"},
	"plan.trial":              {ru: "🎁 Активировать пробный период", en: "🎁 Start free trial"},
	"plan.policyUpgrade":      {ru: "Смена тарифа вверх: <b>%s</b>", en: "Upgrade policy: <b>%s</b>"},
	"plan.policyDowngrade":    {ru: "Смена тарифа вниз: <b>%s</b>", en: "Downgrade policy: <b>%s</b>"},
	"plan.policyCancellation": {ru: "Отмена: <b>%s</b>", en: "Cancellation: <b>%s</b>"},
	"plan.grace":              {ru: "Льготный период после окончания: <b>%s</b>", en: "Grace period after expiry: <b>%s</b>"},
	"plan.terms":              {ru: "\n\nПродолжая, вы соглашаетесь с условиями обслуживания.", en: "\n\nBy continuing you accept the terms of service."},
	"plan.gone":               {ru: "⚠️ <b>Тариф больше недоступен</b>\n\nКаталог изменился. Откройте список тарифов заново.", en: "⚠️ <b>This plan is no longer available</b>\n\nThe catalog changed. Open the plan list again."},

	// Policy labels.
	"policy.forbid":    {ru: "недоступно", en: "not available"},
	"policy.replace":   {ru: "новый период с текущей даты", en: "a new period starting today"},
	"policy.extend":    {ru: "добавляется к текущему сроку", en: "added to the current period"},
	"policy.immediate": {ru: "сразу", en: "immediately"},
	"policy.atExpiry":  {ru: "по окончании текущего периода", en: "at the end of the current period"},

	// Payment method selection.
	"pay.choose": {ru: "💳 <b>Способ оплаты</b>\n\nДоступны только подключённые способы, которые поддерживают валюту тарифа.",
		en: "💳 <b>Payment method</b>\n\nOnly configured methods that support the plan currency are listed."},
	"pay.none": {ru: "⚠️ <b>Нет доступных способов оплаты</b>\n\nАдминистратор ещё не подключил платёжный сервис для этой валюты. Напишите в поддержку.",
		en: "⚠️ <b>No payment method is available</b>\n\nNo payment provider is configured for this currency yet. Please contact support."},
	"pay.telegram_stars": {ru: "⭐ Telegram Stars", en: "⭐ Telegram Stars"},
	"pay.cryptobot":      {ru: "🪙 CryptoBot", en: "🪙 CryptoBot"},
	"pay.yookassa":       {ru: "🏦 ЮKassa", en: "🏦 YooKassa"},
	"pay.manual":         {ru: "🧾 Оплата вручную", en: "🧾 Manual payment"},
	"pay.option":         {ru: "%s — %s", en: "%s — %s"},

	// Checkout summary.
	"checkout.title":        {ru: "🧾 <b>Подтверждение заказа</b>", en: "🧾 <b>Order summary</b>"},
	"checkout.plan":         {ru: "Тариф: <b>%s</b>", en: "Plan: <b>%s</b>"},
	"checkout.period":       {ru: "Период: <b>%s</b>", en: "Period: <b>%s</b>"},
	"checkout.subtotal":     {ru: "Стоимость: <b>%s</b>", en: "Subtotal: <b>%s</b>"},
	"checkout.discount":     {ru: "Скидка: <b>−%s</b>", en: "Discount: <b>−%s</b>"},
	"checkout.wallet":       {ru: "Кошелёк: <b>−%s</b> (доступно %s)", en: "Wallet: <b>−%s</b> (available %s)"},
	"checkout.total":        {ru: "К оплате: <b>%s</b>", en: "Total due: <b>%s</b>"},
	"checkout.free":         {ru: "К оплате: <b>0</b> — заказ полностью покрыт кошельком.", en: "Total due: <b>0</b> — the wallet covers this order in full."},
	"checkout.method":       {ru: "Способ оплаты: <b>%s</b>", en: "Payment method: <b>%s</b>"},
	"checkout.promoAdd":     {ru: "🏷 Ввести промокод", en: "🏷 Enter promo code"},
	"checkout.promoOn":      {ru: "🏷 Промокод %s ✅", en: "🏷 Promo code %s ✅"},
	"checkout.promoOff":     {ru: "🏷 Убрать промокод", en: "🏷 Remove promo code"},
	"checkout.walletOn":     {ru: "👛 Использовать кошелёк ✅", en: "👛 Use wallet ✅"},
	"checkout.walletOff":    {ru: "👛 Использовать кошелёк ☑️", en: "👛 Use wallet ☑️"},
	"checkout.addons":       {ru: "Дополнения: <b>%s</b>", en: "Add-ons: <b>%s</b>"},
	"checkout.pay":          {ru: "💳 Оплатить", en: "💳 Pay"},
	"checkout.changeMethod": {ru: "💱 Другой способ", en: "💱 Change method"},
	"checkout.expired":      {ru: "⌛ <b>Оформление истекло</b>\n\nЦены могли измениться. Начните оформление заново.", en: "⌛ <b>Checkout expired</b>\n\nPrices may have changed. Please start again."},
	"checkout.none":         {ru: "Нет активного оформления. Выберите тариф ещё раз.", en: "No checkout is in progress. Choose a plan again."},

	// Required channels, at the moment a purchase is attempted. The wording
	// names the channels rather than saying "requirements not met", because a
	// customer who cannot see what to do simply leaves.
	// Shop promo codes and saved purchases. The rejections stay distinct
	// because the customer's next move differs: a typo, a code that ran out, and
	// the right code on the wrong product are three different problems.
	"shop.addPromo":         {ru: "🏷 Промокод", en: "🏷 Promo code"},
	"shop.removePromo":      {ru: "Убрать промокод", en: "Remove promo code"},
	"shop.promoApplied":     {ru: "Промокод %s: −%s", en: "Promo %s: −%s"},
	"shop.total":            {ru: "К оплате: %s", en: "Total: %s"},
	"shop.promo.unknown":    {ru: "Такого промокода нет.", en: "That promo code does not exist."},
	"shop.promo.exhausted":  {ru: "Этот промокод уже использован.", en: "That promo code has been used up."},
	"shop.promo.ineligible": {ru: "Этот промокод не действует на этот товар.", en: "That promo code does not apply to this item."},
	"shop.promo.belowCost":  {ru: "Скидка по этому промокоду больше, чем этот товар может дать.", en: "That code discounts more than this item can give."},
	"shop.promo.invalid":    {ru: "Промокод не подошёл.", en: "That promo code could not be applied."},
	"shop.saveForLater":     {ru: "Сохранить на потом", en: "Save for later"},
	"shop.savedTitle":       {ru: "🔖 <b>Сохранено</b>", en: "🔖 <b>Saved</b>"},
	"shop.savedNotice":      {ru: "Мы ничего не спишем автоматически: цена у поставщика меняется, поэтому вы подтвердите её сами, когда вернётесь.", en: "Nothing will be charged automatically. The provider price moves, so you confirm it yourself when you come back."},
	"shop.resume":           {ru: "Вернуться к покупке", en: "Resume purchase"},

	"channels.required": {ru: "📣 <b>Сначала подпишитесь</b>\n\nЧтобы продолжить покупку, подпишитесь на:", en: "📣 <b>Please subscribe first</b>\n\nTo continue with your purchase, subscribe to:"},
	"channels.retry":    {ru: "Я подписался", en: "I have subscribed"},
	"channels.open":     {ru: "Открыть %s", en: "Open %s"},

	// Promo codes.
	"promo.prompt":           {ru: "🏷 <b>Промокод</b>\n\nОтправьте код одним сообщением. Для отмены: /cancel", en: "🏷 <b>Promo code</b>\n\nSend the code in one message. To cancel: /cancel"},
	"promo.applied":          {ru: "✅ Промокод <b>%s</b> применён.", en: "✅ Promo code <b>%s</b> applied."},
	"promo.promo_unknown":    {ru: "Такого промокода нет или он больше не действует.", en: "That promo code does not exist or is no longer active."},
	"promo.promo_ineligible": {ru: "Промокод не действует для этого тарифа или аккаунта.", en: "This promo code does not apply to this plan or account."},
	"promo.promo_exhausted":  {ru: "Промокод уже использован максимальное число раз.", en: "This promo code has reached its redemption limit."},
	"promo.promo_invalid":    {ru: "Формат промокода неверный.", en: "That promo code format is not valid."},
	"promo.rejected":         {ru: "⚠️ %s", en: "⚠️ %s"},
	"promo.rateLimited":      {ru: "⏳ Слишком много попыток ввода промокода. Попробуйте позже.", en: "⏳ Too many promo-code attempts. Please try again later."},

	// Payment states.
	"payment.pending": {ru: "⏳ <b>Ожидаем оплату</b>\n\nЗаказ <code>%s</code>\nК оплате: <b>%s</b>\n\nОплатите по кнопке ниже. Экран можно обновить в любой момент — статус не потеряется.",
		en: "⏳ <b>Waiting for payment</b>\n\nOrder <code>%s</code>\nDue: <b>%s</b>\n\nPay using the button below. You can refresh this screen at any time without losing the order."},
	"payment.open":      {ru: "💳 Перейти к оплате", en: "💳 Open payment page"},
	"payment.invoice":   {ru: "⭐ Оплатить в Telegram", en: "⭐ Pay in Telegram"},
	"payment.expires":   {ru: "\n\nСсылка действует до <b>%s</b>.", en: "\n\nThis payment window closes at <b>%s</b>."},
	"payment.manual":    {ru: "\n\nОператор подтвердит оплату вручную. Мы сообщим, когда заказ будет активирован.", en: "\n\nAn operator will confirm this payment manually. You will be notified once the order is activated."},
	"payment.succeeded": {ru: "✅ <b>Оплата получена</b>\n\nЗаказ <code>%s</code> оплачен. Готовим подписку…", en: "✅ <b>Payment received</b>\n\nOrder <code>%s</code> is paid. Preparing your subscription…"},
	"payment.provisioning": {ru: "⚙️ <b>Активируем подписку</b>\n\nОплата получена, доступ настраивается. Это занимает до минуты — обновите экран.",
		en: "⚙️ <b>Activating your subscription</b>\n\nPayment received and access is being provisioned. This takes up to a minute — refresh to check."},
	"payment.completed": {ru: "🎉 <b>Готово</b>\n\nПодписка активна. Откройте раздел подключения, чтобы добавить её в приложение.",
		en: "🎉 <b>All set</b>\n\nYour subscription is active. Open the connect screen to add it to your app."},
	"payment.failed": {ru: "⚠️ <b>Оплата не прошла</b>\n\nДеньги не списаны или будут возвращены платёжным сервисом. Попробуйте другой способ оплаты или напишите в поддержку.",
		en: "⚠️ <b>Payment did not go through</b>\n\nNothing was charged, or the provider will return it. Try another method or contact support."},
	"payment.cancelled": {ru: "🚫 <b>Заказ отменён</b>\n\nОплата не производилась.", en: "🚫 <b>Order cancelled</b>\n\nNothing was charged."},
	"payment.expired": {ru: "⌛ <b>Срок оплаты истёк</b>\n\nЗаказ закрыт, деньги не списаны. Оформите новый заказ, когда будете готовы.",
		en: "⌛ <b>Payment window closed</b>\n\nThe order was closed and nothing was charged. Create a new order whenever you are ready."},
	"payment.refunded":  {ru: "↩️ <b>Возврат</b>\n\nПо заказу оформлен возврат: <b>%s</b>.", en: "↩️ <b>Refunded</b>\n\nA refund of <b>%s</b> was recorded for this order."},
	"payment.duplicate": {ru: "Заказ уже оплачен — повторная оплата не нужна.", en: "This order is already paid — no second payment is needed."},
	"payment.delayed": {ru: "\n\nЕсли вы уже оплатили, подтверждение может идти до нескольких минут. Нажмите «Обновить».",
		en: "\n\nIf you have already paid, confirmation can take a few minutes. Tap Refresh."},
	"payment.starsTitle":       {ru: "Подписка Omniflow", en: "Omniflow subscription"},
	"payment.starsDescription": {ru: "%s — %s", en: "%s — %s"},
	"payment.precheckoutStale": {ru: "Заказ больше не активен. Оформите его заново.", en: "This order is no longer active. Please start again."},

	// Orders.
	"orders.title": {ru: "🧾 <b>Мои заказы</b>", en: "🧾 <b>My orders</b>"},
	"orders.empty": {ru: "🧾 <b>Мои заказы</b>\n\nЗаказов пока нет. Выберите тариф, чтобы оформить первый.", en: "🧾 <b>My orders</b>\n\nNo orders yet. Choose a plan to place your first one."},
	"orders.row":   {ru: "%s · %s · %s", en: "%s · %s · %s"},
	"orders.detail": {ru: "🧾 <b>Заказ</b> <code>%s</code>\n\nТариф: <b>%s</b>\nСоздан: <b>%s</b>\nСтатус: <b>%s</b>\nСумма: <b>%s</b>\nОплачено: <b>%s</b>",
		en: "🧾 <b>Order</b> <code>%s</code>\n\nPlan: <b>%s</b>\nCreated: <b>%s</b>\nStatus: <b>%s</b>\nAmount: <b>%s</b>\nPaid: <b>%s</b>"},
	"orders.refund":  {ru: "\nВозврат: <b>%s</b> (%s)", en: "\nRefund: <b>%s</b> (%s)"},
	"orders.receipt": {ru: "🧾 Чек", en: "🧾 Receipt"},
	"orders.cancel":  {ru: "🚫 Отменить заказ", en: "🚫 Cancel order"},

	// Order states.
	"state.draft":              {ru: "черновик", en: "draft"},
	"state.pending":            {ru: "ожидает оплаты", en: "awaiting payment"},
	"state.paid":               {ru: "оплачен", en: "paid"},
	"state.fulfilled":          {ru: "выполнен", en: "completed"},
	"state.cancelled":          {ru: "отменён", en: "cancelled"},
	"state.expired":            {ru: "истёк", en: "expired"},
	"state.partially_refunded": {ru: "частичный возврат", en: "partially refunded"},
	"state.refunded":           {ru: "возвращён", en: "refunded"},

	// Wallet.
	"wallet.title": {ru: "👛 <b>Кошелёк</b>\n\nДоступно: <b>%s</b>\n\nСредства кошелька применяются к заказу автоматически, если вы этого не отключите.",
		en: "👛 <b>Wallet</b>\n\nAvailable: <b>%s</b>\n\nWallet credit is applied to an order automatically unless you turn it off."},
	"wallet.empty":           {ru: "\n\nОпераций пока нет.", en: "\n\nNo transactions yet."},
	"wallet.entry":           {ru: "%s · %s · %s", en: "%s · %s · %s"},
	"wallet.credit":          {ru: "пополнение", en: "credit"},
	"wallet.debit":           {ru: "списание", en: "debit"},
	"wallet.payment":         {ru: "оплата заказа", en: "order payment"},
	"wallet.refund":          {ru: "возврат", en: "refund"},
	"wallet.referral_reward": {ru: "реферальная награда", en: "referral reward"},
	"wallet.correction":      {ru: "корректировка", en: "correction"},
	"wallet.expiration":      {ru: "истечение срока", en: "expiry"},

	// Subscription lifecycle.
	"life.provisioning": {ru: "⚙️ <b>Подписка активируется</b>\n\nОплата получена. Доступ появится в течение минуты.", en: "⚙️ <b>Subscription is being activated</b>\n\nPayment received. Access appears within a minute."},
	"life.grace": {ru: "🟠 <b>Льготный период</b>\n\nПодписка закончилась %s, но доступ сохраняется до <b>%s</b>. Продлите её, чтобы не потерять подключение.",
		en: "🟠 <b>Grace period</b>\n\nYour subscription ended on %s, and access continues until <b>%s</b>. Renew to keep your connection."},
	"life.limited":  {ru: "🟠 <b>Лимит трафика исчерпан</b>\n\nПодключение ограничено до обновления лимита или смены тарифа.", en: "🟠 <b>Traffic limit reached</b>\n\nYour connection is limited until the allowance resets or you change plan."},
	"life.expired":  {ru: "🔴 <b>Подписка истекла</b>\n\nВосстановите доступ в один тап — тариф и настройки сохранены.", en: "🔴 <b>Subscription expired</b>\n\nRestore access in one tap — your plan and settings are kept."},
	"life.disabled": {ru: "⚪️ <b>Подписка отключена</b>\n\nОбратитесь в поддержку, чтобы восстановить доступ.", en: "⚪️ <b>Subscription disabled</b>\n\nContact support to restore access."},
	"life.failed": {ru: "⚠️ <b>Активация не завершилась</b>\n\nОплата сохранена. Мы повторяем активацию автоматически — обновите экран или напишите в поддержку.",
		en: "⚠️ <b>Activation did not finish</b>\n\nYour payment is safe. Activation retries automatically — refresh or contact support."},
	"life.none":    {ru: "У вас пока нет активной подписки.", en: "You do not have an active subscription yet."},
	"life.recover": {ru: "♻️ Восстановить подписку", en: "♻️ Restore subscription"},

	// Trials.
	"trial.title":                     {ru: "🎁 <b>Пробный период</b>", en: "🎁 <b>Free trial</b>"},
	"trial.trial_already_used":        {ru: "Пробный период уже был использован на этом аккаунте.", en: "This account has already used its free trial."},
	"trial.subscription_active":       {ru: "Пробный период доступен только без активной подписки.", en: "A trial can only be started without an active subscription."},
	"trial.identity_already_trialled": {ru: "Пробный период уже использован для этого аккаунта Telegram.", en: "This Telegram account has already used a trial."},
	"trial.account_too_new":           {ru: "Аккаунт слишком новый. Попробуйте позже.", en: "This account is too new. Please try again later."},
	"trial.existing_customer":         {ru: "Пробный период доступен только новым клиентам.", en: "The trial is only available to new customers."},
	"trial.not_a_trial_plan":          {ru: "Этот тариф не является пробным.", en: "This plan is not a trial."},
	"trial.unsupported_trial_rule":    {ru: "Правило пробного периода не поддерживается. Напишите в поддержку.", en: "This trial rule is not supported. Please contact support."},
	"trial.rejected":                  {ru: "🎁 <b>Пробный период недоступен</b>\n\n%s", en: "🎁 <b>Trial unavailable</b>\n\n%s"},

	// Auto renew.
	"renew.title": {ru: "♻️ <b>Автопродление</b>", en: "♻️ <b>Auto-renew</b>"},
	"renew.unsupported": {ru: "♻️ <b>Автопродление</b>\n\nНи один подключённый способ оплаты не поддерживает автосписание. Мы напомним о продлении заранее.",
		en: "♻️ <b>Auto-renew</b>\n\nNo configured payment method supports recurring charges. We will remind you before expiry instead."},
	"renew.on":      {ru: "♻️ <b>Автопродление включено</b>\n\nТариф: <b>%s</b>\nСпособ: <b>%s</b>\n\nОтключить можно в любой момент.", en: "♻️ <b>Auto-renew is on</b>\n\nPlan: <b>%s</b>\nMethod: <b>%s</b>\n\nYou can turn it off at any time."},
	"renew.off":     {ru: "♻️ <b>Автопродление выключено</b>\n\nМы напомним о продлении за 7, 3 и 1 день до окончания.", en: "♻️ <b>Auto-renew is off</b>\n\nWe will remind you 7, 3, and 1 day before expiry."},
	"renew.enable":  {ru: "Включить автопродление", en: "Turn auto-renew on"},
	"renew.disable": {ru: "Выключить автопродление", en: "Turn auto-renew off"},

	// What an automatic charge will be taken from, and when. Both are stated on
	// the screen rather than left to a default the customer never sees, because
	// this is money moving while they are not present.
	"renew.funding":    {ru: "Списываем с: <b>%s</b>", en: "Charged to: <b>%s</b>"},
	"renew.lead":       {ru: "Списание за <b>%d дн.</b> до окончания", en: "Charged <b>%d day(s)</b> before expiry"},
	"renew.fromWallet": {ru: "Баланс", en: "Wallet"},
	"renew.fromMethod": {ru: "Сохранённая карта", en: "Saved card"},
	"renew.leadDays":   {ru: "%d дн.", en: "%d d"},
	"renew.methods":    {ru: "💳 Способы оплаты", en: "💳 Payment methods"},
	"renew.noMethod": {ru: "У вас нет сохранённого способа оплаты. Он появится после оплаты картой с включённым автопродлением.",
		en: "You have no saved payment method. One appears after you pay by card with auto-renew turned on."},
	"renew.dunning": {ru: "⚠️ Последнее списание не прошло. Мы попробуем ещё раз автоматически.",
		en: "⚠️ The last charge did not go through. We will try again automatically."},
	"renew.suspended": {ru: "⚠️ Автосписание остановлено после нескольких неудачных попыток. Продлите вручную или измените способ оплаты.",
		en: "⚠️ Automatic charging stopped after several failed attempts. Renew by hand or change your payment method."},

	// Saved methods.
	"methods.title":       {ru: "💳 <b>Способы оплаты</b>", en: "💳 <b>Payment methods</b>"},
	"methods.empty":       {ru: "💳 <b>Способы оплаты</b>\n\nСохранённых способов пока нет.", en: "💳 <b>Payment methods</b>\n\nYou have no saved methods yet."},
	"methods.default":     {ru: "основной", en: "default"},
	"methods.makeDefault": {ru: "Сделать основным", en: "Make default"},
	"methods.remove":      {ru: "Удалить", en: "Remove"},
	"methods.notice": {ru: "Мы храним только ссылку от платёжного провайдера — номер карты и срок действия не сохраняются.",
		en: "We store only a reference issued by the payment provider — no card number and no expiry date are kept."},
	"methods.status.expired": {ru: "истёк", en: "expired"},
	"methods.status.failed":  {ru: "отклонён", en: "declined"},
	"methods.status.revoked": {ru: "удалён", en: "removed"},

	// Digital goods shop. Every screen speaks about the goods rather than about
	// the payment: a customer asking where their Stars are is not asking
	// whether their card was charged.
	"shop.title": {ru: "🎁 <b>Магазин</b>\n\nTelegram Premium и Stars с доставкой на указанный аккаунт.",
		en: "🎁 <b>Shop</b>\n\nTelegram Premium and Stars, delivered to the account you name."},
	"shop.empty": {ru: "🎁 <b>Магазин</b>\n\nСейчас здесь ничего нет. Загляните позже.",
		en: "🎁 <b>Shop</b>\n\nNothing is on sale right now. Check back later."},
	"shop.premiumSection": {ru: "<b>Telegram Premium</b>", en: "<b>Telegram Premium</b>"},
	"shop.starsSection":   {ru: "<b>Telegram Stars</b>", en: "<b>Telegram Stars</b>"},
	"shop.item":           {ru: "🎁 <b>%s</b>", en: "🎁 <b>%s</b>"},
	"shop.months":         {ru: "⏳ Срок: <b>%d мес.</b>", en: "⏳ Duration: <b>%d month(s)</b>"},
	"shop.stars":          {ru: "⭐ Количество: <b>%d</b>", en: "⭐ Quantity: <b>%d</b>"},
	"shop.price":          {ru: "💳 Цена: <b>%s</b>", en: "💳 Price: <b>%s</b>"},
	"shop.quoteExpiry":    {ru: "Цена действительна до <b>%s</b>", en: "This price holds until <b>%s</b>"},
	"shop.quoteFailed": {ru: "⚠️ Сейчас не удаётся получить цену у поставщика. Попробуйте позже.",
		en: "⚠️ The provider price is unavailable right now. Please try again later."},
	"shop.buyForMe":    {ru: "Купить себе", en: "Buy for me"},
	"shop.buyForOther": {ru: "Купить другому", en: "Buy for someone else"},
	"shop.history":     {ru: "🧾 Мои покупки", en: "🧾 My purchases"},
	"shop.recipientPrompt": {ru: "Отправьте @username получателя.\n\nДоставка идёт на этот аккаунт и её нельзя отозвать — проверьте имя внимательно.",
		en: "Send the recipient's @username.\n\nDelivery goes to that account and cannot be recalled, so check the handle carefully."},
	"shop.recipientInvalid": {ru: "⚠️ Это не похоже на имя пользователя Telegram. Оно состоит из 5–32 латинских букв, цифр и подчёркиваний.",
		en: "⚠️ That does not look like a Telegram username. Handles are 5–32 Latin letters, digits, and underscores."},
	"shop.confirmTitle":   {ru: "🧾 <b>Проверьте получателя</b>", en: "🧾 <b>Check the recipient</b>"},
	"shop.confirmProduct": {ru: "Товар: <b>%s</b>", en: "Item: <b>%s</b>"},
	"shop.confirmSelf":    {ru: "Получатель: <b>@%s</b> — это вы.", en: "Recipient: <b>@%s</b> — that is you."},
	"shop.confirmOther":   {ru: "Получатель: <b>@%s</b>", en: "Recipient: <b>@%s</b>"},
	"shop.irreversible": {ru: "После отправки доставку нельзя отменить или вернуть.",
		en: "Once sent, the delivery cannot be cancelled or taken back."},
	"shop.confirmBuy":    {ru: "Подтвердить и оплатить", en: "Confirm and pay"},
	"shop.recipientLine": {ru: "Получатель: @%s", en: "Recipient: @%s"},
	"shop.ordersTitle":   {ru: "🧾 <b>Покупки в магазине</b>", en: "🧾 <b>Shop purchases</b>"},
	"shop.noOrders":      {ru: "🧾 <b>Покупки в магазине</b>\n\nПокупок пока нет.", en: "🧾 <b>Shop purchases</b>\n\nYou have no purchases yet."},
	"shop.orderTitle":    {ru: "🧾 <b>Покупка %s</b>", en: "🧾 <b>Purchase %s</b>"},
	"shop.deliveryState": {ru: "Статус доставки: <b>%s</b>", en: "Delivery status: <b>%s</b>"},
	"shop.underReview": {ru: "Мы проверяем эту доставку вручную: поставщик не подтвердил результат. Оплата не потеряна — мы либо завершим доставку, либо вернём деньги на баланс.",
		en: "We are checking this delivery by hand: the provider did not confirm the outcome. Your payment is safe — we will either complete the delivery or return the money to your balance."},
	"shop.state.awaitingPayment": {ru: "ожидает оплаты", en: "awaiting payment"},
	"shop.state.paid":            {ru: "оплачено", en: "paid"},
	"shop.state.delivering":      {ru: "отправляем", en: "sending"},
	"shop.state.delivered":       {ru: "доставлено", en: "delivered"},
	"shop.state.failed":          {ru: "не доставлено", en: "not delivered"},
	"shop.state.review":          {ru: "на проверке", en: "under review"},
	"shop.state.refunded":        {ru: "возвращено", en: "refunded"},

	// Gifts. The claim code is shown once and never again, so every screen that
	// touches it says so plainly rather than leaving a sender to discover it.
	"gift.title": {ru: "🎁 <b>Подарки</b>\n\nПодарите подписку или баланс — получателю не нужен аккаунт заранее.",
		en: "🎁 <b>Gifts</b>\n\nGive a subscription or wallet credit. The recipient does not need an account beforehand."},
	"gift.givePlan":   {ru: "🎁 Подарить подписку", en: "🎁 Gift a subscription"},
	"gift.giveCredit": {ru: "👛 Подарить баланс", en: "👛 Gift wallet credit"},
	"gift.claim":      {ru: "🔑 Активировать подарок", en: "🔑 Redeem a gift"},
	"gift.sentTitle":  {ru: "<b>Ваши подарки</b>", en: "<b>Your gifts</b>"},
	"gift.choosePlan": {ru: "🎁 <b>Какую подписку подарить?</b>", en: "🎁 <b>Which subscription?</b>"},
	"gift.chooseCredit": {ru: "👛 <b>Сколько подарить?</b>\n\nПолучатель сможет потратить сумму на любой тариф.",
		en: "👛 <b>How much?</b>\n\nThe recipient can spend it on any plan."},
	"gift.noCreditPresets": {ru: "Подарочные суммы пока не настроены. Напишите в поддержку.",
		en: "No gift amounts are configured yet. Please contact support."},
	"gift.messagePrompt": {ru: "Напишите короткое сообщение получателю (до 200 символов) или отправьте «-», чтобы обойтись без него.",
		en: "Write a short note for the recipient (up to 200 characters), or send “-” to skip it."},
	"gift.created": {ru: "🎁 <b>Подарок готов</b>\n\nКод активации:", en: "🎁 <b>Your gift is ready</b>\n\nRedemption code:"},
	"gift.codeNotice": {ru: "Мы показываем код один раз и не храним его — сохраните и передайте получателю сами.",
		en: "The code is shown once and not stored — save it and pass it on yourself."},
	"gift.expires": {ru: "Действует до: <b>%s</b>", en: "Valid until: <b>%s</b>"},
	"gift.awaitingPayment": {ru: "Подарок станет активируемым сразу после оплаты заказа.",
		en: "The gift becomes redeemable as soon as the order is paid."},
	"gift.paymentLabel": {ru: "Подарок", en: "Gift"},
	"gift.claimPrompt":  {ru: "Отправьте код активации подарка.", en: "Send the gift redemption code."},
	"gift.claimRefused": {ru: "⚠️ Этот код нельзя активировать. Проверьте его или попросите отправителя выдать новый.",
		en: "⚠️ This code cannot be redeemed. Check it, or ask the sender to issue a new one."},
	"gift.claimed":             {ru: "🎁 <b>Подарок активирован</b>", en: "🎁 <b>Gift redeemed</b>"},
	"gift.claimedCredit":       {ru: "На баланс зачислено <b>%s</b>.", en: "<b>%s</b> was added to your wallet."},
	"gift.claimedSubscription": {ru: "Подписка активируется — это займёт меньше минуты.", en: "Your subscription is being activated — this takes under a minute."},
	"gift.kind.subscription":   {ru: "подписка", en: "subscription"},
	"gift.kind.addon":          {ru: "дополнение", en: "add-on"},
	"gift.kind.wallet_credit":  {ru: "баланс", en: "wallet credit"},
	"gift.status.pending":      {ru: "ждёт оплаты", en: "awaiting payment"},
	"gift.status.deliverable":  {ru: "можно активировать", en: "redeemable"},
	"gift.status.claimed":      {ru: "активирован", en: "redeemed"},
	"gift.status.expired":      {ru: "истёк", en: "expired"},
	"gift.status.revoked":      {ru: "отозван", en: "revoked"},
	"gift.status.refunded":     {ru: "возвращён", en: "refunded"},

	// Personal offers. The countdown is in whole days: an offer measured in
	// hours reads as pressure rather than as information.
	"offer.title": {ru: "✨ <b>Персональные предложения</b>", en: "✨ <b>Your offers</b>"},
	"offer.empty": {ru: "✨ <b>Персональные предложения</b>\n\nСейчас предложений нет.",
		en: "✨ <b>Your offers</b>\n\nYou have no offers right now."},
	"offer.open":      {ru: "Открыть", en: "View"},
	"offer.dismiss":   {ru: "Не интересно", en: "Not interested"},
	"offer.remaining": {ru: "Осталось %d дн.", en: "%d day(s) left"},
	"offer.lastDay":   {ru: "Последний день", en: "Last day"},
	"offer.expired":   {ru: "Срок истёк", en: "Expired"},
	"offer.code":      {ru: "Промокод: <code>%s</code>", en: "Promo code: <code>%s</code>"},
	"offer.singleUse": {ru: "Предложение можно использовать один раз. Введите промокод при оформлении заказа.",
		en: "The offer can be used once. Enter the promo code at checkout."},

	// Connection instructions.
	"connect.title":            {ru: "🚀 <b>Подключение</b>\n\nВыберите платформу — покажем приложение и шаги установки.", en: "🚀 <b>Connect</b>\n\nPick your platform for the recommended app and setup steps."},
	"connect.platform.ios":     {ru: "🍎 iPhone / iPad", en: "🍎 iPhone / iPad"},
	"connect.platform.android": {ru: "🤖 Android", en: "🤖 Android"},
	"connect.platform.windows": {ru: "🪟 Windows", en: "🪟 Windows"},
	"connect.platform.macos":   {ru: "💻 macOS", en: "💻 macOS"},
	"connect.platform.linux":   {ru: "🐧 Linux", en: "🐧 Linux"},
	"connect.steps": {ru: "%s\n\n1. Установите <b>%s</b>.\n2. Нажмите кнопку добавления профиля ниже. Если приложение не открылось, скопируйте ссылку и вставьте её в приложение вручную.\n3. Подключитесь.\n\nНикому не пересылайте ссылку подписки.",
		en: "%s\n\n1. Install <b>%s</b>.\n2. Tap the add-profile button below. If no app opens, copy the link and import it manually.\n3. Connect.\n\nNever share your subscription link."},
	"connect.deepLink": {ru: "📲 Добавить в %s", en: "📲 Add to %s"},
	"connect.copyLink": {ru: "📋 Скопировать ссылку", en: "📋 Copy link"},
	"connect.noLink":   {ru: "Ссылка подписки появится после активации доступа.", en: "Your subscription link appears once access is active."},

	// Support desk.
	"support.list":            {ru: "💬 <b>Поддержка</b>\n\nВаши обращения:", en: "💬 <b>Support</b>\n\nYour requests:"},
	"support.empty":           {ru: "💬 <b>Поддержка</b>\n\nОбращений пока нет. Опишите вопрос — мы ответим здесь же.", en: "💬 <b>Support</b>\n\nNo requests yet. Describe your question and we will answer right here."},
	"support.new":             {ru: "✍️ Новое обращение", en: "✍️ New request"},
	"support.row":             {ru: "%s · %s%s", en: "%s · %s%s"},
	"support.unread":          {ru: " · %d новых", en: " · %d new"},
	"support.ticket":          {ru: "💬 <b>Обращение</b>\n\nТема: <b>%s</b>\nСтатус: <b>%s</b>\nОбновлено: <b>%s</b>", en: "💬 <b>Request</b>\n\nSubject: <b>%s</b>\nStatus: <b>%s</b>\nUpdated: <b>%s</b>"},
	"support.messageCustomer": {ru: "👤 Вы", en: "👤 You"},
	"support.messageOperator": {ru: "🛟 Поддержка", en: "🛟 Support"},
	"support.messageSystem":   {ru: "⚙️ Система", en: "⚙️ System"},
	"support.attachment":      {ru: "📎 %s (%s)", en: "📎 %s (%s)"},
	"support.reply":           {ru: "✍️ Ответить", en: "✍️ Reply"},
	"support.close":           {ru: "✅ Закрыть обращение", en: "✅ Close request"},
	"support.reopen":          {ru: "♻️ Открыть заново", en: "♻️ Reopen"},
	"support.status.open":     {ru: "открыто", en: "open"},
	"support.status.closed":   {ru: "закрыто", en: "closed"},
	"support.compose": {ru: "✍️ <b>Сообщение в поддержку</b>\n\nОтправьте текст одним сообщением. Можно приложить скриншот или документ до 10 МБ.\nНикогда не отправляйте пароли, токены и ссылку подписки.\n\nДля отмены: /cancel",
		en: "✍️ <b>Message to support</b>\n\nSend your text in one message. You may attach a screenshot or a document up to 10 MB.\nNever send passwords, tokens, or your subscription link.\n\nTo cancel: /cancel"},
	"support.sent":       {ru: "✅ <b>Сообщение отправлено</b>\n\nОтвет придёт в этот чат.", en: "✅ <b>Message sent</b>\n\nThe reply arrives in this chat."},
	"support.tooLong":    {ru: "⚠️ Сообщение должно содержать от 1 до 4000 символов.", en: "⚠️ Your message must contain 1 to 4000 characters."},
	"support.tooBig":     {ru: "⚠️ Вложение больше 10 МБ. Отправьте файл меньшего размера.", en: "⚠️ That attachment is larger than 10 MB. Please send a smaller file."},
	"support.badKind":    {ru: "⚠️ Можно приложить только фото или документ.", en: "⚠️ Only photos and documents can be attached."},
	"support.closedHint": {ru: "⚠️ Обращение закрыто. Откройте его заново или создайте новое.", en: "⚠️ This request is closed. Reopen it or create a new one."},
	"support.notFound":   {ru: "⚠️ Обращение не найдено.", en: "⚠️ Request not found."},
	"support.replyAlert": {ru: "🛟 <b>Ответ поддержки</b>\n\n%s\n\n%s", en: "🛟 <b>Support reply</b>\n\n%s\n\n%s"},
	"support.open":       {ru: "💬 Открыть обращение", en: "💬 Open request"},

	// News inbox.
	"news.title":                 {ru: "📰 <b>Новости и объявления</b>", en: "📰 <b>News and announcements</b>"},
	"news.empty":                 {ru: "📰 <b>Новости и объявления</b>\n\nПока ничего нового.", en: "📰 <b>News and announcements</b>\n\nNothing new right now."},
	"news.row":                   {ru: "%s%s · %s", en: "%s%s · %s"},
	"news.unread":                {ru: "🔵 ", en: "🔵 "},
	"news.item":                  {ru: "%s\n\n<b>%s</b>\n\n%s", en: "%s\n\n<b>%s</b>\n\n%s"},
	"news.gone":                  {ru: "📰 Публикация больше недоступна.", en: "📰 That post is no longer available."},
	"news.category.news":         {ru: "📰 Новость", en: "📰 News"},
	"news.category.announcement": {ru: "📢 Объявление", en: "📢 Announcement"},
	"news.category.incident":     {ru: "🚨 Инцидент", en: "🚨 Incident"},
	"news.category.maintenance":  {ru: "🛠 Технические работы", en: "🛠 Maintenance"},
	"news.alert":                 {ru: "%s\n\n<b>%s</b>", en: "%s\n\n<b>%s</b>"},

	// Referrals.
	"referral.title":    {ru: "🎁 <b>Пригласить друга</b>\n\nВаш код: <code>%s</code>", en: "🎁 <b>Invite a friend</b>\n\nYour code: <code>%s</code>"},
	"referral.progress": {ru: "\n\nПриглашено: <b>%d</b>\nПодтверждено: <b>%d</b>\nНачислено: <b>%s</b>", en: "\n\nInvited: <b>%d</b>\nQualified: <b>%d</b>\nEarned: <b>%s</b>"},
	"referral.rewards": {ru: "\n\nНаграда пригласившему: <b>%s</b>\nНаграда приглашённому: <b>%s</b>\nНаграда начисляется один раз после первой оплаченной покупки друга.",
		en: "\n\nInviter reward: <b>%s</b>\nInvitee reward: <b>%s</b>\nThe reward is granted once, after your friend's first paid order."},
	"referral.remaining":    {ru: "\nОсталось награждаемых приглашений: <b>%d</b>", en: "\nRewarded invitations remaining: <b>%d</b>"},
	"referral.disabled":     {ru: "\n\nПрограмма приглашений сейчас отключена администратором.", en: "\n\nThe referral programme is currently disabled by the operator."},
	"referral.share":        {ru: "📨 Поделиться", en: "📨 Share"},
	"referral.terms":        {ru: "📄 Условия программы", en: "📄 Programme terms"},
	"referral.history":      {ru: "📜 История начислений", en: "📜 Reward history"},
	"referral.historyTitle": {ru: "📜 <b>История начислений</b>", en: "📜 <b>Reward history</b>"},
	"referral.historyEmpty": {ru: "📜 <b>История начислений</b>\n\nНачислений пока нет.", en: "📜 <b>Reward history</b>\n\nNo rewards yet."},
	"referral.role.inviter": {ru: "за приглашение", en: "for inviting"},
	"referral.role.invitee": {ru: "приветственная", en: "welcome bonus"},

	// Settings.
	"settings.title":     {ru: "⚙️ <b>Настройки</b>\n\nЯзык, уведомления и тихие часы.", en: "⚙️ <b>Settings</b>\n\nLanguage, notifications, and quiet hours."},
	"settings.expiry":    {ru: "%s Срок подписки", en: "%s Subscription expiry"},
	"settings.traffic":   {ru: "%s Лимит трафика", en: "%s Traffic limit"},
	"settings.renewal":   {ru: "%s Напоминания о продлении", en: "%s Renewal reminders"},
	"settings.news":      {ru: "%s Новости сервиса", en: "%s Service news"},
	"settings.marketing": {ru: "%s Рекламные сообщения", en: "%s Marketing messages"},
	"settings.quiet":     {ru: "🌙 Тихие часы: %s", en: "🌙 Quiet hours: %s"},
	"settings.quietOff":  {ru: "выключены", en: "off"},
	"settings.quietOn":   {ru: "%02d:00–%02d:00", en: "%02d:00–%02d:00"},
	"settings.quietTitle": {ru: "🌙 <b>Тихие часы</b>\n\nВ это время мы не присылаем несрочные сообщения. Уведомления об оплате, активации и ответах поддержки приходят всегда.",
		en: "🌙 <b>Quiet hours</b>\n\nNon-urgent messages wait during this window. Payment, activation, and support replies always arrive."},
	"settings.marketingNote": {ru: "\n\nРекламные сообщения приходят только с вашего согласия и не чаще %d раз в неделю.", en: "\n\nMarketing messages require your consent and are limited to %d per week."},

	// Push notifications.
	"alert.subscription": {ru: "🗂 Подписка: <b>%s</b>", en: "🗂 Subscription: <b>%s</b>"},
	"alert.expiry":       {ru: "⏳ <b>Подписка закончится через %d дн.</b>\n\nПродлите её, чтобы не потерять доступ.", en: "⏳ <b>Your subscription expires in %d day(s)</b>\n\nRenew it to keep your access."},
	"alert.traffic":      {ru: "📡 <b>Использовано %d%% доступного трафика</b>\n\nПри исчерпании лимита подключение будет ограничено.", en: "📡 <b>You have used %d%% of your traffic allowance</b>\n\nYour connection is limited once the allowance runs out."},
	"alert.renewal":      {ru: "♻️ <b>Пора продлить подписку</b>\n\nТариф: <b>%s</b>\nОсталось: <b>%d дн.</b>\nДействует до: <b>%s</b>", en: "♻️ <b>Time to renew</b>\n\nPlan: <b>%s</b>\nRemaining: <b>%d day(s)</b>\nValid until: <b>%s</b>"},
	// Automatic renewal. The two messages differ because what the customer has
	// to do differs: the first is a warning that resolves itself if the card
	// starts working, the second is the end of automatic renewal.
	"alert.dunningRetry": {
		ru: "💳 <b>Не удалось списать оплату</b>\n\nМы попробуем ещё раз автоматически. Доступ пока сохраняется — проверьте способ оплаты или продлите вручную.",
		en: "💳 <b>We could not take the payment</b>\n\nWe will try again automatically. Your access continues for now — check your payment method or renew by hand."},
	"alert.dunningAbandoned": {
		ru: "💳 <b>Автопродление остановлено</b>\n\nМы больше не пытаемся списать оплату. Продлите подписку вручную, чтобы сохранить доступ.",
		en: "💳 <b>Automatic renewal has stopped</b>\n\nWe are no longer attempting to charge. Renew by hand to keep your access."},
	"alert.fulfillmentDone": {ru: "🎉 <b>Подписка активирована</b>\n\nМожно подключаться.", en: "🎉 <b>Subscription activated</b>\n\nYou can connect now."},
	"alert.fulfillmentFailed": {ru: "⚠️ <b>Активация задерживается</b>\n\nОплата сохранена, мы повторяем активацию автоматически. Если доступ не появится в течение часа — напишите в поддержку.",
		en: "⚠️ <b>Activation is delayed</b>\n\nYour payment is safe and activation retries automatically. If access does not appear within an hour, contact support."},

	// Shared short labels.
	"connect.title.short":      {ru: "🚀 Подключиться", en: "🚀 Connect"},
	"connect.openSubscription": {ru: "🔗 Открыть подписку", en: "🔗 Open subscription"},
	"news.open":                {ru: "📰 Читать", en: "📰 Read"},

	// Wallet top-up.
	"menu.topup": {ru: "➕ Пополнить кошелёк", en: "➕ Top up wallet"},
	"topup.title": {ru: "➕ <b>Пополнение кошелька</b>\n\nНа балансе: <b>%s</b>",
		en: "➕ <b>Top up your wallet</b>\n\nCurrent balance: <b>%s</b>"},
	"topup.bounds":    {ru: "Минимум: <b>%s</b> · Максимум: <b>%s</b>", en: "Minimum: <b>%s</b> · Maximum: <b>%s</b>"},
	"topup.window":    {ru: "Доступно к пополнению: <b>%s</b> за %s", en: "Remaining allowance: <b>%s</b> per %s"},
	"topup.custom":    {ru: "✍️ Своя сумма", en: "✍️ Custom amount"},
	"topup.history":   {ru: "📜 История пополнений", en: "📜 Top-up history"},
	"topup.noPresets": {ru: "Готовых сумм сейчас нет — введите свою.", en: "No preset amount fits right now — enter your own."},
	"topup.prompt": {ru: "✍️ <b>Сумма пополнения</b>\n\nОтправьте сумму одним сообщением, от %s до %s.\nДля отмены: /cancel",
		en: "✍️ <b>Top-up amount</b>\n\nSend the amount in one message, between %s and %s.\nTo cancel: /cancel"},
	"topup.badAmount":           {ru: "⚠️ Не удалось разобрать сумму. Отправьте число, например 500.", en: "⚠️ That amount could not be read. Send a number, for example 500."},
	"topup.method":              {ru: "💳 <b>Пополнение на %s</b>\n\nВыберите способ оплаты.", en: "💳 <b>Top up %s</b>\n\nChoose a payment method."},
	"topup.rejected":            {ru: "⚠️ <b>Пополнение недоступно</b>\n\n%s", en: "⚠️ <b>Top-up unavailable</b>\n\n%s"},
	"topup.topup_disabled":      {ru: "Пополнение кошелька отключено администратором.", en: "Wallet top-up is turned off by the operator."},
	"topup.topup_below_minimum": {ru: "Сумма меньше минимальной.", en: "That amount is below the minimum."},
	"topup.topup_above_maximum": {ru: "Сумма больше максимальной.", en: "That amount is above the maximum."},
	"topup.topup_window_exceeded": {ru: "Достигнут лимит пополнений за период. Попробуйте позже.",
		en: "You have reached the top-up limit for this period. Please try again later."},
	"topup.topup_invalid_amount":     {ru: "Сумма должна быть больше нуля.", en: "The amount must be greater than zero."},
	"topup.historyTitle":             {ru: "📜 <b>История пополнений</b>", en: "📜 <b>Top-up history</b>"},
	"topup.historyEmpty":             {ru: "📜 <b>История пополнений</b>\n\nПополнений пока не было.", en: "📜 <b>Top-up history</b>\n\nNo top-ups yet."},
	"topup.openPending":              {ru: "⏳ Открыть незавершённое", en: "⏳ Open pending top-up"},
	"topup.state.credited":           {ru: "зачислено", en: "credited"},
	"topup.state.pending":            {ru: "ожидает оплаты", en: "awaiting payment"},
	"topup.state.paid":               {ru: "оплачено", en: "paid"},
	"topup.state.cancelled":          {ru: "отменено", en: "cancelled"},
	"topup.state.expired":            {ru: "истекло", en: "expired"},
	"topup.state.draft":              {ru: "черновик", en: "draft"},
	"topup.state.fulfilled":          {ru: "зачислено", en: "credited"},
	"topup.state.refunded":           {ru: "возвращено", en: "refunded"},
	"topup.state.partially_refunded": {ru: "частичный возврат", en: "partially refunded"},
	"topup.disabled": {ru: "➕ <b>Пополнение кошелька</b>\n\nПополнение сейчас отключено администратором сервиса.",
		en: "➕ <b>Wallet top-up</b>\n\nTop-up is currently turned off by the operator."},

	// Saved cart and deferred purchase.
	"menu.cart":    {ru: "🛍 Отложенный заказ", en: "🛍 Saved cart"},
	"cart.title":   {ru: "🛍 <b>Отложенный заказ</b>", en: "🛍 <b>Saved cart</b>"},
	"cart.save":    {ru: "🛍 Отложить и пополнить", en: "🛍 Save and top up"},
	"cart.plan":    {ru: "Тариф: <b>%s</b>", en: "Plan: <b>%s</b>"},
	"cart.total":   {ru: "К оплате: <b>%s</b>", en: "Total: <b>%s</b>"},
	"cart.addons":  {ru: "В том числе дополнения: <b>%s</b>", en: "Including add-ons: <b>%s</b>"},
	"cart.balance": {ru: "На балансе: <b>%s</b>", en: "Wallet balance: <b>%s</b>"},
	"cart.readyAuto": {ru: "✅ Баланса достаточно. Заказ будет оформлен автоматически в течение минуты — или нажмите «Купить сейчас».",
		en: "✅ Your balance covers this. The order is placed automatically within a minute — or tap Buy now."},
	"cart.readyManual": {ru: "✅ Баланса достаточно. Нажмите «Купить сейчас», чтобы оформить заказ.",
		en: "✅ Your balance covers this. Tap Buy now to place the order."},
	"cart.waiting": {ru: "⏳ Не хватает <b>%s</b>. Пополните кошелёк — заказ оформится автоматически.",
		en: "⏳ <b>%s</b> short. Top up your wallet and the order is placed automatically."},
	"cart.waitingManual": {ru: "⏳ Не хватает <b>%s</b>. Автопокупка выключена — оформите заказ вручную после пополнения.",
		en: "⏳ <b>%s</b> short. Automatic purchase is off — place the order yourself after topping up."},
	"cart.expires": {ru: "Заказ хранится до <b>%s</b>.", en: "This cart is kept until <b>%s</b>."},
	"cart.empty": {ru: "🛍 <b>Отложенный заказ</b>\n\nОтложенных заказов нет. Выберите тариф и отложите его, если сейчас не хватает средств.",
		en: "🛍 <b>Saved cart</b>\n\nNothing is saved. Choose a plan and save it if your balance is short right now."},
	"cart.stale": {ru: "🛍 <b>Заказ устарел</b>\n\nТариф или дополнение изменились. Очистите отложенный заказ и соберите его заново.",
		en: "🛍 <b>This cart is out of date</b>\n\nThe plan or an add-on changed. Clear the cart and build it again."},
	"cart.buyNow":                    {ru: "🛒 Купить сейчас", en: "🛒 Buy now"},
	"cart.topUp":                     {ru: "➕ Пополнить кошелёк", en: "➕ Top up wallet"},
	"cart.autoOn":                    {ru: "🤖 Автопокупка включена ✅", en: "🤖 Automatic purchase on ✅"},
	"cart.autoOff":                   {ru: "🤖 Автопокупка выключена ☑️", en: "🤖 Automatic purchase off ☑️"},
	"cart.clear":                     {ru: "🗑 Очистить", en: "🗑 Clear"},
	"cart.rejected":                  {ru: "⚠️ <b>Заказ не оформлен</b>\n\n%s", en: "⚠️ <b>The order was not placed</b>\n\n%s"},
	"cart.cart_insufficient_balance": {ru: "На балансе недостаточно средств.", en: "Your wallet balance does not cover it yet."},
	"cart.cart_plan_unavailable":     {ru: "Тариф или дополнение больше недоступны.", en: "The plan or an add-on is no longer available."},
	"cart.cart_price_changed":        {ru: "Цена изменилась. Соберите заказ заново.", en: "The price changed. Please build the order again."},
	"cart.cart_expired":              {ru: "Срок хранения заказа истёк.", en: "This cart has expired."},
	"cart.cart_auto_purchase_off":    {ru: "Автопокупка выключена.", en: "Automatic purchase is off."},
	"cart.cart_maintenance":          {ru: "Идут технические работы. Попробуйте позже.", en: "Maintenance is in progress. Please try again later."},

	// Concurrent subscriptions.
	"menu.subs":    {ru: "🗂 Мои подписки", en: "🗂 My subscriptions"},
	"subs.title":   {ru: "🗂 <b>Мои подписки</b>", en: "🗂 <b>My subscriptions</b>"},
	"subs.empty":   {ru: "Подписок пока нет.", en: "No subscriptions yet."},
	"subs.until":   {ru: "Действует до: <b>%s</b>", en: "Valid until: <b>%s</b>"},
	"subs.detail":  {ru: "🗂 <b>%s</b>", en: "🗂 <b>%s</b>"},
	"subs.plan":    {ru: "Тариф: <b>%s</b>", en: "Plan: <b>%s</b>"},
	"subs.add":     {ru: "➕ Добавить подписку", en: "➕ Add a subscription"},
	"subs.back":    {ru: "‹ К подпискам", en: "‹ Subscriptions"},
	"subs.devices": {ru: "📱 Устройства", en: "📱 Devices"},
	"subs.rotate":  {ru: "🔄 Обновить ссылку", en: "🔄 Rotate link"},
	"subs.rotateConfirm": {ru: "🔄 <b>Обновить ссылку подписки?</b>\n\nСтарая ссылка перестанет работать на всех устройствах — профиль придётся добавить заново.",
		en: "🔄 <b>Rotate this subscription link?</b>\n\nThe old link stops working on every device and the profile has to be added again."},
	"subs.rename":         {ru: "✏️ Переименовать", en: "✏️ Rename"},
	"subs.renamePrompt":   {ru: "✏️ <b>Название подписки</b>\n\nОтправьте новое название одним сообщением, до 40 символов.", en: "✏️ <b>Subscription name</b>\n\nSend the new name in one message, up to 40 characters."},
	"subs.renameRejected": {ru: "⚠️ Название должно быть от 1 до 40 обычных символов.", en: "⚠️ The name must be 1 to 40 ordinary characters."},
	"subs.gone":           {ru: "⚠️ Подписка не найдена.", en: "⚠️ That subscription was not found."},
	"subs.notProvisioned": {ru: "Доступ ещё настраивается. Обновите экран через минуту.", en: "Access is still being set up. Refresh in a minute."},
	"subs.rejected":       {ru: "⚠️ <b>Нельзя добавить подписку</b>\n\n%s", en: "⚠️ <b>Cannot add a subscription</b>\n\n%s"},
	"subs.multi_subscription_disabled": {ru: "Несколько одновременных подписок отключены администратором.",
		en: "Multiple concurrent subscriptions are turned off by the operator."},
	"subs.subscription_limit_reached": {ru: "Достигнут лимит одновременных подписок.", en: "You have reached the concurrent subscription limit."},
	"subs.plan_limit_reached":         {ru: "Для этого тарифа достигнут лимит одновременных подписок.", en: "This plan's concurrent subscription limit is reached."},

	// Subscription phases used by the switcher.
	"phase.none":          {ru: "Подписки нет", en: "No subscription"},
	"phase.provisioning":  {ru: "⚙️ Активируется", en: "⚙️ Activating"},
	"phase.active":        {ru: "🟢 Активна", en: "🟢 Active"},
	"phase.expiring_soon": {ru: "🟡 Скоро закончится", en: "🟡 Expiring soon"},
	"phase.grace":         {ru: "🟠 Льготный период", en: "🟠 Grace period"},
	"phase.limited":       {ru: "🟠 Лимит исчерпан", en: "🟠 Limit reached"},
	"phase.disabled":      {ru: "⚪️ Отключена", en: "⚪️ Disabled"},
	"phase.expired":       {ru: "🔴 Истекла", en: "🔴 Expired"},
	"phase.failed":        {ru: "⚠️ Активация не завершена", en: "⚠️ Activation unfinished"},

	// Squad configurator.
	"squad.title": {ru: "🌍 <b>Локации</b>", en: "🌍 <b>Locations</b>"},
	"squad.optional": {ru: "Выберите нужные локации — их можно изменить позже, оформив дополнение.",
		en: "Pick the locations you want. You can change them later by buying an add-on."},
	"squad.optionalMax": {ru: "Можно выбрать не больше <b>%d</b>.", en: "You may select up to <b>%d</b>."},
	"squad.required":    {ru: "Нужно выбрать от <b>%d</b> до <b>%d</b> локаций.", en: "Select between <b>%d</b> and <b>%d</b> locations."},
	"squad.requiredMin": {ru: "Нужно выбрать минимум <b>%d</b>.", en: "Select at least <b>%d</b>."},
	"squad.tooFew":      {ru: "Выбрано слишком мало локаций.", en: "Too few locations selected."},
	"squad.tooMany":     {ru: "Выбрано слишком много локаций.", en: "Too many locations selected."},

	// Add-ons.
	"addon.title":       {ru: "🧩 <b>Дополнения</b>", en: "🧩 <b>Add-ons</b>"},
	"addon.title.short": {ru: "🧩 Дополнения", en: "🧩 Add-ons"},
	"addon.empty":       {ru: "🧩 <b>Дополнения</b>\n\nДля этого тарифа дополнений нет.", en: "🧩 <b>Add-ons</b>\n\nThis plan has no add-ons."},
	"addon.unavailable": {ru: "⚠️ Дополнение больше недоступно.", en: "⚠️ That add-on is no longer available."},
	"addon.checkoutHint": {ru: "Выбранные дополнения оплачиваются вместе с тарифом одним платежом и действуют весь период.",
		en: "Selected add-ons are charged with the plan in one payment and last the whole period."},
	"addon.noSubscription": {ru: "⚠️ Дополнение можно купить только к активной подписке.",
		en: "⚠️ An add-on can only be bought for an active subscription."},
	"addon.proration.full_price": {ru: "Оплачивается полностью, срок совпадает с текущим периодом.",
		en: "Charged in full and lasts to the end of the current period."},
	"addon.proration.remaining_period": {ru: "Оплачивается пропорционально оставшемуся времени периода.",
		en: "Charged in proportion to the time left in the current period."},
	"addon.proration.daily_rate": {ru: "Оплачивается по дневной ставке за оставшиеся дни периода.",
		en: "Charged at a daily rate for the days left in the current period."},

	// Maintenance mode.
	"maintenance.title": {ru: "🛠 <b>Технические работы</b>", en: "🛠 <b>Maintenance</b>"},
	"maintenance.body": {ru: "Покупки и активация временно недоступны. Уже оплаченные заказы и активные подписки не затронуты — доступ продолжает работать.",
		en: "Purchases and activation are paused. Orders you already paid for and active subscriptions are untouched, and your access keeps working."},
	"maintenance.until": {ru: "Ожидаемое время возобновления: <b>%s</b>.", en: "Expected back at <b>%s</b>."},

	// Errors.
	"error.generic":   {ru: "⚠️ <b>Не удалось выполнить действие</b>\n\nПопробуйте ещё раз через минуту.", en: "⚠️ <b>That action did not complete</b>\n\nPlease try again in a moment."},
	"error.load":      {ru: "⚠️ <b>Не удалось загрузить данные</b>\n\nПроверьте соединение и повторите запрос.", en: "⚠️ <b>Could not load your data</b>\n\nCheck your connection and try again."},
	"error.order":     {ru: "⚠️ <b>Заказ не создан</b>\n\nПопробуйте ещё раз или выберите другой способ оплаты.", en: "⚠️ <b>The order was not created</b>\n\nTry again or choose another payment method."},
	"error.payment":   {ru: "⚠️ <b>Платёж не создан</b>\n\nПлатёжный сервис недоступен. Попробуйте позже или выберите другой способ.", en: "⚠️ <b>Payment could not be started</b>\n\nThe provider is unavailable. Try later or pick another method."},
	"error.notFound":  {ru: "⚠️ Запись не найдена.", en: "⚠️ That record was not found."},
	"error.forbidden": {ru: "⚠️ Эта операция недоступна для выбранного тарифа.", en: "⚠️ This operation is not allowed for the selected plan."},
}

// formatMoney renders an integer minor-unit amount for display. Telegram Stars
// have no minor units and read better with their own symbol.
func formatMoney(amountMinor int64, currency string) string {
	currency = strings.ToUpper(currency)
	if currency == "XTR" {
		return "⭐ " + strconv.FormatInt(amountMinor, 10)
	}
	exponent, err := payments.CurrencyExponent(currency)
	if err != nil || exponent == 0 {
		return strconv.FormatInt(amountMinor, 10) + " " + currency
	}
	scale := int64(1)
	for range exponent {
		scale *= 10
	}
	whole, fraction := amountMinor/scale, amountMinor%scale
	if fraction == 0 {
		return strconv.FormatInt(whole, 10) + " " + currency
	}
	digits := strconv.FormatInt(fraction, 10)
	return strconv.FormatInt(whole, 10) + "." + strings.Repeat("0", exponent-len(digits)) + digits + " " + currency
}

// formatDuration renders a plan period in whole units a customer recognises.
func formatDuration(locale Locale, duration time.Duration) string {
	days := int(duration.Hours() / 24)
	switch {
	case days >= 365 && days%365 == 0:
		return plural(locale, days/365, phrase{ru: "%d год", en: "%d year"}, phrase{ru: "%d года", en: "%d years"}, phrase{ru: "%d лет", en: "%d years"})
	case days >= 30 && days%30 == 0:
		return plural(locale, days/30, phrase{ru: "%d месяц", en: "%d month"}, phrase{ru: "%d месяца", en: "%d months"}, phrase{ru: "%d месяцев", en: "%d months"})
	case days >= 1:
		return plural(locale, days, phrase{ru: "%d день", en: "%d day"}, phrase{ru: "%d дня", en: "%d days"}, phrase{ru: "%d дней", en: "%d days"})
	default:
		hours := max(int(duration.Hours()), 1)
		return plural(locale, hours, phrase{ru: "%d час", en: "%d hour"}, phrase{ru: "%d часа", en: "%d hours"}, phrase{ru: "%d часов", en: "%d hours"})
	}
}

// plural applies Russian plural rules, which need three forms where English
// needs two.
func plural(locale Locale, count int, one, few, many phrase) string {
	if locale != LocaleRussian {
		form := one
		if count != 1 {
			form = few
		}
		return fmt.Sprintf(form.en, count)
	}
	remainder100, remainder10 := count%100, count%10
	switch {
	case remainder100 >= 11 && remainder100 <= 14:
		return fmt.Sprintf(many.ru, count)
	case remainder10 == 1:
		return fmt.Sprintf(one.ru, count)
	case remainder10 >= 2 && remainder10 <= 4:
		return fmt.Sprintf(few.ru, count)
	default:
		return fmt.Sprintf(many.ru, count)
	}
}

// policyLabel renders a catalog policy value as customer-facing wording.
func policyLabel(locale Locale, policy string) string {
	switch policy {
	case "forbid":
		return text(locale, "policy.forbid")
	case "replace":
		return text(locale, "policy.replace")
	case "extend":
		return text(locale, "policy.extend")
	case "immediate":
		return text(locale, "policy.immediate")
	case "at_expiry":
		return text(locale, "policy.atExpiry")
	default:
		return policy
	}
}
