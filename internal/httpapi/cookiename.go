package httpapi

// Cookie naming for the two session cookies and the two OIDC flow cookies.
//
// The `__Host-` prefix is the strongest binding a cookie can carry: a browser
// accepts it only with `Secure`, `Path=/`, and no `Domain`, so a sibling
// subdomain cannot set or overwrite the session. That is what production wants
// and it is what these cookies are named for.
//
// The prefix is not free, though, and the cost is the whole reason this file
// exists: the same rule that rejects a `Domain` attribute also rejects the
// cookie outright when `Secure` is absent. `APP_ADMIN_COOKIE_SECURE=false` and
// `APP_CUSTOMER_COOKIE_SECURE=false` are the documented way to run the stack
// over plain HTTP locally, and with a `__Host-` name that combination does not
// merely weaken the cookie — the browser discards it on arrival, so sign-in
// cannot complete at all and the panel bounces back to the login screen with no
// error to show for it.
//
// So the prefix follows the attribute it depends on rather than being asserted
// independently of it. A secure cookie is `__Host-`-prefixed and a plain-HTTP
// one is not, which keeps the production name unchanged and makes the local
// stack usable, instead of naming every cookie for a guarantee only one
// configuration actually provides.
const (
	adminSessionCookieBase   = "omniflow_admin"
	accountSessionCookieBase = "omniflow_account"
	adminOIDCCookieBase      = "omniflow_oidc"
	accountOIDCCookieBase    = "omniflow_account_oidc"

	hostPrefix = "__Host-"
)

// cookieName prefixes base with `__Host-` when the cookie will carry Secure,
// and returns it unchanged when it will not.
func cookieName(base string, secure bool) string {
	if secure {
		return hostPrefix + base
	}
	return base
}
