package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Issuing, listing, and revoking invitations is account administration and
// sits behind the session gate.
func TestInvitationManagementRequiresASession(t *testing.T) {
	router := newTestRouter()
	cases := []struct{ method, path string }{
		{http.MethodGet, "/v1/panel/admins/invitations"},
		{http.MethodPost, "/v1/panel/admins/invitations"},
		{http.MethodDelete, "/v1/panel/admins/invitations/00000000-0000-0000-0000-000000000001"},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(testCase.method, testCase.path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s answered %d, want 401", testCase.method, testCase.path, recorder.Code)
		}
	}
}

// Redeeming an invitation happens before the invitee has an account, so the two
// public routes must not be behind the session gate. With no service attached
// they answer 503, which proves they reached a handler.
func TestInvitationRedemptionIsReachableWithoutASession(t *testing.T) {
	router := newTestRouter()
	for _, path := range []string{"/v1/panel/invitations/preview", "/v1/panel/invitations/accept"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("POST %s answered %d, want 503 from the handler", path, recorder.Code)
		}
	}
}
