package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/customerauthpg"
)

func withCustomer(request *http.Request, principal customerauthpg.Principal) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), customerContextKey{}, principal))
}

// problemCode extracts the stable machine reason from a problem response. The
// panel branches on it, so it is part of the contract, not decoration.
func problemCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode problem %q: %v", recorder.Body.String(), err)
	}
	return strings.TrimPrefix(body.Type, "https://omniflow.dev/problems/")
}

func TestCustomerFromIsEmptyOutsideTheSessionGate(t *testing.T) {
	if _, ok := CustomerFrom(context.Background()); ok {
		t.Fatal("a bare context yielded a customer")
	}
	// An operator principal under the customer key's type must not be read as
	// a customer either; the keys are distinct types.
	ctx := context.WithValue(context.Background(), principalContextKey{}, "operator")
	if _, ok := CustomerFrom(ctx); ok {
		t.Fatal("an operator context yielded a customer")
	}
	principal := customerauthpg.Principal{SessionID: "session"}
	got, ok := CustomerFrom(withCustomer(httptest.NewRequest(http.MethodGet, "/", nil), principal).Context())
	if !ok || got.SessionID != "session" {
		t.Fatalf("CustomerFrom = %+v, %v", got, ok)
	}
}

func TestRequireCustomerCSRFLetsSafeMethodsThroughWithoutASession(t *testing.T) {
	handlers := &AccountHandlers{}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		recorder := httptest.NewRecorder()
		handlers.requireCustomerCSRF(okHandler()).ServeHTTP(recorder, httptest.NewRequest(method, "/", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s was blocked with %d", method, recorder.Code)
		}
	}
}

func TestRequireCustomerCSRFRefusesEveryUnsafeMethodWithoutTheSessionToken(t *testing.T) {
	handlers := &AccountHandlers{}
	principal := customerauthpg.Principal{SessionID: "session", CSRFToken: "expected-token"}
	// Anything that is not GET, HEAD, or OPTIONS is unsafe, including methods a
	// router would never route: the gate must not depend on an allowlist of
	// unsafe verbs that a new route could fall outside.
	methods := []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
		"PROPFIND", "post", "get",
	}
	cases := []struct {
		name      string
		submitted string
		want      int
		code      string
	}{
		{"missing", "", http.StatusForbidden, "csrf_failed"},
		{"forged", "forged-token", http.StatusForbidden, "csrf_failed"},
		{"prefix of the real token", "expected", http.StatusForbidden, "csrf_failed"},
		{"real token with whitespace", " expected-token", http.StatusForbidden, "csrf_failed"},
		{"case-folded", "EXPECTED-TOKEN", http.StatusForbidden, "csrf_failed"},
		{"matching", "expected-token", http.StatusOK, ""},
	}
	for _, method := range methods {
		for _, testCase := range cases {
			t.Run(method+"/"+testCase.name, func(t *testing.T) {
				request := withCustomer(httptest.NewRequest(method, "/v1/account/profile", nil), principal)
				if testCase.submitted != "" {
					request.Header.Set("X-CSRF-Token", testCase.submitted)
				}
				recorder := httptest.NewRecorder()
				handlers.requireCustomerCSRF(okHandler()).ServeHTTP(recorder, request)
				if recorder.Code != testCase.want {
					t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
				}
				if testCase.code != "" {
					if code := problemCode(t, recorder); code != testCase.code {
						t.Fatalf("problem = %q, want %q", code, testCase.code)
					}
				}
			})
		}
	}
}

// A session whose CSRF token is somehow empty must not be satisfied by an
// absent header: "" == "" would otherwise open every unsafe route.
func TestRequireCustomerCSRFRefusesAnEmptyTokenOnBothSides(t *testing.T) {
	handlers := &AccountHandlers{}
	request := withCustomer(httptest.NewRequest(http.MethodPost, "/", nil), customerauthpg.Principal{})
	recorder := httptest.NewRecorder()
	handlers.requireCustomerCSRF(okHandler()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

// The operator panel's token is not the customer panel's, and the two
// middlewares read different principals, so an operator session cannot pass
// the customer gate.
func TestRequireCustomerCSRFDoesNotAcceptAnOperatorSession(t *testing.T) {
	handlers := &AccountHandlers{}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, "operator"))
	request.Header.Set("X-CSRF-Token", "anything")
	recorder := httptest.NewRecorder()
	handlers.requireCustomerCSRF(okHandler()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestRequireCustomerCSRFRefusesAnUnauthenticatedUnsafeRequest(t *testing.T) {
	handlers := &AccountHandlers{}
	request := httptest.NewRequest(http.MethodDelete, "/", nil)
	request.Header.Set("X-CSRF-Token", "expected-token")
	recorder := httptest.NewRecorder()
	handlers.requireCustomerCSRF(okHandler()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || problemCode(t, recorder) != "unauthenticated" {
		t.Fatalf("status = %d %q, want 401 unauthenticated", recorder.Code, recorder.Body.String())
	}
}

func TestRequireRecentAuthentication(t *testing.T) {
	handlers := &AccountHandlers{}
	cases := []struct {
		name      string
		principal *customerauthpg.Principal
		want      int
		code      string
	}{
		{"no session", nil, http.StatusUnauthorized, "unauthenticated"},
		{"stale session", &customerauthpg.Principal{ReauthenticationRequired: true}, http.StatusUnauthorized, "reauthentication_required"},
		{"fresh session", &customerauthpg.Principal{}, http.StatusOK, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/account/delete", nil)
			if testCase.principal != nil {
				request = withCustomer(request, *testCase.principal)
			}
			reached := false
			next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				reached = true
				writer.WriteHeader(http.StatusOK)
			})
			recorder := httptest.NewRecorder()
			handlers.requireRecentAuthentication(next).ServeHTTP(recorder, request)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
			if reached != (testCase.want == http.StatusOK) {
				t.Fatalf("handler reached = %v with status %d", reached, recorder.Code)
			}
			if testCase.code != "" && problemCode(t, recorder) != testCase.code {
				t.Fatalf("problem = %q, want %q", problemCode(t, recorder), testCase.code)
			}
		})
	}
}

// The session gate answers an anonymous caller 401 before it looks at whether
// the panel is configured at all, and never reaches the handler.
func TestRequireCustomerSessionRefusesAMissingOrEmptyCookie(t *testing.T) {
	handlers := NewAccountHandlers(AccountOptions{Logger: slog.New(slog.DiscardHandler), CookieSecure: true})
	cases := []struct {
		name   string
		cookie *http.Cookie
		want   int
		code   string
	}{
		{"no cookie", nil, http.StatusUnauthorized, "unauthenticated"},
		{"empty cookie", &http.Cookie{Name: handlers.cookieName, Value: ""}, http.StatusUnauthorized, "unauthenticated"},
		// The insecure development name is not read by a secure deployment.
		{"insecure cookie name", &http.Cookie{
			Name: cookieName(accountSessionCookieBase, false), Value: "token",
		}, http.StatusUnauthorized, "unauthenticated"},
		// A credential is presented but nothing can resolve it.
		{"cookie without a service", &http.Cookie{Name: handlers.cookieName, Value: "token"}, http.StatusServiceUnavailable, "account_unavailable"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/account/profile", nil)
			if testCase.cookie != nil {
				request.AddCookie(testCase.cookie)
			}
			reached := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
			recorder := httptest.NewRecorder()
			handlers.requireCustomerSession(next).ServeHTTP(recorder, request)
			if reached {
				t.Fatal("the handler ran without a resolved session")
			}
			if recorder.Code != testCase.want || problemCode(t, recorder) != testCase.code {
				t.Fatalf("got %d %q, want %d %q", recorder.Code, problemCode(t, recorder), testCase.want, testCase.code)
			}
			if recorder.Header().Get("X-CSRF-Token") != "" {
				t.Fatal("a refused request was handed a CSRF token")
			}
		})
	}
}

func TestSameOriginRequestEdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		headers map[string]string
		want    bool
	}{
		{"typed into the address bar", "vpn.example.com", map[string]string{"Sec-Fetch-Site": "none"}, true},
		// Sec-Fetch-Site is the browser's own statement and wins over an Origin
		// that happens to match.
		{"cross-site with a matching origin", "vpn.example.com", map[string]string{
			"Sec-Fetch-Site": "cross-site", "Origin": "https://vpn.example.com",
		}, false},
		{"unknown fetch-site value", "vpn.example.com", map[string]string{"Sec-Fetch-Site": "same-origin, cross-site"}, false},
		{"origin host compared without case", "VPN.example.com", map[string]string{"Origin": "https://vpn.EXAMPLE.com"}, true},
		{"origin on another port", "vpn.example.com", map[string]string{"Origin": "https://vpn.example.com:8443"}, false},
		{"origin on a parent domain", "vpn.example.com", map[string]string{"Origin": "https://example.com"}, false},
		{"origin with a suffix trick", "vpn.example.com", map[string]string{"Origin": "https://vpn.example.com.evil.test"}, false},
		{"origin with userinfo", "vpn.example.com", map[string]string{"Origin": "https://vpn.example.com@evil.test"}, false},
		{"origin without a scheme", "vpn.example.com", map[string]string{"Origin": "vpn.example.com"}, false},
		{"malformed origin", "vpn.example.com", map[string]string{"Origin": "https://%zz"}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://vpn.example.com/v1/account/auth/link", nil)
			request.Host = testCase.host
			for key, value := range testCase.headers {
				request.Header.Set(key, value)
			}
			if got := sameOriginRequest(request); got != testCase.want {
				t.Fatalf("sameOriginRequest = %v, want %v", got, testCase.want)
			}
		})
	}
}
