package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The unauthenticated sign-in POSTs have no session CSRF token to check, so
// what stops a cross-site page signing a visitor into the attacker's account is
// the content type and what the browser says about the request's origin.
func TestRequireSameOriginJSONRefusesCrossSiteSignIn(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		headers     map[string]string
		want        int
	}{
		{"same-origin fetch", "application/json", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		{"json with charset", "application/json; charset=utf-8", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		{"non-browser client", "application/json", nil, http.StatusOK},
		{"old browser, matching origin", "application/json", map[string]string{"Origin": "https://vpn.example.com"}, http.StatusOK},
		{"cross-site fetch", "application/json", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"sibling subdomain", "application/json", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"old browser, foreign origin", "application/json", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"opaque origin", "application/json", map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"html form", "application/x-www-form-urlencoded", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusUnsupportedMediaType},
		{"text/plain no-cors post", "text/plain", nil, http.StatusUnsupportedMediaType},
		{"no content type", "", nil, http.StatusUnsupportedMediaType},
	}
	handler := requireSameOriginJSON(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://vpn.example.com/v1/account/auth/telegram",
				strings.NewReader("{}"))
			if testCase.contentType != "" {
				request.Header.Set("Content-Type", testCase.contentType)
			}
			for key, value := range testCase.headers {
				request.Header.Set(key, value)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
		})
	}
}
