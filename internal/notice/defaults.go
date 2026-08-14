package notice

// The shipped wording, and the variables each notice carries.
//
// This is the authoritative copy. The bot renders from here rather than from
// its own catalogue, so an operator reading "what am I replacing" in the panel
// is reading the same string the customer would have received.
//
// The subscription line is deliberately not a variable. It is prepended by the
// bot when a customer holds more than one subscription and omitted when they
// hold one, which is a decision about context rather than about wording — and
// exposing it would let an override write "Subscription: " around a value that
// is usually empty.

var definitions = []Definition{
	{
		Code: CodeExpiry,
		Variables: []Variable{
			{Name: "days", Purpose: "Whole days until the subscription expires", Sample: "3"},
		},
		Default: map[string]string{
			"en": "⏳ <b>Your subscription expires in {days} day(s)</b>\n\n" +
				"Renew it to keep your access.",
			"ru": "⏳ <b>Подписка закончится через {days} дн.</b>\n\n" +
				"Продлите её, чтобы не потерять доступ.",
		},
	},
	{
		Code: CodeTraffic,
		Variables: []Variable{
			{Name: "percent", Purpose: "Percentage of the traffic allowance used", Sample: "85"},
		},
		Default: map[string]string{
			"en": "📡 <b>You have used {percent}% of your traffic allowance</b>\n\n" +
				"Your connection is limited once the allowance runs out.",
			"ru": "📡 <b>Использовано {percent}% доступного трафика</b>\n\n" +
				"При исчерпании лимита подключение будет ограничено.",
		},
	},
	{
		Code: CodeRenewal,
		Variables: []Variable{
			{Name: "plan", Purpose: "The plan the subscription is on", Sample: "Pro"},
			{Name: "days", Purpose: "Whole days remaining", Sample: "5"},
			{Name: "until", Purpose: "The date access currently runs to", Sample: "12 Sep 2026"},
		},
		Default: map[string]string{
			"en": "♻️ <b>Time to renew</b>\n\nPlan: <b>{plan}</b>\n" +
				"Remaining: <b>{days} day(s)</b>\nValid until: <b>{until}</b>",
			"ru": "♻️ <b>Пора продлить подписку</b>\n\nТариф: <b>{plan}</b>\n" +
				"Осталось: <b>{days} дн.</b>\nДействует до: <b>{until}</b>",
		},
	},
	{
		Code: CodeGrace,
		Variables: []Variable{
			{Name: "until", Purpose: "The date the subscription expired", Sample: "12 Sep 2026"},
			{Name: "graceUntil", Purpose: "The date the grace period ends", Sample: "15 Sep 2026"},
		},
		Default: map[string]string{
			"en": "🟠 <b>Grace period</b>\n\nYour subscription ended on {until}, and " +
				"access continues until <b>{graceUntil}</b>. Renew to keep your connection.",
			"ru": "🟠 <b>Льготный период</b>\n\nПодписка закончилась {until}, но доступ " +
				"сохраняется до <b>{graceUntil}</b>. Продлите её, чтобы не потерять подключение.",
		},
	},
	{
		// Recovery carries no plan name. The shipped wording says the plan and
		// settings are kept without naming them, which is true of an expired
		// subscription whose plan version may since have been superseded —
		// naming a version the customer can no longer buy would be a promise the
		// recovery button cannot honour.
		Code: CodeRecovery,
		Default: map[string]string{
			"en": "🔴 <b>Subscription expired</b>\n\nRestore access in one tap — your " +
				"plan and settings are kept.",
			"ru": "🔴 <b>Подписка истекла</b>\n\nВосстановите доступ в один тап — тариф " +
				"и настройки сохранены.",
		},
	},
	{
		Code: CodeFulfillmentSucceeded,
		Default: map[string]string{
			"en": "🎉 <b>Subscription activated</b>\n\nYou can connect now.",
			"ru": "🎉 <b>Подписка активирована</b>\n\nМожно подключаться.",
		},
	},
	{
		Code: CodeFulfillmentFailed,
		Default: map[string]string{
			"en": "⚠️ <b>Activation is delayed</b>\n\nYour payment is safe and activation " +
				"retries automatically. If access does not appear within an hour, contact support.",
			"ru": "⚠️ <b>Активация задерживается</b>\n\nОплата сохранена, мы повторяем " +
				"активацию автоматически. Если доступ не появится в течение часа — " +
				"напишите в поддержку.",
		},
	},
	{
		// The two dunning notices differ because what the customer has to do
		// differs: the first resolves itself if the card starts working, the
		// second is the end of automatic renewal. An operator rewording them
		// should keep that distinction, which is why they are two entries rather
		// than one with a variable.
		Code: CodeDunningRetry,
		Default: map[string]string{
			"en": "💳 <b>We could not take the payment</b>\n\nWe will try again " +
				"automatically. Your access continues for now — check your payment method " +
				"or renew by hand.",
			"ru": "💳 <b>Не удалось списать оплату</b>\n\nМы попробуем ещё раз " +
				"автоматически. Доступ пока сохраняется — проверьте способ оплаты или " +
				"продлите вручную.",
		},
	},
	{
		Code: CodeDunningAbandoned,
		Default: map[string]string{
			"en": "💳 <b>Automatic renewal has stopped</b>\n\nWe are no longer attempting " +
				"to charge. Renew by hand to keep your access.",
			"ru": "💳 <b>Автопродление остановлено</b>\n\nМы больше не пытаемся списать " +
				"оплату. Продлите подписку вручную, чтобы сохранить доступ.",
		},
	},
}
