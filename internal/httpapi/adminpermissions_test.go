package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// sensitiveHolders names, for each permission that moves money, changes who
// can sign in, or changes configuration, exactly the roles allowed through the
// route gate. It is written out by hand rather than derived from rbac so that
// widening a role is a deliberate edit in two places, not a silent one.
var sensitiveHolders = map[rbac.Permission][]rbac.Role{
	rbac.PermissionAdminsWrite:        {rbac.RoleOwner},
	rbac.PermissionAdminsRoles:        {rbac.RoleOwner},
	rbac.PermissionFinanceWrite:       {rbac.RoleOwner, rbac.RoleAdministrator, rbac.RoleFinance},
	rbac.PermissionGiftsWrite:         {rbac.RoleOwner, rbac.RoleAdministrator, rbac.RoleFinance},
	rbac.PermissionCatalogWrite:       {rbac.RoleOwner, rbac.RoleAdministrator},
	rbac.PermissionGoodsWrite:         {rbac.RoleOwner, rbac.RoleAdministrator},
	rbac.PermissionSettingsWrite:      {rbac.RoleOwner, rbac.RoleAdministrator},
	rbac.PermissionSystemWrite:        {rbac.RoleOwner, rbac.RoleAdministrator},
	rbac.PermissionRiskWrite:          {rbac.RoleOwner, rbac.RoleAdministrator},
	rbac.PermissionCustomersExport:    {rbac.RoleOwner, rbac.RoleAdministrator},
	rbac.PermissionCustomersWrite:     {rbac.RoleOwner, rbac.RoleAdministrator, rbac.RoleSupport},
	rbac.PermissionSubscriptionsWrite: {rbac.RoleOwner, rbac.RoleAdministrator, rbac.RoleSupport},
}

// TestEverySensitivePermissionGateRefusesEveryOtherRole drives the real route
// gate, not the rbac table, for each role on each sensitive permission, so a
// change to either that lets a role through is caught here.
func TestEverySensitivePermissionGateRefusesEveryOtherRole(t *testing.T) {
	handlers := &AdminHandlers{logger: slog.New(slog.DiscardHandler)}
	for permission, holders := range sensitiveHolders {
		for _, role := range rbac.AllRoles {
			want := http.StatusForbidden
			if slices.Contains(holders, role) {
				want = http.StatusOK
			}
			t.Run(string(permission)+"/"+string(role), func(t *testing.T) {
				principal := adminauthpg.Principal{Grant: rbac.NewGrant(role)}
				reached := false
				next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					reached = true
					writer.WriteHeader(http.StatusOK)
				})
				request := withPrincipal(httptest.NewRequest(http.MethodPost, "/v1/panel/x", nil), principal)
				recorder := httptest.NewRecorder()
				handlers.requirePermission(permission)(next).ServeHTTP(recorder, request)
				if recorder.Code != want {
					t.Fatalf("status = %d, want %d", recorder.Code, want)
				}
				if reached != (want == http.StatusOK) {
					t.Fatalf("handler reached = %v", reached)
				}
				if want == http.StatusForbidden && problemCode(t, recorder) != "forbidden" {
					t.Fatalf("problem = %q, want forbidden", problemCode(t, recorder))
				}
			})
		}
	}
}

// An account with no roles — a suspended grant, an unknown role read from the
// database, or a freshly invited operator — holds nothing at all.
func TestAnOperatorWithoutARoleIsRefusedEverything(t *testing.T) {
	handlers := &AdminHandlers{logger: slog.New(slog.DiscardHandler)}
	grants := map[string]rbac.Grant{
		"no roles":     rbac.NewGrant(),
		"unknown role": rbac.NewGrant(rbac.Role("superuser")),
	}
	for name, grant := range grants {
		for _, permission := range rbac.AllPermissions {
			request := withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), adminauthpg.Principal{Grant: grant})
			recorder := httptest.NewRecorder()
			handlers.requirePermission(permission)(okHandler()).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("%s reached %s with %d", name, permission, recorder.Code)
			}
		}
	}
}

// Roles combine by union, and the gate requires every listed permission: an
// operator who is both support and marketing still cannot move money.
func TestCombinedRolesNeverReachAPermissionNeitherHolds(t *testing.T) {
	handlers := &AdminHandlers{logger: slog.New(slog.DiscardHandler)}
	principal := adminauthpg.Principal{Grant: rbac.NewGrant(rbac.RoleSupport, rbac.RoleMarketing)}
	cases := []struct {
		required []rbac.Permission
		want     int
	}{
		{[]rbac.Permission{rbac.PermissionSupportWrite}, http.StatusOK},
		{[]rbac.Permission{rbac.PermissionMarketingWrite}, http.StatusOK},
		{[]rbac.Permission{rbac.PermissionSupportWrite, rbac.PermissionMarketingWrite}, http.StatusOK},
		{[]rbac.Permission{rbac.PermissionFinanceWrite}, http.StatusForbidden},
		{[]rbac.Permission{rbac.PermissionSupportWrite, rbac.PermissionFinanceWrite}, http.StatusForbidden},
		{[]rbac.Permission{rbac.PermissionAdminsRoles}, http.StatusForbidden},
	}
	for _, testCase := range cases {
		request := withPrincipal(httptest.NewRequest(http.MethodPost, "/", nil), principal)
		recorder := httptest.NewRecorder()
		handlers.requirePermission(testCase.required...)(okHandler()).ServeHTTP(recorder, request)
		if recorder.Code != testCase.want {
			t.Fatalf("%v: status = %d, want %d", testCase.required, recorder.Code, testCase.want)
		}
	}
}

// The operator session gate refuses a missing or empty cookie before it asks
// the service anything, and never hands a refused caller a CSRF token.
func TestRequireSessionRefusesAMissingOrEmptyCookie(t *testing.T) {
	handlers := &AdminHandlers{cookieName: cookieName(adminSessionCookieBase, true)}
	for name, cookie := range map[string]*http.Cookie{
		"no cookie":     nil,
		"empty cookie":  {Name: handlers.cookieName, Value: ""},
		"insecure name": {Name: cookieName(adminSessionCookieBase, false), Value: "token"},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/panel/auth/session", nil)
			if cookie != nil {
				request.AddCookie(cookie)
			}
			reached := false
			recorder := httptest.NewRecorder()
			handlers.requireSession(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })).
				ServeHTTP(recorder, request)
			if reached || recorder.Code != http.StatusUnauthorized {
				t.Fatalf("reached = %v, status = %d; want 401 without the handler", reached, recorder.Code)
			}
			if recorder.Header().Get("X-CSRF-Token") != "" {
				t.Fatal("a refused request was handed a CSRF token")
			}
		})
	}
}
