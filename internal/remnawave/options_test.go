package remnawave

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuthModesAddWhatTheProxyExpects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		options Options
		apiKey  string
		cookies map[string]string
	}{
		{"bearer", Options{}, "", nil},
		{"api key", Options{AuthType: AuthAPIKey}, "secret", nil},
		{"basic", Options{AuthType: AuthBasic, Username: "u", Password: "p"}, "Basic " + base64.StdEncoding.EncodeToString([]byte("u:p")), nil},
		{"caddy", Options{AuthType: AuthCaddy, CaddyToken: "caddy"}, "caddy", nil},
		{"secret key and cookies", Options{AuthType: AuthCookies, SecretKey: "gate:open", Cookies: "a=1; b=2"}, "", map[string]string{"gate": "open", "a": "1", "b": "2"}},
		{"bare secret key", Options{SecretKey: "token"}, "", map[string]string{"token": "token"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var seen *http.Request
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				seen = request
				_, _ = writer.Write([]byte(`{"response":{"users":[],"total":0}}`))
			}))
			t.Cleanup(server.Close)
			client, err := NewClient(server.URL, "secret", testCase.options)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = client.ListUsers(context.Background(), 0, 1); err != nil {
				t.Fatal(err)
			}
			if got := seen.Header.Get("Authorization"); got != "Bearer secret" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := seen.Header.Get("X-Api-Key"); got != testCase.apiKey {
				t.Fatalf("X-Api-Key = %q, want %q", got, testCase.apiKey)
			}
			for name, value := range testCase.cookies {
				cookie, err := seen.Cookie(name)
				if err != nil || cookie.Value != value {
					t.Fatalf("cookie %s = %v (%v), want %q", name, cookie, err, value)
				}
			}
		})
	}
}

func TestIncompleteAuthModesFailAtStartup(t *testing.T) {
	t.Parallel()
	for _, options := range []Options{
		{AuthType: "magic"},
		{AuthType: AuthBasic, Username: "u"},
		{AuthType: AuthCaddy},
		{Cookies: "=x"},
	} {
		if _, err := NewClient("https://panel.example", "t", options); err == nil {
			t.Fatalf("options %+v were accepted", options)
		}
	}
}

func TestOptionsFromEnv(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"APP_REMNAWAVE_AUTH_TYPE": " Caddy ", "APP_REMNAWAVE_CADDY_TOKEN": "c",
		"APP_REMNAWAVE_TIMEOUT_SECONDS": "30", "APP_REMNAWAVE_REQUESTS_PER_MINUTE": "120",
	}
	options := OptionsFromEnv(func(key string) string { return env[key] })
	if options.AuthType != AuthCaddy || options.CaddyToken != "c" ||
		options.Timeout != 30*time.Second || options.RequestsPerMinute != 120 {
		t.Fatalf("options = %+v", options)
	}
}

func TestTheRateLimitSpacesCalls(t *testing.T) {
	t.Parallel()
	gate := newLimiter(600) // one call every 100ms
	started := time.Now()
	for range 3 {
		if err := gate.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(started); elapsed < 180*time.Millisecond {
		t.Fatalf("three calls took %v, want at least two intervals", elapsed)
	}
	if newLimiter(0) != nil {
		t.Fatal("a zero rate must mean no limit")
	}
}
