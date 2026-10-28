package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// Operator OIDC providers decide who becomes which operator, so changing one is
// owner-only even though reading it is an ordinary setting. An administrator
// who could repoint a provider could sign in as anyone linked through it.
func TestOperatorOIDCWritesAreOwnerOnly(t *testing.T) {
	handlers := &AdminHandlers{}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			principal := adminauthpg.Principal{Grant: rbac.NewGrant(rbac.RoleAdministrator)}
			next.ServeHTTP(writer, withPrincipal(request, principal))
		})
	})
	handlers.mountOIDCSecure(router)
	for _, testCase := range []struct{ method, path string }{
		{http.MethodPut, "/settings/oidc"},
		{http.MethodDelete, "/settings/oidc/corp"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(testCase.method, testCase.path, nil))
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("administrator %s %s = %d, want 403", testCase.method, testCase.path, recorder.Code)
		}
	}
}
