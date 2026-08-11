package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

func TestSecurityHeadersAreRestrictive(t *testing.T) {
	recorder := httptest.NewRecorder()
	SecurityHeaders(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/panel/auth/session", nil))

	expected := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store",
	}
	for header, want := range expected {
		if got := recorder.Header().Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
	// Embedding the panel must be impossible, so a clickjacked click cannot be
	// laundered into an authenticated action.
	if policy := recorder.Header().Get("Content-Security-Policy"); policy == "" {
		t.Fatal("no Content-Security-Policy was set")
	} else if want := "frame-ancestors 'none'"; !strings.Contains(policy, want) {
		t.Fatalf("CSP %q does not contain %q", policy, want)
	}
}

// An unauthenticated deployment must not believe a forwarded address, because
// anyone can send the header and it feeds rate limiting and the audit trail.
func TestClientIPIgnoresForwardedHeaderFromUntrustedPeers(t *testing.T) {
	trusted, err := NewTrustedProxies(nil)
	if err != nil {
		t.Fatalf("NewTrustedProxies: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "203.0.113.9:5000"
	request.Header.Set("X-Forwarded-For", "1.2.3.4")

	address := trusted.ClientIP(request)
	if address == nil || address.String() != "203.0.113.9" {
		t.Fatalf("ClientIP = %v, want the socket address 203.0.113.9", address)
	}
}

func TestClientIPHonoursForwardedHeaderFromATrustedProxy(t *testing.T) {
	trusted, err := NewTrustedProxies([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("NewTrustedProxies: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.1.2.3:5000"
	request.Header.Set("X-Forwarded-For", "198.51.100.7")

	address := trusted.ClientIP(request)
	if address == nil || address.String() != "198.51.100.7" {
		t.Fatalf("ClientIP = %v, want 198.51.100.7", address)
	}
}

// With a chain of proxies, the closest hop that is not itself trusted is the
// furthest right we still have grounds to believe. Anything left of it was
// supplied by a client we do not control.
func TestClientIPWalksBackToTheLastUntrustedHop(t *testing.T) {
	trusted, err := NewTrustedProxies([]string{"10.0.0.0/8", "192.168.0.0/16"})
	if err != nil {
		t.Fatalf("NewTrustedProxies: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.1.2.3:5000"
	request.Header.Set("X-Forwarded-For", "1.1.1.1, 198.51.100.7, 192.168.5.5")

	address := trusted.ClientIP(request)
	if address == nil || address.String() != "198.51.100.7" {
		t.Fatalf("ClientIP = %v, want 198.51.100.7", address)
	}
}

func TestClientIPRejectsMalformedForwardedEntries(t *testing.T) {
	trusted, err := NewTrustedProxies([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("NewTrustedProxies: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.1.2.3:5000"
	request.Header.Set("X-Forwarded-For", "not-an-ip, still-not-an-ip")

	address := trusted.ClientIP(request)
	if address == nil || address.String() != "10.1.2.3" {
		t.Fatalf("ClientIP = %v, want the peer 10.1.2.3 when no hop parses", address)
	}
}

func TestNewTrustedProxiesAcceptsBareAddressesAndRejectsGarbage(t *testing.T) {
	trusted, err := NewTrustedProxies([]string{"10.1.2.3", " ", "2001:db8::/32"})
	if err != nil {
		t.Fatalf("NewTrustedProxies: %v", err)
	}
	if !trusted.contains(netip.MustParseAddr("10.1.2.3")) {
		t.Fatal("a bare address was not treated as a single-host network")
	}
	if trusted.contains(netip.MustParseAddr("10.1.2.4")) {
		t.Fatal("a bare address matched a neighbouring host")
	}
	if _, err = NewTrustedProxies([]string{"nonsense"}); err == nil {
		t.Fatal("an unparsable entry was accepted")
	}
}

// ---------------------------------------------------------------------------
// CSRF
// ---------------------------------------------------------------------------

func withPrincipal(request *http.Request, principal adminauthpg.Principal) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal))
}

func TestRequireCSRFAllowsSafeMethods(t *testing.T) {
	handlers := &AdminHandlers{}
	recorder := httptest.NewRecorder()
	// No principal at all: a safe method must still pass, because it changes
	// nothing and the session middleware has its own gate.
	handlers.requireCSRF(okHandler()).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET was blocked with %d", recorder.Code)
	}
}

func TestRequireCSRFRejectsUnsafeMethodsWithoutAMatchingToken(t *testing.T) {
	handlers := &AdminHandlers{}
	principal := adminauthpg.Principal{CSRFToken: "expected-token"}

	cases := []struct {
		name      string
		submitted string
		want      int
	}{
		{name: "missing token", submitted: "", want: http.StatusForbidden},
		{name: "wrong token", submitted: "forged-token", want: http.StatusForbidden},
		{name: "matching token", submitted: "expected-token", want: http.StatusOK},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := withPrincipal(httptest.NewRequest(http.MethodPost, "/", nil), principal)
			if testCase.submitted != "" {
				request.Header.Set("X-CSRF-Token", testCase.submitted)
			}
			recorder := httptest.NewRecorder()
			handlers.requireCSRF(okHandler()).ServeHTTP(recorder, request)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
		})
	}
}

func TestRequireCSRFRejectsAnUnauthenticatedUnsafeRequest(t *testing.T) {
	handlers := &AdminHandlers{}
	recorder := httptest.NewRecorder()
	handlers.requireCSRF(okHandler()).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

// ---------------------------------------------------------------------------
// Permissions
// ---------------------------------------------------------------------------

func TestRequirePermissionAllowsAndDenies(t *testing.T) {
	// The denial path records an audit event. With no trail attached it reports
	// ErrAuditUnavailable rather than panicking, and the denial itself still
	// stands — which is the property that matters: authorization must never
	// fail open because logging did.
	handlers := &AdminHandlers{}

	cases := []struct {
		name     string
		roles    []rbac.Role
		required []rbac.Permission
		want     int
	}{
		{
			name:  "owner reaches account management",
			roles: []rbac.Role{rbac.RoleOwner}, required: []rbac.Permission{rbac.PermissionAdminsWrite},
			want: http.StatusOK,
		},
		{
			name:  "support cannot reach account management",
			roles: []rbac.Role{rbac.RoleSupport}, required: []rbac.Permission{rbac.PermissionAdminsWrite},
			want: http.StatusForbidden,
		},
		{
			name:  "auditor cannot write finance",
			roles: []rbac.Role{rbac.RoleAuditor}, required: []rbac.Permission{rbac.PermissionFinanceWrite},
			want: http.StatusForbidden,
		},
		{
			name:  "auditor may read finance",
			roles: []rbac.Role{rbac.RoleAuditor}, required: []rbac.Permission{rbac.PermissionFinanceRead},
			want: http.StatusOK,
		},
		{
			name:     "holding one of two required permissions is not enough",
			roles:    []rbac.Role{rbac.RoleSupport},
			required: []rbac.Permission{rbac.PermissionCustomersRead, rbac.PermissionFinanceWrite},
			want:     http.StatusForbidden,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			principal := adminauthpg.Principal{Grant: rbac.NewGrant(testCase.roles...)}
			request := withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), principal)
			recorder := httptest.NewRecorder()
			handlers.requirePermission(testCase.required...)(okHandler()).ServeHTTP(recorder, request)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
		})
	}
}

func TestRequirePermissionRejectsAnUnauthenticatedRequest(t *testing.T) {
	handlers := &AdminHandlers{}
	recorder := httptest.NewRecorder()
	handlers.requirePermission(rbac.PermissionAuditRead)(okHandler()).
		ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
}
