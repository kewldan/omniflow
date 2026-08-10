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
	"menu.badge":   {ru: " · %d новых", en: " · %d new"},
	"menu.unavail": {ru: "⚠️ <b>Раздел недоступен</b>\n\nПокупки ещё не настроены администратором сервиса. Обратитесь в поддержку.", en: "⚠️ <b>This section is unavailable</b>\n\nPurchases are not configured yet. Please contact support."},
	"menu.rateLimit": {ru: "⏳ Слишком много запросов. Подождите немного и попробуйте снова.",
		en: "⏳ Too many requests. Please wait a moment and try again."},
	"menu.replay": {ru: "Это действие уже выполнено.", en: "This action has already been completed."},

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
	"checkout.pay":          {ru: "💳 Оплатить", en: "💳 Pay"},
	"checkout.changeMethod": {ru: "💱 Другой способ", en: "💱 Change method"},
	"checkout.expired":      {ru: "⌛ <b>Оформление истекло</b>\n\nЦены могли измениться. Начните оформление заново.", en: "⌛ <b>Checkout expired</b>\n\nPrices may have changed. Please start again."},
	"checkout.none":         {ru: "Нет активного оформления. Выберите тариф ещё раз.", en: "No checkout is in progress. Choose a plan again."},

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
	"alert.expiry":          {ru: "⏳ <b>Подписка закончится через %d дн.</b>\n\nПродлите её, чтобы не потерять доступ.", en: "⏳ <b>Your subscription expires in %d day(s)</b>\n\nRenew it to keep your access."},
	"alert.traffic":         {ru: "📡 <b>Использовано %d%% доступного трафика</b>\n\nПри исчерпании лимита подключение будет ограничено.", en: "📡 <b>You have used %d%% of your traffic allowance</b>\n\nYour connection is limited once the allowance runs out."},
	"alert.renewal":         {ru: "♻️ <b>Пора продлить подписку</b>\n\nТариф: <b>%s</b>\nОсталось: <b>%d дн.</b>\nДействует до: <b>%s</b>", en: "♻️ <b>Time to renew</b>\n\nPlan: <b>%s</b>\nRemaining: <b>%d day(s)</b>\nValid until: <b>%s</b>"},
	"alert.fulfillmentDone": {ru: "🎉 <b>Подписка активирована</b>\n\nМожно подключаться.", en: "🎉 <b>Subscription activated</b>\n\nYou can connect now."},
	"alert.fulfillmentFailed": {ru: "⚠️ <b>Активация задерживается</b>\n\nОплата сохранена, мы повторяем активацию автоматически. Если доступ не появится в течение часа — напишите в поддержку.",
		en: "⚠️ <b>Activation is delayed</b>\n\nYour payment is safe and activation retries automatically. If access does not appear within an hour, contact support."},

	// Shared short labels.
	"connect.title.short":      {ru: "🚀 Подключиться", en: "🚀 Connect"},
	"connect.openSubscription": {ru: "🔗 Открыть подписку", en: "🔗 Open subscription"},
	"news.open":                {ru: "📰 Читать", en: "📰 Read"},

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
