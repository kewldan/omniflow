package customerauth

// ProviderPreset is a starting point for one well-known identity provider.
//
// A preset is data, not a code path. Choosing one in the panel fills the same
// form an operator could have typed by hand and produces an ordinary
// `customer_oidc_providers` row; nothing downstream ever asks which preset a
// provider came from, and no branch anywhere tests for "google" or "discord".
// That is the property the roadmap asks for, and keeping presets as values is
// the only way to keep it true as more are added.
//
// What a preset cannot supply is the client credentials, which come from the
// operator's own registration with that provider, or the redirect URI, which
// depends on the installation's host.
type ProviderPreset struct {
	Slug        string
	DisplayName string
	Issuer      string
	// DiscoveryURL is the provider's OpenID configuration document. Every field
	// the flow needs — authorization, token, and userinfo endpoints, and the
	// JWKS location — is read from it at runtime rather than hard-coded, so a
	// provider rotating an endpoint does not need a release here.
	DiscoveryURL string
	Scopes       []string
	// Icon names one of the sign-in icons the panel ships. It is a name rather
	// than a URL so enabling a provider can never make the sign-in page fetch
	// from a third-party host.
	Icon string
	// RequireVerifiedEmail is the default for this provider, not a fixed rule —
	// the operator can change it. It reflects whether the provider actually
	// asserts `email_verified`; defaulting it on for a provider that never sends
	// the claim would configure a provider nobody can sign in through.
	RequireVerifiedEmail bool
	// Note records what an operator has to know that the form does not show.
	Note string
}

// ProviderPresets are the configurations shipped with the panel.
//
// They are ordered as the sign-in screen offers them by default; an operator's
// own `sort_order` overrides that once a provider is saved.
func ProviderPresets() []ProviderPreset {
	return []ProviderPreset{
		{
			Slug:         "google",
			DisplayName:  "Google",
			Issuer:       "https://accounts.google.com",
			DiscoveryURL: "https://accounts.google.com/.well-known/openid-configuration",
			Scopes:       []string{"openid", "email", "profile"},
			Icon:         "google",

			// Google asserts `email_verified` and is the reference case for
			// requiring it.
			RequireVerifiedEmail: true,
			Note: "Add the installation's callback URL to the OAuth client's " +
				"authorised redirect URIs before enabling.",
		},
		{
			Slug:         "yandex",
			DisplayName:  "Yandex",
			Issuer:       "https://oauth.yandex.ru",
			DiscoveryURL: "https://oauth.yandex.ru/.well-known/openid-configuration",

			// Yandex names its scopes differently from the OIDC defaults, which
			// is exactly the kind of per-provider detail a preset exists to
			// carry — the flow itself stays generic.
			Scopes: []string{"openid", "login:email", "login:info"},
			Icon:   "yandex",

			// Yandex returns the address but does not reliably assert that it is
			// verified, so requiring the claim would refuse every sign-in. The
			// default is off and the operator is told why rather than being left
			// to discover it from a failed login.
			RequireVerifiedEmail: false,
			Note: "Yandex does not consistently return `email_verified`. Leave the " +
				"verified-email requirement off unless your application is " +
				"configured to supply it.",
		},
		{
			Slug:         "discord",
			DisplayName:  "Discord",
			Issuer:       "https://discord.com",
			DiscoveryURL: "https://discord.com/.well-known/openid-configuration",
			Scopes:       []string{"openid", "email", "identify"},
			Icon:         "discord",

			RequireVerifiedEmail: true,
			Note: "Discord issues an ID token only when the `openid` scope is " +
				"requested; the `email` scope is what carries the address.",
		},
	}
}
