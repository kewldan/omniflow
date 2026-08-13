package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The rule this file defends, from RFC 6265bis: a browser accepts a cookie whose
// name begins with `__Host-` only when it also carries `Secure`. A cookie that
// breaks the rule is not merely weaker — it is discarded on arrival, so a
// session cookie named that way over plain HTTP never comes back, and sign-in
// fails with nothing to show the operator. `APP_ADMIN_COOKIE_SECURE=false` and
// `APP_CUSTOMER_COOKIE_SECURE=false` are the documented local plain-HTTP
// setting, so both configurations have to be exercised.

// collect returns every cookie a handler-driven write produced.
func collect(t *testing.T, write func(http.ResponseWriter)) []*http.Cookie {
	t.Helper()
	recorder := httptest.NewRecorder()
	write(recorder)
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no cookie was set")
	}
	return cookies
}

// assertPrefixMatchesSecure is the invariant itself, applied to one cookie.
func assertPrefixMatchesSecure(t *testing.T, cookie *http.Cookie) {
	t.Helper()
	prefixed := strings.HasPrefix(cookie.Name, hostPrefix)
	switch {
	case prefixed && !cookie.Secure:
		t.Fatalf(
			"cookie %q carries the __Host- prefix without Secure; a browser discards it, so sign-in cannot complete",
			cookie.Name,
		)
	case !prefixed && cookie.Secure:
		t.Fatalf(
			"cookie %q is Secure but unprefixed; it should be bound to the origin with __Host-",
			cookie.Name,
		)
	}
	// The prefix also requires Path=/ and no Domain. Asserting them here keeps
	// the three requirements together rather than spread across the call sites.
	if prefixed {
		if cookie.Path != "/" {
			t.Fatalf("cookie %q is __Host- prefixed with Path=%q, want /", cookie.Name, cookie.Path)
		}
		if cookie.Domain != "" {
			t.Fatalf("cookie %q is __Host- prefixed with Domain=%q, want none", cookie.Name, cookie.Domain)
		}
	}
}

func TestOperatorCookiesMatchTheirSecureAttribute(t *testing.T) {
	for _, secure := range []bool{true, false} {
		handlers := NewAdminHandlers(AdminOptions{
			Logger: slog.New(slog.DiscardHandler), CookieSecure: secure,
		})
		writes := map[string]func(http.ResponseWriter){
			"session": func(writer http.ResponseWriter) {
				handlers.setSessionCookie(writer, "token", time.Now().Add(time.Hour))
			},
			"session cleared": handlers.clearSessionCookie,
			"oidc flow cleared": func(writer http.ResponseWriter) {
				handlers.clearFlowCookie(writer)
			},
		}
		for name, write := range writes {
			for _, cookie := range collect(t, write) {
				t.Run(name, func(t *testing.T) { assertPrefixMatchesSecure(t, cookie) })
			}
		}
	}
}

func TestCustomerCookiesMatchTheirSecureAttribute(t *testing.T) {
	for _, secure := range []bool{true, false} {
		handlers := NewAccountHandlers(AccountOptions{
			Logger: slog.New(slog.DiscardHandler), CookieSecure: secure,
		})
		writes := map[string]func(http.ResponseWriter){
			"session": func(writer http.ResponseWriter) {
				handlers.setSessionCookie(writer, "token", time.Now().Add(time.Hour))
			},
			"session cleared": handlers.clearSessionCookie,
			"oidc flow cleared": func(writer http.ResponseWriter) {
				handlers.clearAccountFlowCookie(writer)
			},
		}
		for name, write := range writes {
			for _, cookie := range collect(t, write) {
				t.Run(name, func(t *testing.T) { assertPrefixMatchesSecure(t, cookie) })
			}
		}
	}
}

// The name a handler reads has to be the name it wrote, or a session would be
// set and then never recognised on the next request.
func TestTheCookieReadIsTheCookieWritten(t *testing.T) {
	for _, secure := range []bool{true, false} {
		admin := NewAdminHandlers(AdminOptions{
			Logger: slog.New(slog.DiscardHandler), CookieSecure: secure,
		})
		written := collect(t, func(writer http.ResponseWriter) {
			admin.setSessionCookie(writer, "token", time.Now().Add(time.Hour))
		})[0]
		if written.Name != admin.cookieName {
			t.Fatalf("operator handler writes %q but reads %q", written.Name, admin.cookieName)
		}

		account := NewAccountHandlers(AccountOptions{
			Logger: slog.New(slog.DiscardHandler), CookieSecure: secure,
		})
		written = collect(t, func(writer http.ResponseWriter) {
			account.setSessionCookie(writer, "token", time.Now().Add(time.Hour))
		})[0]
		if written.Name != account.cookieName {
			t.Fatalf("customer handler writes %q but reads %q", written.Name, account.cookieName)
		}
	}
}

// The production spelling must not drift: the panel middleware and the Next.js
// server session both look this cookie up by name.
func TestSecureCookieNamesAreTheDocumentedOnes(t *testing.T) {
	cases := map[string]string{
		cookieName(adminSessionCookieBase, true):    "__Host-omniflow_admin",
		cookieName(accountSessionCookieBase, true):  "__Host-omniflow_account",
		cookieName(adminOIDCCookieBase, true):       "__Host-omniflow_oidc",
		cookieName(accountOIDCCookieBase, true):     "__Host-omniflow_account_oidc",
		cookieName(adminSessionCookieBase, false):   "omniflow_admin",
		cookieName(accountSessionCookieBase, false): "omniflow_account",
	}
	for actual, want := range cases {
		if actual != want {
			t.Fatalf("cookie name is %q, want %q", actual, want)
		}
	}
}
