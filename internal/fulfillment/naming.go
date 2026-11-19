package fulfillment

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Naming is how a newly created Remnawave user is named and described.
//
// The default — no templates — is the `omniflow_<16 hex>` name derived from the
// subscription, which is what every installation used before. A template is
// rendered once, when the user is created; an existing user keeps its name,
// because the panel cannot rename a user through its update call and the access
// link does not depend on it anyway.
type Naming struct {
	// UsernameTemplate may use {telegram_id}, {short_id} (the subscription's
	// 16-hex identifier), {slot}, and {customer_id} (the first 8 hex of the
	// customer). It must contain {short_id} or {telegram_id}, or two
	// customers would render the same name.
	UsernameTemplate string
	// DescriptionTemplate is the panel's free-text description, with the same
	// placeholders plus {label} (the customer's own name for the subscription).
	DescriptionTemplate string
	// SendTelegramID puts the customer's Telegram ID on the panel user, so the
	// panel's own Telegram features and searches find them.
	SendTelegramID bool
}

// namingFacts are the values a template can draw on for one subscription.
type namingFacts struct {
	TelegramID int64
	ShortID    string
	Slot       int32
	CustomerID string
	Label      string
}

var (
	usernameShape = regexp.MustCompile(`^[A-Za-z0-9_-]{3,36}$`)
	unsafeRunes   = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
	repeatedBars  = regexp.MustCompile(`_{2,}`)
)

// NamingFromEnv reads APP_REMNAWAVE_USERNAME_TEMPLATE,
// APP_REMNAWAVE_DESCRIPTION_TEMPLATE and APP_REMNAWAVE_SEND_TELEGRAM_ID.
func NamingFromEnv(getenv func(string) string) (Naming, error) {
	naming := Naming{
		UsernameTemplate:    strings.TrimSpace(getenv("APP_REMNAWAVE_USERNAME_TEMPLATE")),
		DescriptionTemplate: strings.TrimSpace(getenv("APP_REMNAWAVE_DESCRIPTION_TEMPLATE")),
		SendTelegramID:      true,
	}
	if raw := strings.TrimSpace(getenv("APP_REMNAWAVE_SEND_TELEGRAM_ID")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Naming{}, errors.New("APP_REMNAWAVE_SEND_TELEGRAM_ID must be true or false")
		}
		naming.SendTelegramID = value
	}
	return naming, naming.Validate()
}

// Validate refuses a username template that cannot produce unique names.
func (naming Naming) Validate() error {
	template := naming.UsernameTemplate
	if template == "" {
		return nil
	}
	if !strings.Contains(template, "{short_id}") && !strings.Contains(template, "{telegram_id}") {
		return errors.New("APP_REMNAWAVE_USERNAME_TEMPLATE must contain {short_id} or {telegram_id}")
	}
	return nil
}

func (facts namingFacts) replacer(label bool) *strings.Replacer {
	telegram := ""
	if facts.TelegramID != 0 {
		telegram = strconv.FormatInt(facts.TelegramID, 10)
	}
	customer := strings.ReplaceAll(facts.CustomerID, "-", "")
	if len(customer) > 8 {
		customer = customer[:8]
	}
	pairs := []string{
		"{telegram_id}", telegram, "{short_id}", facts.ShortID,
		"{slot}", strconv.Itoa(int(facts.Slot)), "{customer_id}", customer,
	}
	if label {
		pairs = append(pairs, "{label}", facts.Label)
	}
	return strings.NewReplacer(pairs...)
}

// username renders the template, or returns the default name when there is
// no template or it cannot produce a valid, unique name for these facts — a
// customer who signed in by e-mail has no Telegram ID to put in
// `tg_{telegram_id}`, and an empty placeholder would collide with every other
// such customer.
func (naming Naming) username(facts namingFacts, fallback string) string {
	template := naming.UsernameTemplate
	if template == "" {
		return fallback
	}
	if strings.Contains(template, "{telegram_id}") && facts.TelegramID == 0 &&
		!strings.Contains(template, "{short_id}") {
		return fallback
	}
	rendered := facts.replacer(false).Replace(template)
	// One customer may own several subscriptions; a name that does not carry
	// the subscription's own identifier is told apart by its slot.
	if !strings.Contains(template, "{short_id}") && facts.Slot > 1 &&
		!strings.Contains(template, "{slot}") {
		rendered += "_" + strconv.Itoa(int(facts.Slot))
	}
	rendered = unsafeRunes.ReplaceAllString(rendered, "_")
	rendered = repeatedBars.ReplaceAllString(rendered, "_")
	rendered = strings.Trim(rendered, "_-")
	if !usernameShape.MatchString(rendered) {
		return fallback
	}
	return rendered
}

// description renders the description template; empty means none is sent.
func (naming Naming) description(facts namingFacts) string {
	if naming.DescriptionTemplate == "" {
		return ""
	}
	rendered := strings.Join(strings.Fields(facts.replacer(true).Replace(naming.DescriptionTemplate)), " ")
	if len(rendered) > 1000 {
		rendered = rendered[:1000]
	}
	return rendered
}
