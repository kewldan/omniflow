//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/customerauthpg"
)

// A magic link used to open a session on the GET a chat link lands on. Any page
// can make a browser perform a GET, so a link requested for the attacker's own
// account and planted in a victim's browser signed the victim into it. The GET
// now only checks the token and hands off to a confirmation page; the session
// is opened by a same-origin JSON POST.
func TestMagicLinkGETDoesNotSignInAndThePOSTIsSameOriginOnly(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	now := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return now })
	router := mountAccount(newAccountHandlersForTest(service))

	seed, err := service.SignInWithTelegram(ctx, signedWidget(700450, now), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	link, err := service.IssueMagicLink(ctx, seed.Customer.ID, customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	parsed, _ := url.Parse(link)
	token := parsed.Query().Get("token")

	// The GET sets no cookie and spends nothing; it forwards to the
	// confirmation page with the token in the fragment.
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, parsed.RequestURI(), nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("GET answered %d", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != "/account/sign-in/confirm#token="+url.QueryEscape(token) {
		t.Fatalf("Location = %q", location)
	}
	if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("the GET set cookies: %v", cookies)
	}

	post := func(contentType string, headers map[string]string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/account/auth/link",
			strings.NewReader(`{"token":"`+token+`"}`))
		request.Header.Set("Content-Type", contentType)
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	// A cross-site page can neither fetch it nor submit it as a form, and
	// neither attempt spends the token.
	if response := post("application/json", map[string]string{"Sec-Fetch-Site": "cross-site"}); response.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST answered %d", response.Code)
	}
	if response := post("application/x-www-form-urlencoded", nil); response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form POST answered %d", response.Code)
	}

	response := post("application/json", map[string]string{"Sec-Fetch-Site": "same-origin"})
	if response.Code != http.StatusOK {
		t.Fatalf("same-origin POST answered %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Set-Cookie"), "omniflow_account") {
		t.Fatal("the confirming POST set no session cookie")
	}
	if replay := post("application/json", map[string]string{"Sec-Fetch-Site": "same-origin"}); replay.Code != http.StatusUnauthorized {
		t.Fatalf("a spent link answered %d", replay.Code)
	}
}
