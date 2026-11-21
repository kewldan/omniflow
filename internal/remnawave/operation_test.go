package remnawave

import (
	"errors"
	"net/http"
	"testing"
)

func TestOperationNameNeverCarriesIdentifiers(t *testing.T) {
	cases := map[string]string{
		"/api/users/42/actions/enable":                         "POST /api/users/:id/actions/enable",
		"/api/users/by-username/alice":                         "POST /api/users/by-username/:id",
		"/api/users/stream?size=2&telegramId=123":              "POST /api/users/stream",
		"/api/subscription-page-configs/0b6f1c2e-1111-4a3b-9c": "POST /api/subscription-page-configs/:id",
		"/api/hwid/devices/delete":                             "POST /api/hwid/devices/delete",
		"/api/users/?start=0&size=1":                           "POST /api/users",
	}
	for path, want := range cases {
		if got := OperationName(http.MethodPost, path); got != want {
			t.Errorf("OperationName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestClassifyCall(t *testing.T) {
	cases := []struct {
		err           error
		outcome, code string
	}{
		{nil, "succeeded", ""},
		{ErrNotFound, "not_found", ""},
		{&APIError{StatusCode: 502}, "failed", "remnawave_http_502"},
		{errors.New("dial tcp: refused"), "failed", "remnawave_unavailable"},
	}
	for _, tc := range cases {
		outcome, code := classifyCall(tc.err)
		if outcome != tc.outcome || code != tc.code {
			t.Errorf("classifyCall(%v) = %q, %q; want %q, %q", tc.err, outcome, code, tc.outcome, tc.code)
		}
	}
}
