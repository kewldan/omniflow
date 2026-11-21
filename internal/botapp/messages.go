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
	// The operator's own wording from the bot section, when set.
	if override, overridden := overrideText(key); overridden {
		message = override
	}
	rendered := message.get(locale)
	if translated, ok := extraCatalog[locale][key]; ok {
		rendered = translated
	}
	if len(args) == 0 {
		return rendered
	}
	return fmt.Sprintf(rendered, args...)
}

// catalog holds every v0.4 customer-facing string. Success, empty, pending, and
// failure states are all represented here in Russian and English.
var catalog = map[string]phrase{
	// Navigation and shared actions.
	"action.back":      {ru: "‹ Назад", en: "‹ Back"},
	"action.menu":      {ru: "‹ В меню", en: "‹ Menu"},
	"maintenance.soon": {ru: "скоро", en: "soon"},
	"action.refresh":   {ru: "🔄 Обновить", en: "🔄 Refresh"},
	"action.retry":     {ru: "🔄 Повторить", en: "🔄 Try again"},
	"action.cancel":    {ru: "Отмена", en: "Cancel"},
	"action.confirm":   {ru: "Подтвердить", en: "Confirm"},
	"action.continue":  {ru: "Продолжить", en: "Continue"},
	"action.terms":     {ru: "📄 Условия обслуживания", en: "📄 Terms of service"},

	// Main menu additions.
	"menu.plans":    {ru: "🛒 Тарифы", en: "🛒 Plans"},
	"menu.orders":   {ru: "🧾 Мои заказы", en: "🧾 My orders"},
	"menu.wallet":   {ru: "👛 Кошелёк", en: "👛 Wallet"},
	"menu.news":     {ru: "📰 Новости", en: "📰 News"},
	"menu.support":  {ru: "💬 Поддержка", en: "💬 Support"},
	"menu.renew":    {ru: "♻️ Продлить", en: "♻️ Renew"},
	"menu.upgrade":  {ru: "⬆️ Сменить тариф", en: "⬆️ Change plan"},
	"menu.shop":     {ru: "🛍 Магазин", en: "🛍 Shop"},
	"menu.gifts":    {ru: "🎁 Подарки", en: "🎁 Gifts"},
	"menu.settings": {ru: "⚙️ Настройки", en: "⚙️ Settings"},
	"menu.offers":   {ru: "✨ Ваше предложение", en: "✨ Your offer"},
	"menu.badge":    {ru: " · %d новых", en: " · %d new"},
	"menu.unavail":  {ru: "⚠️ <b>Раздел недоступен</b>\n\nПокупки ещё не настроены администратором сервиса. Обратитесь в поддержку.", en: "⚠️ <b>This section is unavailable</b>\n\nPurchases are not configured yet. Please contact support."},
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
	"legal.terms":        {ru: "📄 <b>Условия использования</b>\n\nОплачивая заказ, вы принимаете условия по ссылке ниже.", en: "📄 <b>Terms of service</b>\n\nPaying for an order means accepting the terms linked below."},
	"legal.termsMissing": {ru: "📄 Условия использования ещё не опубликованы. Напишите в поддержку.", en: "📄 The terms of service are not published yet. Please contact support."},
	"legal.openTerms":    {ru: "Открыть условия", en: "Open the terms"},
	"legal.paySupport": {ru: "💳 <b>Помощь с оплатой</b>\n\nЕсли деньги списались, а заказ не активировался, или нужен возврат — откройте обращение в поддержку и укажите номер заказа из раздела «Заказы». Оплаченный заказ, который не удалось выдать, возвращается на баланс или исходным способом оплаты.",
		en: "💳 <b>Payment help</b>\n\nIf you were charged and the order did not activate, or you need a refund, open a support ticket with the order number from Orders. A paid order that could not be delivered is refunded to your balance or to the original payment method."},
	"legal.openSupport":    {ru: "Открыть поддержку", en: "Open support"},
	"legal.contact":        {ru: "Написать оператору", en: "Contact an operator"},
	"legal.faq":            {ru: "❓ <b>Инструкции и ответы</b>\n\nКак подключиться на телефоне и компьютере, что делать, если не работает, и ответы на частые вопросы.", en: "❓ <b>Instructions and answers</b>\n\nHow to connect on a phone or computer, what to do when it does not work, and common questions."},
	"legal.openFAQ":        {ru: "Открыть инструкции", en: "Open the instructions"},
	"plans.highlightBadge": {ru: "🔥 <i>выгодно</i>", en: "🔥 <i>best deal</i>"},
	"campaign.bonusWallet": {ru: "🎁 Бонус за регистрацию: на баланс зачислено <b>%s</b>.", en: "🎁 Sign-up bonus: <b>%s</b> added to your balance."},
	"campaign.bonusDays":   {ru: "🎁 Бонус за регистрацию: <b>%d дн.</b> VPN уже ваши — подключение в разделе «Подписка».", en: "🎁 Sign-up bonus: <b>%d days</b> of VPN are yours — connect from your subscription."},
	"campaign.bonusTrial":  {ru: "🎁 Бонус за регистрацию: пробный период на <b>%d дн.</b> уже включён — подключение в разделе «Подписка».", en: "🎁 Sign-up bonus: a <b>%d-day</b> trial is already on — connect from your subscription."},
	"campaign.bonusPlan":   {ru: "🎁 Бонус за регистрацию: тариф на <b>%d дн.</b> уже ваш — подключение в разделе «Подписка».", en: "🎁 Sign-up bonus: <b>%d days</b> of a plan are yours — connect from your subscription."},

	// Partner programme.
	"partner.open":     {ru: "🤝 Партнёрская программа", en: "🤝 Partner programme"},
	"partner.title":    {ru: "🤝 <b>Партнёрская программа</b>", en: "🤝 <b>Partner programme</b>"},
	"partner.intro":    {ru: "Ведёте канал или блог? Станьте партнёром: администратор назначит вам персональный процент с каждого платежа приглашённых. Рассказать, где вы будете продвигать сервис, можно в веб-кабинете — или просто отправьте заявку отсюда.", en: "Run a channel or a blog? Become a partner: the administrator sets you a personal share of every payment your invitees make. You can tell us where you will promote the service in the web panel — or just apply from here."},
	"partner.apply":    {ru: "📝 Подать заявку", en: "📝 Apply"},
	"partner.sent":     {ru: "✅ Заявка отправлена.", en: "✅ Application sent."},
	"partner.pending":  {ru: "Ваша заявка от %s ждёт решения администратора.", en: "Your application of %s is waiting for the administrator's decision."},
	"partner.rejected": {ru: "Прошлую заявку отклонили. Можно подать новую.", en: "Your last application was declined. You may apply again."},
	"partner.active":   {ru: "Вы партнёр. Ваш процент с платежей приглашённых: <b>%s</b>, с первого платежа: <b>%s</b>.", en: "You are a partner. Your share of your invitees' payments: <b>%s</b>; of a first payment: <b>%s</b>."},

	// Daily games.
	"games.open":                  {ru: "🎲 Ежедневные игры", en: "🎲 Daily games"},
	"games.title":                 {ru: "🎲 <b>Ежедневные игры</b>\nРезультат решает криптостойкий генератор случайных чисел.", en: "🎲 <b>Daily games</b>\nResults are drawn by a cryptographically secure random generator."},
	"games.none":                  {ru: "Сейчас игр нет.", en: "There are no games right now."},
	"games.left":                  {ru: "осталось сегодня: %d", en: "left today: %d"},
	"games.closed":                {ru: "призы на сегодня разыграны", en: "today's prizes are taken"},
	"games.doneToday":             {ru: "На сегодня всё — приходите завтра.", en: "That's all for today — come back tomorrow."},
	"games.noQuestion":            {ru: "Сейчас вопроса нет.", en: "There is no question right now."},
	"games.prize":                 {ru: "Приз: <b>%s</b>", en: "Prize: <b>%s</b>"},
	"games.cost":                  {ru: "Участие: %s с баланса", en: "Entry: %s from the balance"},
	"games.streak":                {ru: "Серия: <b>%d</b> дн. подряд", en: "Streak: <b>%d</b> days in a row"},
	"games.days":                  {ru: "%d дн. подписки", en: "%d days of subscription"},
	"games.won":                   {ru: "🎉 <b>Выигрыш!</b> Приз уже начислен.", en: "🎉 <b>You won!</b> The prize is already yours."},
	"games.lost":                  {ru: "Не повезло — попробуйте ещё.", en: "No luck this time."},
	"games.insufficient":          {ru: "На балансе не хватает на участие.", en: "The balance does not cover the entry."},
	"games.tooNew":                {ru: "Аккаунт слишком новый для игр — загляните позже.", en: "The account is too new for games — come back later."},
	"games.prizeHeld":             {ru: "У вас уже есть предложение, которое разыгрывает эта игра. Сначала воспользуйтесь им.", en: "You already hold the offer this game gives. Use it first."},
	"games.name.dice":             {ru: "🎲 Кости", en: "🎲 Dice"},
	"games.name.slots":            {ru: "🎰 Слоты", en: "🎰 Slots"},
	"games.name.guess_number":     {ru: "🔢 Угадай число", en: "🔢 Guess the number"},
	"games.name.lucky_ticket":     {ru: "🎫 Счастливый билет", en: "🎫 Lucky ticket"},
	"games.name.quiz":             {ru: "❓ Викторина", en: "❓ Quiz"},
	"games.name.daily_streak":     {ru: "🔥 Ежедневная серия", en: "🔥 Daily streak"},
	"games.name.first_to_answer":  {ru: "⚡ Кто первый", en: "⚡ First to answer"},
	"games.rules.dice":            {ru: "Бросьте кубик: выпадет %d или больше — приз ваш.", en: "Roll the die: %d or higher wins."},
	"games.rules.slots":           {ru: "Три одинаковых символа — выигрыш.", en: "Three of a kind wins."},
	"games.rules.guess_number":    {ru: "Загадайте число от 1 до %d — его загадают после вашего выбора.", en: "Pick a number from 1 to %d — the secret is drawn after you pick."},
	"games.rules.lucky_ticket":    {ru: "Билет счастливый, если суммы первых и последних трёх цифр равны.", en: "A ticket is lucky when its first three digits add up to its last three."},
	"games.rules.quiz":            {ru: "Ответьте на вопрос — верный ответ получает приз.", en: "Answer the question — a right answer wins."},
	"games.rules.daily_streak":    {ru: "Отмечайтесь каждый день: каждый %d-й день подряд — приз.", en: "Check in every day: every %dth day in a row wins."},
	"games.rules.first_to_answer": {ru: "Приз получают первые %d, кто ответит верно.", en: "The first %d right answers win."},
	"games.play.dice":             {ru: "🎲 Бросить", en: "🎲 Roll"},
	"games.play.slots":            {ru: "🎰 Крутить", en: "🎰 Spin"},
	"games.play.lucky_ticket":     {ru: "🎫 Взять билет", en: "🎫 Draw a ticket"},
	"games.play.daily_streak":     {ru: "✅ Отметиться", en: "✅ Check in"},
	"games.result.dice":           {ru: "Выпало: <b>%d</b>", en: "You rolled <b>%d</b>"},
	"games.result.guess":          {ru: "Ваше число %d, загадано %d", en: "You picked %d; the number was %d"},
	"games.result.ticket":         {ru: "Билет № <b>%s</b>", en: "Ticket <b>%s</b>"},
	"games.result.streak":         {ru: "Серия: %d дн.", en: "Streak: %d days"},
	"games.result.right":          {ru: "✅ Верно!", en: "✅ Right!"},
	"games.result.wrong":          {ru: "❌ Неверно.", en: "❌ Wrong."},
	"winback.body": {ru: "🔥 <b>Скидка %d%% на продление</b>\n\nВозвращайтесь — скидка действует %d ч и применится сама при оплате.\nКод на всякий случай: <code>%s</code>",
		en: "🔥 <b>%d%% off your renewal</b>\n\nCome back — the discount lasts %d h and applies by itself at checkout.\nYour code, just in case: <code>%s</code>"},
	"winback.renew": {ru: "Продлить со скидкой %d%%", en: "Renew with %d%% off"},
	"winback.trialBody": {ru: "🎁 <b>Пробный период закончился</b>\n\nПонравилось? Оформите подписку со скидкой %d%% — она действует %d ч и применится сама при оплате.\nКод на всякий случай: <code>%s</code>",
		en: "🎁 <b>Your trial has ended</b>\n\nEnjoyed it? Subscribe with %d%% off — the discount lasts %d h and applies by itself at checkout.\nYour code, just in case: <code>%s</code>"},
	"winback.trialBuy": {ru: "Выбрать тариф со скидкой %d%%", en: "Choose a plan with %d%% off"},
	"pay.choose": {ru: "💳 <b>Способ оплаты</b>\n\nДоступны только подключённые способы, которые поддерживают валюту тарифа.",
		en: "💳 <b>Payment method</b>\n\nOnly configured methods that support the plan currency are listed."},
	"pay.none": {ru: "⚠️ <b>Нет доступных способов оплаты</b>\n\nАдминистратор ещё не подключил платёжный сервис для этой валюты. Напишите в поддержку.",
		en: "⚠️ <b>No payment method is available</b>\n\nNo payment provider is configured for this currency yet. Please contact support."},
	"pay.telegram_stars": {ru: "⭐ Telegram Stars", en: "⭐ Telegram Stars"},
	"pay.cryptobot":      {ru: "🪙 CryptoBot", en: "🪙 CryptoBot"},
	"pay.yookassa":       {ru: "🏦 ЮKassa", en: "🏦 YooKassa"},
	"pay.manual":         {ru: "🧾 Оплата вручную", en: "🧾 Manual payment"},
	"pay.heleket":        {ru: "🪙 Heleket", en: "🪙 Heleket"},
	"pay.mulenpay":       {ru: "💳 MulenPay", en: "💳 MulenPay"},
	"pay.pal24":          {ru: "💳 PayPalych", en: "💳 PayPalych"},
	"pay.platega":        {ru: "💳 Platega", en: "💳 Platega"},
	"pay.cashera":        {ru: "💳 Cashera", en: "💳 Cashera"},
	"pay.tribute":        {ru: "💎 Tribute", en: "💎 Tribute"},
	"pay.freekassa":      {ru: "💳 FreeKassa", en: "💳 FreeKassa"},
	"pay.kassa_ai":       {ru: "💳 Kassa AI", en: "💳 Kassa AI"},
	"pay.riopay":         {ru: "💳 RioPay", en: "💳 RioPay"},
	"pay.severpay":       {ru: "💳 SeverPay", en: "💳 SeverPay"},
	"pay.rollypay":       {ru: "💳 RollyPay", en: "💳 RollyPay"},
	"pay.aurapay":        {ru: "💳 AuraPay", en: "💳 AuraPay"},
	"pay.wata":           {ru: "💳 WATA", en: "💳 WATA"},
	"pay.cloudpayments":  {ru: "💳 CloudPayments", en: "💳 CloudPayments"},
	"pay.lava":           {ru: "💳 Lava", en: "💳 Lava"},
	"pay.overpay":        {ru: "💳 Overpay", en: "💳 Overpay"},
	"pay.paritypay":      {ru: "💳 ParityPay", en: "💳 ParityPay"},
	"pay.payhot":         {ru: "🔥 HOT Pay", en: "🔥 HOT Pay"},
	"pay.cispay":         {ru: "💳 CisPay", en: "💳 CisPay"},
	"pay.antilopay":      {ru: "💳 Antilopay", en: "💳 Antilopay"},
	"pay.anore":          {ru: "💳 Anore", en: "💳 Anore"},
	"pay.donut":          {ru: "🍩 Donut", en: "🍩 Donut"},
	"pay.etoplatezhi":    {ru: "💳 ЭтоПлатежи", en: "💳 EtoPlatezhi"},
	"pay.jupiter":        {ru: "💳 Jupiter", en: "💳 Jupiter"},
	"pay.tabpay":         {ru: "💳 TabPay", en: "💳 TabPay"},
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
	"checkout.traffic":      {ru: "Трафик: <b>%s</b>", en: "Traffic: <b>%s</b>"},

	"squad.full": {ru: "⚠️ На одном из выбранных серверов нет мест. Выберите другой сервер или напишите в поддержку.", en: "⚠️ One of the chosen servers has no room left. Choose another server or contact support."},

	// The simple subscription: one plan and one Buy button.
	"simple.title":   {ru: "🛒 <b>%s</b>", en: "🛒 <b>%s</b>"},
	"simple.period":  {ru: "Период: <b>%s</b>", en: "Period: <b>%s</b>"},
	"simple.traffic": {ru: "Трафик: <b>%s</b>", en: "Traffic: <b>%s</b>"},
	"simple.devices": {ru: "Устройств: <b>%d</b>", en: "Devices: <b>%d</b>"},
	"simple.price":   {ru: "Цена: <b>%s</b>", en: "Price: <b>%s</b>"},

	// The traffic choice in the checkout.
	"traffic.title": {ru: "📶 <b>Объём трафика</b>", en: "📶 <b>Traffic volume</b>"},

	// Daily plans: the line on the subscription screen, the customer's pause,
	// and the two notices the daily charge sends.
	"daily.running":         {ru: "📅 Посуточная оплата: %s в день с баланса.", en: "📅 Billed daily: %s a day from your balance."},
	"daily.paused":          {ru: "⏸ Посуточная оплата на паузе: списаний нет, доступ отключён.", en: "⏸ Daily billing is paused: nothing is charged and access is off."},
	"daily.suspended":       {ru: "⛔ Доступ приостановлен: баланса не хватило на следующий день (%s). Пополните баланс — подписка возобновится сама.", en: "⛔ Access is suspended: the balance did not cover the next day (%s). Top up and it resumes on its own."},
	"daily.pause":           {ru: "⏸ Приостановить", en: "⏸ Pause"},
	"daily.resume":          {ru: "▶️ Возобновить", en: "▶️ Resume"},
	"daily.pausedDone":      {ru: "⏸ Подписка на паузе. Списания остановлены, доступ отключится в течение минуты.", en: "⏸ The subscription is paused. Charges have stopped and access goes off within a minute."},
	"daily.resumedDone":     {ru: "▶️ Подписка возобновлена. Доступ вернётся в течение минуты, списания продолжатся.", en: "▶️ The subscription is resumed. Access returns within a minute and the daily charges continue."},
	"daily.refused":         {ru: "Сейчас это сделать нельзя: подписка не в том состоянии.", en: "That cannot be done right now: the subscription is not in a state it applies to."},
	"event.dailyLowBalance": {ru: "⚠️ Завтрашний день посуточного тарифа стоит %s, а баланса не хватает на %s. Пополните баланс, иначе доступ приостановится.", en: "⚠️ Tomorrow's day of your daily plan costs %s and your balance is %s short. Top up, or access will be suspended."},
	"event.walletCredited":  {ru: "💳 Оплата, которую вы начали в прежнем боте, дошла: на баланс зачислено %s. Им можно оплатить подписку.", en: "💳 The payment you started in our previous bot arrived: %s is on your balance. You can pay for a subscription with it."},
	"event.dailySuspended":  {ru: "⛔ Посуточный тариф приостановлен: баланса не хватило на следующий день (%s). Пополните баланс — подписка возобновится сама.", en: "⛔ Your daily plan is suspended: the balance did not cover the next day (%s). Top up and it resumes on its own."},
	"traffic.current":       {ru: "Сейчас: <b>%s</b>", en: "Selected: <b>%s</b>"},
	"traffic.perGb":         {ru: "Свой объём: %s за ГБ, от %d до %d ГБ.", en: "Your own volume: %s per GB, from %d to %d GB."},
	"traffic.own":           {ru: "Как в тарифе: %s", en: "As in the plan: %s"},
	"traffic.gb":            {ru: "%d ГБ", en: "%d GB"},
	"traffic.package":       {ru: "%d ГБ — +%s", en: "%d GB — +%s"},
	"traffic.unlimited":     {ru: "без ограничений", en: "unlimited"},
	"traffic.change":        {ru: "📶 Изменить трафик", en: "📶 Change traffic"},
	"traffic.enter":         {ru: "✏️ Ввести объём", en: "✏️ Enter a volume"},
	"traffic.prompt":        {ru: "Напишите нужный объём трафика в гигабайтах, например <code>150</code>.", en: "Type the traffic volume you need in gigabytes, for example <code>150</code>."},
	"traffic.refused":       {ru: "⚠️ Такой объём в этом тарифе не продаётся. Выберите другой.", en: "⚠️ This plan is not sold with that volume. Choose another."},
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

	"channels.required":  {ru: "📣 <b>Сначала подпишитесь</b>\n\nЧтобы продолжить покупку, подпишитесь на:", en: "📣 <b>Please subscribe first</b>\n\nTo continue with your purchase, subscribe to:"},
	"channels.retry":     {ru: "Я подписался", en: "I have subscribed"},
	"channels.open":      {ru: "Открыть %s", en: "Open %s"},
	"channels.warned":    {ru: "📣 <b>Подписка на канал обязательна</b>\n\nВы вышли из канала, без которого подписка не работает. Подпишитесь снова до %s, иначе доступ будет приостановлен:", en: "📣 <b>A channel subscription is required</b>\n\nYou left a channel your subscription depends on. Subscribe again before %s, or access will be suspended:"},
	"channels.suspended": {ru: "⛔️ <b>Доступ приостановлен</b>\n\nВы не подписаны на обязательный канал. Подпишитесь — доступ вернётся автоматически после следующей проверки:", en: "⛔️ <b>Access is suspended</b>\n\nYou are not subscribed to a required channel. Subscribe and access returns automatically at the next check:"},
	"channels.restored":  {ru: "✅ <b>Доступ восстановлен</b>\n\nСпасибо, что подписались снова. Подписка снова работает.", en: "✅ <b>Access is restored</b>\n\nThanks for subscribing again. Your subscription is working again."},

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
	"payment.open":        {ru: "💳 Перейти к оплате", en: "💳 Open payment page"},
	"payment.otherMethod": {ru: "💱 Другой способ оплаты", en: "💱 Pay another way"},
	"payment.invoice":     {ru: "⭐ Оплатить в Telegram", en: "⭐ Pay in Telegram"},
	"payment.expires":     {ru: "\n\nСсылка действует до <b>%s</b>.", en: "\n\nThis payment window closes at <b>%s</b>."},
	"payment.manual":      {ru: "\n\nОператор подтвердит оплату вручную. Мы сообщим, когда заказ будет активирован.", en: "\n\nAn operator will confirm this payment manually. You will be notified once the order is activated."},
	"payment.succeeded":   {ru: "✅ <b>Оплата получена</b>\n\nЗаказ <code>%s</code> оплачен. Готовим подписку…", en: "✅ <b>Payment received</b>\n\nOrder <code>%s</code> is paid. Preparing your subscription…"},
	"payment.provisioning": {ru: "⚙️ <b>Активируем подписку</b>\n\nОплата получена, доступ настраивается. Это занимает до минуты — обновите экран.",
		en: "⚙️ <b>Activating your subscription</b>\n\nPayment received and access is being provisioned. This takes up to a minute — refresh to check."},
	"payment.completed": {ru: "🎉 <b>Готово</b>\n\nПодписка активна. Откройте раздел подключения, чтобы добавить её в приложение.",
		en: "🎉 <b>All set</b>\n\nYour subscription is active. Open the connect screen to add it to your app."},
	// A provisioning run that has needed more than one attempt, and one that
	// gave up. Neither names the worker's error code: that is operator
	// vocabulary, and the customer's next move is the same either way.
	"payment.provisionSlow": {ru: "Активация занимает больше времени, чем обычно. Мы продолжаем попытки автоматически — оплата сохранена.",
		en: "Activation is taking longer than usual. We keep retrying automatically — your payment is safe."},
	"payment.provisionFailed": {ru: "⚠️ <b>Активация не завершилась</b>\n\nОплата получена и сохранена, но доступ настроить не удалось. Напишите в поддержку — мы разберёмся и активируем подписку.",
		en: "⚠️ <b>Activation did not finish</b>\n\nYour payment was received and is safe, but access could not be set up. Contact support and we will sort it out and activate the subscription."},
	// What a paid order bought, when it was not a subscription. Only the worker
	// ever moves a subscription order past paid; a top-up, a gift, and a shop
	// purchase stay at paid for good, so "activating" would be shown forever.
	"payment.topupCredited": {ru: "✅ <b>Кошелёк пополнен</b>\n\nНа баланс зачислено <b>%s</b>.", en: "✅ <b>Wallet topped up</b>\n\n<b>%s</b> was credited to your wallet."},
	"payment.giftPaid":      {ru: "🎁 <b>Подарок оплачен</b>\n\nЕго можно активировать кодом, который вы сохранили.", en: "🎁 <b>Gift paid</b>\n\nIt can be redeemed with the code you saved."},
	"payment.giftState":     {ru: "Код <code>••%s</code> · %s", en: "Code <code>••%s</code> · %s"},
	"payment.goodsPaid":     {ru: "✅ <b>Покупка оплачена</b>\n\nДоставка идёт на указанный аккаунт.", en: "✅ <b>Purchase paid</b>\n\nDelivery is on its way to the account you named."},
	"payment.addonApplying": {ru: "⚙️ <b>Подключаем дополнение</b>\n\nОплата получена, дополнение применяется к подписке. Это занимает до минуты — обновите экран.",
		en: "⚙️ <b>Applying your add-on</b>\n\nPayment received and the add-on is being applied to your subscription. This takes up to a minute — refresh to check."},
	"payment.addonApplied": {ru: "🎉 <b>Дополнение подключено</b>\n\nОно уже действует на вашей подписке.", en: "🎉 <b>Add-on applied</b>\n\nIt is already active on your subscription."},
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
	"wallet.reserved":        {ru: "\n\nЗарезервировано неоплаченными заказами: <b>%s</b>. Деньги вернутся в доступный баланс, когда заказ будет оплачен или отменён в разделе «Мои заказы».", en: "\n\nReserved by unpaid orders: <b>%s</b>. It returns to the available balance once the order is paid or cancelled under My orders."},
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
	// A pause is not a disable, and saying so is the whole point of the state:
	// the customer's remaining days are safe and the figure beside this does
	// not move. Telling them "disabled, contact support" would send somebody to
	// a ticket queue to be told nothing is wrong.
	"life.paused": {ru: "\u23f8 <b>Подписка на паузе</b>\n\nОставшиеся дни сохранены — счётчик остановлен и продолжит идти с того же места, когда подписку возобновят. Напишите в поддержку, если хотите вернуть доступ раньше.",
		en: "\u23f8 <b>Subscription paused</b>\n\nYour remaining days are kept — the clock is stopped and continues from the same point when the subscription is resumed. Contact support if you want access back sooner."},
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
	// The period each renewal buys. "As bought" repeats the period the
	// subscription was last paid for, which is what renewal did before the
	// choice existed.
	"renew.period":         {ru: "Период продления: <b>%s</b>", en: "Renewal period: <b>%s</b>"},
	"renew.periodAsBought": {ru: "как при покупке", en: "as last bought"},
	"renew.periodDays":     {ru: "%d дн. · %s", en: "%d d · %s"},
	"renew.periodShort":    {ru: "%d дн.", en: "%d d"},
	"renew.methods":        {ru: "💳 Способы оплаты", en: "💳 Payment methods"},
	"renew.noMethod": {ru: "У вас нет сохранённого способа оплаты. Он появится после оплаты картой с включённым автопродлением.",
		en: "You have no saved payment method. One appears after you pay by card with auto-renew turned on."},
	"renew.dunning": {ru: "⚠️ Последнее списание не прошло. Мы попробуем ещё раз автоматически.",
		en: "⚠️ The last charge did not go through. We will try again automatically."},
	"renew.suspended": {ru: "⚠️ Автосписание остановлено после нескольких неудачных попыток. Продлите вручную или измените способ оплаты.",
		en: "⚠️ Automatic charging stopped after several failed attempts. Renew by hand or change your payment method."},
	// Stated whenever no card is on file. A customer cannot add one from here —
	// no code path saves a method yet — so the screen says the wallet is the only
	// source rather than hinting at a button that is not there.
	"renew.walletOnly": {ru: "Сохранённой карты нет — списание возможно только с баланса кошелька.",
		en: "No card is saved — the charge can only come from your wallet balance."},
	// Auto-renew is per subscription. With several, the customer names the one
	// to configure before anything is shown.
	"renew.pick": {ru: "♻️ <b>Автопродление</b>\n\nВыберите подписку, которую нужно настроить.",
		en: "♻️ <b>Auto-renew</b>\n\nChoose the subscription to configure."},
	"renew.autoRenew": {ru: "♻️ Автопродление", en: "♻️ Auto-renew"},

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
	"gift.claim":      {ru: "🔑 Активировать код", en: "🔑 Redeem a code"},
	"gift.sentTitle":  {ru: "<b>Ваши подарки</b>", en: "<b>Your gifts</b>"},
	"gift.choosePlan": {ru: "🎁 <b>Какую подписку подарить?</b>", en: "🎁 <b>Which subscription?</b>"},
	"gift.chooseCredit": {ru: "👛 <b>Сколько подарить?</b>\n\nПолучатель сможет потратить сумму на любой тариф.",
		en: "👛 <b>How much?</b>\n\nThe recipient can spend it on any plan."},
	"gift.noCreditPresets": {ru: "Подарочные суммы пока не настроены. Напишите в поддержку.",
		en: "No gift amounts are configured yet. Please contact support."},
	"gift.messagePrompt": {ru: "Напишите короткое сообщение получателю (до 200 символов) или отправьте «-», чтобы обойтись без него.",
		en: "Write a short note for the recipient (up to 200 characters), or send “-” to skip it."},
	"gift.withoutNote": {ru: "🎁 Подарить без сообщения", en: "🎁 Give without a note"},
	// Who a gift is for. A named recipient is given the gift once it is paid
	// and told about it; nobody named leaves the code to the sender.
	"gift.recipientPrompt": {ru: "🎁 <b>Кому подарок?</b>\n\nПришлите @username в Telegram или e-mail получателя — мы доставим подарок сами, как только он будет оплачен. Или нажмите «Просто код» и передайте код сами.",
		en: "🎁 <b>Who is it for?</b>\n\nSend the recipient's Telegram @username or e-mail address and we deliver the gift ourselves once it is paid. Or tap \"Just a code\" and hand the code over yourself."},
	"gift.recipientPromptTelegram": {ru: "🎁 <b>Кому подарок?</b>\n\nПришлите @username получателя в Telegram — мы доставим подарок сами, как только он будет оплачен. Или нажмите «Просто код» и передайте код сами.",
		en: "🎁 <b>Who is it for?</b>\n\nSend the recipient's Telegram @username and we deliver the gift ourselves once it is paid. Or tap \"Just a code\" and hand the code over yourself."},
	"gift.recipientNone":    {ru: "🎟 Просто код", en: "🎟 Just a code"},
	"gift.recipientInvalid": {ru: "⚠️ Это не похоже ни на @username, ни на e-mail.", en: "⚠️ That is neither a @username nor an e-mail address."},
	"gift.recipientSelf":    {ru: "⚠️ Себе подарок отправить нельзя.", en: "⚠️ You cannot send a gift to yourself."},
	"gift.recipientPhantom": {ru: "%s ещё не пользуется нашим ботом — подарок подождёт его первого сообщения.", en: "%s has not used our bot yet — the gift waits for their first message."},
	"gift.recipientDeliveryPhantom": {ru: "📬 %s ещё не пользуется ботом. Подарок дождётся его первого сообщения — до %s — и сразу станет его; мы сообщим вам об этом. Код тоже действует.",
		en: "📬 %s has not used the bot yet. The gift waits for their first message — until %s — and becomes theirs right then; we will tell you. The code works too."},
	"gift.deliveredToSender": {ru: "🎁 Ваш подарок получен: %s уже может им пользоваться.", en: "🎁 Your gift arrived: %s can use it now."},
	"gift.deliveredToEmail":  {ru: "получатель", en: "the recipient"},
	"gift.recipientNoEmail":  {ru: "⚠️ Подарок на e-mail здесь недоступен — пришлите @username или нажмите «Просто код».", en: "⚠️ Gifts by e-mail are not available here — send a @username or tap \"Just a code\"."},
	"gift.recipientFor":      {ru: "Получатель: <b>%s</b>", en: "Recipient: <b>%s</b>"},
	"gift.recipientForEmail": {ru: "Получатель: <b>e-mail</b>", en: "Recipient: <b>e-mail</b>"},
	"gift.recipientDelivery": {ru: "📬 Как только подарок будет оплачен, мы передадим его %s и сообщим об этом. Код тоже действует.",
		en: "📬 Once the gift is paid we hand it to %s and tell them. The code works too."},
	"gift.recipientDeliveryEmail": {ru: "📬 Как только подарок будет оплачен, получатель узнает о нём: в боте, если он у нас есть, или письмом. Код тоже действует.",
		en: "📬 Once the gift is paid the recipient hears about it: here in the bot if they use it, or by e-mail. The code works too."},
	"gift.receivedTitle": {ru: "🎁 <b>Вам подарили %s!</b>", en: "🎁 <b>You have been given %s!</b>"},
	"gift.messageTooLong": {ru: "⚠️ Сообщение длиннее %d символов. Сократите его и отправьте ещё раз.",
		en: "⚠️ The note is longer than %d characters. Shorten it and send it again."},
	"gift.created": {ru: "🎁 <b>Подарок готов</b>\n\nКод активации:", en: "🎁 <b>Your gift is ready</b>\n\nRedemption code:"},
	"gift.codeNotice": {ru: "Мы показываем код один раз и не храним его — сохраните и передайте получателю сами.",
		en: "The code is shown once and not stored — save it and pass it on yourself."},
	"gift.exists": {ru: "🎁 <b>Этот подарок уже создан</b>\n\nКод <code>••%s</code> был показан один раз и не хранится. Откройте заказ, чтобы проверить его состояние, или создайте новый подарок.",
		en: "🎁 <b>This gift already exists</b>\n\nCode <code>••%s</code> was shown once and is not stored. Open the order to check its state, or create a new gift."},
	"gift.expires": {ru: "Действует до: <b>%s</b>", en: "Valid until: <b>%s</b>"},
	"gift.awaitingPayment": {ru: "Подарок станет активируемым сразу после оплаты заказа.",
		en: "The gift becomes redeemable as soon as the order is paid."},
	"gift.claimPrompt": {ru: "Отправьте код подарка или промокод.", en: "Send a gift code or a promo code."},
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
	"connect.title": {ru: "🚀 <b>Подключение</b>\n\nВыберите платформу — покажем приложение и шаги установки.", en: "🚀 <b>Connect</b>\n\nPick your platform for the recommended app and setup steps."},
	// The platform labels are no longer here. They are rows in
	// `connect_platforms`, seeded with exactly these five strings, because an
	// operator who adds a platform cannot add a key to a compiled catalogue.
	"connect.steps": {ru: "%s\n\n1. Установите <b>%s</b>.\n2. Нажмите кнопку добавления профиля ниже. Если приложение не открылось, скопируйте ссылку и вставьте её в приложение вручную.\n3. Подключитесь.\n\nНикому не пересылайте ссылку подписки.",
		en: "%s\n\n1. Install <b>%s</b>.\n2. Tap the add-profile button below. If no app opens, copy the link and import it manually.\n3. Connect.\n\nNever share your subscription link."},
	"connect.deepLink": {ru: "📲 Добавить в %s", en: "📲 Add to %s"},
	"connect.copyLink": {ru: "📋 Скопировать ссылку", en: "📋 Copy link"},
	"connect.noLink":   {ru: "Ссылка подписки появится после активации доступа.", en: "Your subscription link appears once access is active."},
	"connect.download": {ru: "⬇️ Скачать %s", en: "⬇️ Get %s"},
	"connect.noClients": {ru: "Список приложений пока не настроен. Ссылку подписки можно скопировать и импортировать в любой совместимый клиент.",
		en: "No applications are configured yet. You can copy the subscription link and import it into any compatible client."},

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
	"support.earlierHidden": {ru: "<i>Ещё %d более ранних сообщений не поместились на экран.</i>",
		en: "<i>%d earlier messages did not fit on this screen.</i>"},
	"support.reply":         {ru: "✍️ Ответить", en: "✍️ Reply"},
	"support.close":         {ru: "✅ Закрыть обращение", en: "✅ Close request"},
	"support.reopen":        {ru: "♻️ Открыть заново", en: "♻️ Reopen"},
	"support.status.open":   {ru: "открыто", en: "open"},
	"support.status.closed": {ru: "закрыто", en: "closed"},
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

	// A test notification, sent by an operator to establish that notifications
	// reach this customer at all. It says who sent it and when, because an
	// unexplained message from a VPN provider is worse than no message.
	"notifications.test": {
		ru: "🔔 <b>Проверка уведомлений</b>\n\nЭто тестовое сообщение отправил оператор, чтобы убедиться, что уведомления до вас доходят. Ничего делать не нужно.\n\n<code>%s</code>",
		en: "🔔 <b>Notification test</b>\n\nAn operator sent this test message to confirm that notifications reach you. Nothing is required of you.\n\n<code>%s</code>",
	},

	// News inbox.
	"news.title":                 {ru: "📰 <b>Новости и объявления</b>", en: "📰 <b>News and announcements</b>"},
	"news.empty":                 {ru: "📰 <b>Новости и объявления</b>\n\nПока ничего нового.", en: "📰 <b>News and announcements</b>\n\nNothing new right now."},
	"news.row":                   {ru: "%s%s · %s", en: "%s%s · %s"},
	"news.unread":                {ru: "🔵 ", en: "🔵 "},
	"news.featured":              {ru: "📌 ", en: "📌 "},
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
	"settings.timezone":  {ru: "🌍 Часовой пояс: %s", en: "🌍 Time zone: %s"},
	"settings.quietZone": {ru: "Время считается по часовому поясу: <b>%s</b>.", en: "Hours are counted in your time zone: <b>%s</b>."},
	"settings.quietUTC": {ru: "Часовой пояс не выбран, поэтому используется UTC. Выберите свой, чтобы тихие часы совпали с вашей ночью.",
		en: "No time zone is set, so UTC is used. Choose yours so quiet hours fall at your night."},
	"tz.title": {ru: "🌍 <b>Часовой пояс</b>\n\nПо нему считаются тихие часы. Сейчас: <b>%s</b>.",
		en: "🌍 <b>Time zone</b>\n\nQuiet hours are counted in it. Now: <b>%s</b>."},
	"tz.suggest": {ru: "Судя по языку Telegram, вам может подойти <b>%s</b>. Это так?",
		en: "Your Telegram language suggests <b>%s</b>. Is that right?"},
	"tz.useSuggested":  {ru: "✅ Да, %s", en: "✅ Yes, %s"},
	"settings.quietOn": {ru: "%02d:00–%02d:00", en: "%02d:00–%02d:00"},
	"settings.quietTitle": {ru: "🌙 <b>Тихие часы</b>\n\nВ это время мы не присылаем несрочные сообщения. Уведомления об оплате, активации и ответах поддержки приходят всегда.",
		en: "🌙 <b>Quiet hours</b>\n\nNon-urgent messages wait during this window. Payment, activation, and support replies always arrive."},
	"settings.marketingNote":     {ru: "\n\nРекламные сообщения приходят только с вашего согласия и не чаще %d раз в неделю.", en: "\n\nMarketing messages require your consent and are limited to %d per week."},
	"settings.marketingLongNote": {ru: " И не больше %d за %d дней.", en: " And no more than %d in %d days."},

	// Push notifications.
	"alert.subscription": {ru: "🗂 Подписка: <b>%s</b>", en: "🗂 Subscription: <b>%s</b>"},
	// The transactional notices an operator may reword — expiry, traffic,
	// renewal, grace, recovery, fulfillment, and the two dunning messages — live
	// in `internal/notice` rather than here. They are the only strings the panel
	// has to be able to show and replace, so they need a form an operator can
	// write (`{days}`, not `%d`) and a home a process with no bot in it can read.
	// Everything else in this catalogue is bot vocabulary and stays compiled in.

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
	"phase.paused":        {ru: "⏸ На паузе", en: "⏸ Paused"},
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
	// The confirmation shown before an add-on is charged. The wallet settles an
	// add-on it can cover the moment the order is created, so the amount and
	// where it comes from are stated before the tap that spends it.
	"referral.qrCaption": {ru: "📷 <b>Ваше приглашение</b>\n\nДайте другу отсканировать код — он откроет бота по вашей ссылке.\n%s", en: "📷 <b>Your invitation</b>\n\nLet a friend scan the code: it opens the bot through your link.\n%s"},
	// Mid-period capacity changes: switching the traffic package. The preview
	// states what is charged, what is credited, and where the money goes.
	"capacity.confirmTitle":       {ru: "🧾 <b>Предпросмотр изменения</b>", en: "🧾 <b>Preview of the change</b>"},
	"capacity.charge":             {ru: "Новое на оставшееся время: <b>%s</b>", en: "New capacity for the time left: <b>%s</b>"},
	"capacity.credit":             {ru: "Остаток заменяемого: <b>−%s</b>", en: "Unused part of what it replaces: <b>−%s</b>"},
	"capacity.wallet":             {ru: "С кошелька: <b>−%s</b>", en: "From your wallet: <b>−%s</b>"},
	"capacity.external":           {ru: "К оплате: <b>%s</b>", en: "To pay: <b>%s</b>"},
	"capacity.nothingDue":         {ru: "Доплачивать ничего не нужно.", en: "Nothing is left to pay."},
	"capacity.toWallet":           {ru: "Вернётся на кошелёк: <b>%s</b>", en: "Back to your wallet: <b>%s</b>"},
	"capacity.confirm":            {ru: "Подтвердить", en: "Confirm"},
	"capacity.squadsButton":       {ru: "🌍 Дополнительные серверы", en: "🌍 Server add-ons"},
	"capacity.squadsTitle":        {ru: "🌍 <b>Дополнительные серверы</b>", en: "🌍 <b>Server add-ons</b>"},
	"capacity.squadsExplain":      {ru: "Отключённое дополнение перестаёт действовать сразу, а его остаток вернётся на кошелёк по цене, которую вы заплатили.", en: "A removed add-on stops at once, and what is left of it goes back to your wallet at what you paid."},
	"capacity.squadsConfirm":      {ru: "Отключить «%s» до конца периода.", en: "Remove “%s” for the rest of this period."},
	"capacity.squadsUnavailable":  {ru: "У этой подписки нет дополнительных серверов, которые можно отключить.", en: "This subscription has no server add-on to remove."},
	"capacity.devicesButton":      {ru: "📱 Меньше устройств", en: "📱 Fewer devices"},
	"capacity.devicesTitle":       {ru: "📱 <b>Лимит устройств</b>", en: "📱 <b>Device limit</b>"},
	"capacity.devicesCurrent":     {ru: "Сейчас лимит <b>%d</b>. Купленные слоты можно вернуть до <b>%d</b>; их остаток вернётся на кошелёк по цене, которую вы заплатили.", en: "The limit is now <b>%d</b>. Slots you bought can be given back down to <b>%d</b>; what is left of them goes back to your wallet at what you paid."},
	"capacity.devicesOption":      {ru: "Снизить до %d", en: "Lower to %d"},
	"capacity.devicesConfirm":     {ru: "Снизить лимит устройств до %d до конца периода.", en: "Lower the device limit to %d for the rest of this period."},
	"capacity.devicesExcess":      {ru: "Лишних устройств: <b>%d</b> при лимите %d. Сначала отключите их — дольше всего не появлялись эти:", en: "<b>%d</b> devices are over a limit of %d. Disconnect them first — these were seen least recently:"},
	"capacity.devicesUnavailable": {ru: "Для этой подписки лимит устройств снизить нельзя.", en: "This subscription cannot lower its device limit."},
	"capacity.trafficButton":      {ru: "📶 Пакет трафика", en: "📶 Traffic package"},
	"capacity.trafficTitle":       {ru: "📶 <b>Пакет трафика</b>", en: "📶 <b>Traffic package</b>"},
	"capacity.trafficCurrent":     {ru: "Сейчас пакеты добавляют к периоду <b>%s</b>. Больший пакет стоит пропорционально оставшемуся времени за вычетом остатка текущего; при меньшем разница вернётся на кошелёк.", en: "Your packages now add <b>%s</b> to this period. A bigger package costs its price for the time left less what is left of the current one; with a smaller one the difference goes back to your wallet."},
	"capacity.trafficCurrentNone": {ru: "Пакет трафика к этому периоду не добавлен. Пакет стоит пропорционально оставшемуся времени.", en: "No traffic package is added to this period. A package costs its price for the time left."},
	"capacity.trafficNone":        {ru: "Без пакета", en: "No package"},
	"capacity.trafficConfirm":     {ru: "Перейти на «%s» (%s) до конца периода.", en: "Switch to “%s” (%s) for the rest of this period."},
	"capacity.trafficUnavailable": {ru: "Для этой подписки пакет трафика сменить нельзя.", en: "This subscription cannot change its traffic package."},
	"reset.button":                {ru: "🔄 Сбросить трафик · %s", en: "🔄 Reset traffic · %s"},
	"reset.confirmTitle":          {ru: "🔄 <b>Сброс трафика</b>", en: "🔄 <b>Traffic reset</b>"},
	"reset.explain":               {ru: "Счётчик трафика подписки обнулится, и лимит снова будет доступен полностью. Срок подписки не меняется.", en: "The subscription's traffic counter goes back to zero and the full allowance is available again. The end date does not change."},
	"reset.price":                 {ru: "Стоимость: <b>%s</b>", en: "Price: <b>%s</b>"},
	"reset.fromWallet":            {ru: "Спишем с баланса (на балансе %s).", en: "Paid from your wallet (balance %s)."},
	"reset.byProvider":            {ru: "На балансе не хватает — после подтверждения выберете способ оплаты.", en: "Your wallet does not cover it, so you will pick a payment method next."},
	"reset.confirm":               {ru: "Сбросить трафик", en: "Reset traffic"},
	"reset.unavailable":           {ru: "Для этой подписки платный сброс трафика недоступен.", en: "A paid traffic reset is not available for this subscription."},
	"addon.confirmTitle":          {ru: "🧩 <b>Подтвердите дополнение</b>", en: "🧩 <b>Confirm the add-on</b>"},
	"addon.charge":                {ru: "К оплате сейчас: <b>%s</b>", en: "Charged now: <b>%s</b>"},
	"addon.walletCovers": {ru: "👛 Спишем с баланса кошелька (доступно %s) — дополнение подключится сразу.",
		en: "👛 Paid from your wallet (available %s) — the add-on is applied right away."},
	"addon.walletPartial": {ru: "👛 С баланса спишем %s, остаток <b>%s</b> оплатите выбранным способом.",
		en: "👛 %s comes from your wallet; the remaining <b>%s</b> is paid with a method you choose next."},
	"addon.walletNone": {ru: "Способ оплаты вы выберете на следующем шаге.",
		en: "You choose the payment method on the next step."},
	"addon.confirm": {ru: "✅ Подтвердить", en: "✅ Confirm"},
	// An add-on the wallet cannot cover is saved and bought automatically once
	// a top-up settles, instead of opening an order for the shortfall.
	"addon.saveCart": {ru: "🛍 Купить после пополнения", en: "🛍 Buy after a top-up"},
	"cart.addonItem": {ru: "Дополнение: <b>%s</b>", en: "Add-on: <b>%s</b>"},
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

	// A customer an operator suspended or deleted. Support stays reachable: an
	// operator suspends a customer to talk to them, and the customer wants to
	// talk back.
	"account.suspended": {ru: "⛔ <b>Аккаунт приостановлен</b>\n\nДоступ к покупкам и подписке временно закрыт администратором сервиса. Напишите в поддержку — мы разберёмся.",
		en: "⛔ <b>Account suspended</b>\n\nPurchases and your subscription are on hold by the service administrator. Contact support and we will sort it out."},
	"account.deleted": {ru: "⛔ <b>Аккаунт удалён</b>\n\nЭтот аккаунт был удалён. Если это ошибка, напишите в поддержку.",
		en: "⛔ <b>Account deleted</b>\n\nThis account has been deleted. If that is a mistake, contact support."},
	"account.contact": {ru: "💬 Написать в поддержку", en: "💬 Contact support"},

	// Errors.
	"error.generic":   {ru: "⚠️ <b>Не удалось выполнить действие</b>\n\nПопробуйте ещё раз через минуту.", en: "⚠️ <b>That action did not complete</b>\n\nPlease try again in a moment."},
	"error.load":      {ru: "⚠️ <b>Не удалось загрузить данные</b>\n\nПроверьте соединение и повторите запрос.", en: "⚠️ <b>Could not load your data</b>\n\nCheck your connection and try again."},
	"error.order":     {ru: "⚠️ <b>Заказ не создан</b>\n\nПопробуйте ещё раз или выберите другой способ оплаты.", en: "⚠️ <b>The order was not created</b>\n\nTry again or choose another payment method."},
	"error.payment":   {ru: "⚠️ <b>Платёж не создан</b>\n\nПлатёжный сервис недоступен. Попробуйте позже или выберите другой способ.", en: "⚠️ <b>Payment could not be started</b>\n\nThe provider is unavailable. Try later or pick another method."},
	"error.notFound":  {ru: "⚠️ Запись не найдена.", en: "⚠️ That record was not found."},
	"error.forbidden": {ru: "⚠️ Эта операция недоступна для выбранного тарифа.", en: "⚠️ This operation is not allowed for the selected plan."},

	// ---------------------------------------------------------------------
	// Support delivery and ticket state (kept together; see notifier.go,
	// support_store.go, and view_support.go).
	// ---------------------------------------------------------------------

	// A system notice about the conversation itself — closed, resolved, or
	// merged by an operator — pushed through the same path as a reply.
	"support.systemAlert": {ru: "⚙️ <b>Обращение обновлено</b>\n\n%s\n\n%s", en: "⚙️ <b>Request updated</b>\n\n%s\n\n%s"},

	// The three statuses the desk can set that the bot used to render as raw
	// catalogue keys. They are the record's own vocabulary, shared with the web.
	"support.status.pending":  {ru: "ждёт вашего ответа", en: "waiting for you"},
	"support.status.resolved": {ru: "решено", en: "resolved"},
	"support.status.merged":   {ru: "объединено", en: "merged"},
	"support.pendingHint":     {ru: "Поддержка ждёт вашего ответа — напишите, и обращение вернётся в работу.", en: "Support is waiting for you — reply and the request goes back to the operator."},
	"support.resolvedHint":    {ru: "Если вопрос не решён, просто ответьте — обращение откроется заново.", en: "If this did not solve it, just reply — the request reopens."},
	"support.mergedHint":      {ru: "Это обращение объединено с другим. Продолжение разговора — в списке обращений.", en: "This request was merged into another one. The conversation continues there — see your request list."},
	"support.merged":          {ru: "⚠️ Обращение объединено с другим. Откройте список обращений и продолжите там.", en: "⚠️ This request was merged into another one. Open your request list and continue there."},
	// Storage failed for a reason that is not the customer's: no default queue,
	// a database error. Saying "1–4000 characters" here blamed the customer for
	// an installation fault.
	"support.unavailable": {ru: "⚠️ <b>Поддержка сейчас недоступна</b>\n\nСообщение не сохранено. Попробуйте через минуту.", en: "⚠️ <b>Support is unavailable right now</b>\n\nYour message was not saved. Please try again in a minute."},
	// A bare message typed after a support push went to the request it
	// answers. The confirmation names the request, because the customer did not
	// choose it explicitly and must be able to tell if the bot guessed wrong.
	"support.routed": {ru: "✅ <b>Сообщение добавлено в обращение</b>\n\n«%s» — поддержка увидит его в этой переписке.", en: "✅ <b>Added to your request</b>\n\n“%s” — support will see it in that conversation."},

	// ---------------------------------------------------------------------
	// Notices worded from the Remnawave panel's own webhook events.
	// ---------------------------------------------------------------------
	"panel.firstConnected": {ru: "🎉 <b>Подключение работает</b>\n\nПервое устройство подключилось к VPN. Приятного пользования!", en: "🎉 <b>You are connected</b>\n\nYour first device has connected to the VPN. Enjoy!"},
	"panel.notConnected":   {ru: "👋 <b>Вы ещё не подключились</b>\n\nПодписка активна, но ни одно устройство пока не подключилось. Нажмите «Подключиться» — покажем приложение и шаги.", en: "👋 <b>You have not connected yet</b>\n\nYour subscription is active, but no device has connected so far. Tap Connect for the app and the steps."},
	"panel.trafficReset":   {ru: "🔄 <b>Трафик обнулён</b>\n\nСчётчик трафика подписки сброшен — лимит снова доступен полностью.", en: "🔄 <b>Traffic reset</b>\n\nYour subscription's traffic counter was reset — the full allowance is available again."},
	"panel.revoked":        {ru: "🔑 <b>Ссылка подписки перевыпущена</b>\n\nСтарая ссылка больше не работает. Откройте «Подключиться» и добавьте новую в приложение на каждом устройстве.", en: "🔑 <b>Your subscription link was re-issued</b>\n\nThe old link no longer works. Open Connect and add the new one to the app on each device."},
	"panel.torrent":        {ru: "🚫 <b>Торрент-трафик заблокирован</b>\n\nС вашей подписки обнаружен торрент-трафик, и сервер его заблокировал. Если это были не вы, перевыпустите ссылку подписки.", en: "🚫 <b>Torrent traffic blocked</b>\n\nTorrent traffic was detected on your subscription and the server blocked it. If this was not you, re-issue your subscription link."},
	"panel.deviceAdded":    {ru: "📱 <b>Новое устройство</b>\n\nК подписке подключилось устройство: %s.\n\nЕсли это не вы, удалите его в разделе «Устройства» и перевыпустите ссылку.", en: "📱 <b>New device</b>\n\nA device connected to your subscription: %s.\n\nIf this was not you, remove it under Devices and re-issue your link."},
	"panel.deviceDeleted":  {ru: "📴 <b>Устройство отключено</b>\n\nОт подписки отключено устройство: %s.", en: "📴 <b>Device removed</b>\n\nA device was disconnected from your subscription: %s."},
	"panel.deviceUnnamed":  {ru: "без названия", en: "unnamed"},
	"panel.devicesButton":  {ru: "📱 Устройства", en: "📱 Devices"},
	// Connect screen additions: mirrors, the guide behind a one-tap mode, and
	// the Mini App button.
	"connect.mirror":      {ru: "🪞 Ссылка через «%s»", en: "🪞 Link via %s"},
	"connect.allApps":     {ru: "📖 Все приложения и инструкции", en: "📖 All apps and instructions"},
	"connect.miniApp":     {ru: "🚀 Подключить в приложении", en: "🚀 Connect in the app"},
	"menu.miniApp":        {ru: "Кабинет", en: "Account"},
	"subs.rotateDisabled": {ru: "🔐 Перевыпуск ссылки в этом сервисе отключён. Если ссылка утекла, напишите в поддержку.", en: "🔐 Re-issuing the link is turned off on this service. If your link leaked, contact support."},
	"subs.rotateCooldown": {ru: "⏳ Ссылку недавно перевыпускали. Повторить можно через %s.", en: "⏳ The link was re-issued recently. You can do it again in %s."},
	"reminder.open":       {ru: "Открыть", en: "Open"},
	// Pay and keep the card for automatic renewal: the button is the consent.
	"pay.optionSave": {ru: "%s + сохранить карту для автопродления", en: "%s + save the card for auto-renew"},
	// Marketing, loyalty, and referrals.
	"checkout.loyaltyDiscount":    {ru: "  в т. ч. уровень лояльности: −%s", en: "  of which your loyalty tier: −%s"},
	"checkout.credit":             {ru: "Остаток текущего тарифа (%s): <b>−%s</b>", en: "Unused time on your current plan (%s): <b>−%s</b>"},
	"checkout.creditToWallet":     {ru: "Вернётся на кошелёк: <b>%s</b>", en: "Back to your wallet: <b>%s</b>"},
	"event.referralJoined":        {ru: "🎉 <b>По вашей ссылке пришёл новый пользователь</b>\n\nНаграда придёт, когда он оплатит подписку.", en: "🎉 <b>Somebody joined through your link</b>\n\nYour reward arrives when they pay for a subscription."},
	"event.referralWelcomeDays":   {ru: "👋 <b>Вас пригласил друг</b>\n\nПосле первой оплаты подписки вы получите ещё %d дн. в подарок.", en: "👋 <b>A friend invited you</b>\n\nAfter your first subscription payment you get %d extra days."},
	"event.referralWelcomeMoney":  {ru: "👋 <b>Вас пригласил друг</b>\n\nПосле первой оплаты подписки на баланс придёт %s.", en: "👋 <b>A friend invited you</b>\n\nAfter your first subscription payment, %s lands on your balance."},
	"event.referralRewardInviter": {ru: "🎁 <b>Приглашённый друг оплатил подписку</b>\n\nВаша награда: %s.", en: "🎁 <b>Your invited friend paid</b>\n\nYour reward: %s."},
	"event.referralRewardInvitee": {ru: "🎁 <b>Бонус за приглашение начислен</b>\n\nВы получили: %s.", en: "🎁 <b>Your invite bonus is in</b>\n\nYou received: %s."},
	"event.referralOpen":          {ru: "👥 Рефералы", en: "👥 Referrals"},
	"event.loyaltyUp":             {ru: "⭐ <b>Новый уровень лояльности: %s</b>", en: "⭐ <b>New loyalty tier: %s</b>"},
	"event.loyaltyDown":           {ru: "ℹ️ <b>Уровень лояльности изменён: %s</b>", en: "ℹ️ <b>Your loyalty tier is now %s</b>"},
	"event.loyaltyDiscount":       {ru: "Скидка на тарифы: %s.", en: "Your plan discount: %s."},
	"template.friend":             {ru: "друг", en: "friend"},
	"restricted.payment":          {ru: "🚫 <b>Эта оплата для вашего аккаунта недоступна.</b>\nНапишите в поддержку, если считаете это ошибкой.", en: "🚫 <b>This payment is not available for your account.</b>\nWrite to support if you think this is a mistake."},
	"moderation.nameRefused":      {ru: "Ваше имя в Telegram похоже на имя сотрудника сервиса или содержит ссылку. Измените имя и напишите снова.", en: "Your Telegram name looks like a member of our staff or contains a link. Change your name and write again."},
	"support.blocked":             {ru: "🚫 Поддержка для вашего аккаунта недоступна.", en: "🚫 Support is not available for your account."},
	"support.newPayment":          {ru: "💳 Проблема с оплатой", en: "💳 Payment problem"},
	"support.newUrgent":           {ru: "🔥 Срочно", en: "🔥 Urgent"},
	"support.contactOnly":         {ru: "💬 Напишите нам — мы ответим.", en: "💬 Write to us and we will answer."},
	"menu.cabinet":                {ru: "🖥 Личный кабинет", en: "🖥 My account"},
	"menu.pages":                  {ru: "📄 Правила и FAQ", en: "📄 Rules and FAQ"},
	"pages.list":                  {ru: "📄 <b>Правила, оферта и ответы на вопросы</b>", en: "📄 <b>Rules, offer, and answers</b>"},
	"pages.empty":                 {ru: "Страниц пока нет.", en: "No pages yet."},
	"access.inviteOnly":           {ru: "🔒 Регистрация только по приглашению. Откройте бота по ссылке-приглашению.", en: "🔒 Registration is by invitation only. Open the bot from an invitation link."},
	"access.closed":               {ru: "🔒 Регистрация новых пользователей сейчас закрыта.", en: "🔒 Registration of new users is closed at the moment."},
	// Provider methods shown as separate buttons, after the provider's name.
	"pay.method.sbp":           {ru: "СБП", en: "SBP"},
	"pay.method.card":          {ru: "карта", en: "card"},
	"pay.method.sberpay":       {ru: "SberPay", en: "SberPay"},
	"pay.method.crypto":        {ru: "криптовалюта", en: "crypto"},
	"pay.method.intl":          {ru: "зарубежная карта", en: "foreign card"},
	"wallet.supportTopUp":      {ru: "💬 Пополнить через поддержку", en: "💬 Top up through support"},
	"gift.claimedTraffic":      {ru: "К текущему периоду подписки добавлено %d ГБ трафика.", en: "%d GB of traffic was added to your current subscription period."},
	"gift.claimedSquad":        {ru: "К подписке на время открыт дополнительный сервер — до %s.", en: "An extra server is open on your subscription until %s."},
	"gift.claimedTier":         {ru: "Вам открыт новый уровень лояльности — скидки уже действуют.", en: "A new loyalty tier is now yours — its discounts already apply."},
	"event.renewalLowBalance":  {ru: "💳 <b>Не хватает баланса для автопродления</b>\n\nЧерез %s подписка продлится с баланса, но на нём не хватает %s. Пополните кошелёк, чтобы продление прошло.", en: "💳 <b>Your balance will not cover auto-renew</b>\n\nIn %s your subscription renews from your wallet, which is %s short. Top up so the renewal goes through."},
	"code.claimedTraffic":      {ru: "К подписке добавлено <b>%d ГБ</b> трафика.", en: "<b>%d GB</b> of traffic added to your subscription."},
	"code.claimedTier":         {ru: "Вы в группе «<b>%s</b>».", en: "You are now in the “<b>%s</b>” group."},
	"promo.promo_daily_limit":  {ru: "Сегодня вы уже использовали максимум разных промокодов. Попробуйте завтра.", en: "You have used as many different promo codes today as allowed. Try again tomorrow."},
	"terms.gate":               {ru: "📄 <b>Условия использования</b>\n\nЧтобы пользоваться сервисом, прочитайте оферту и правила и подтвердите согласие.", en: "📄 <b>Terms of service</b>\n\nTo use the service, read the offer and the rules and confirm that you accept them."},
	"terms.read":               {ru: "📖 Читать условия", en: "📖 Read the terms"},
	"terms.accept":             {ru: "✅ Принимаю", en: "✅ I accept"},
	"welcome.referralCode":     {ru: "🎟 У меня есть код приглашения", en: "🎟 I have an invite code"},
	"welcome.referralHint":     {ru: "Если вас пригласил друг, введите его код приглашения.", en: "If a friend invited you, enter their invite code."},
	"welcome.referralPrompt":   {ru: "🎟 <b>Код приглашения</b>\n\nОтправьте код друга одним сообщением.", en: "🎟 <b>Invite code</b>\n\nSend your friend's code in one message."},
	"welcome.referralAccepted": {ru: "✅ Код принят: вы пришли по приглашению друга.", en: "✅ Code accepted: you joined through a friend's invite."},
	"welcome.referralRefused":  {ru: "Код не подошёл. Код приглашения можно ввести только новому пользователю и только один раз.", en: "That code did not work. An invite code can be entered only by a new customer, and only once."},
	"event.referralCommission": {ru: "💸 <b>Процент с оплаты друга</b>\n\nНа баланс начислено: %s.", en: "💸 <b>Commission on a friend's payment</b>\n\nCredited to your balance: %s."},
	"offer.bonusCredited":      {ru: "🎉 На баланс зачислен бонус: <b>%s</b>.", en: "🎉 A bonus of <b>%s</b> was credited to your balance."},
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
