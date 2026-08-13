package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// An installation that has not opted in makes no request and does not report
// itself as up to date, because nobody checked.
func TestNoFeedMeansNoCheckAndNoClaim(t *testing.T) {
	status := New(Options{Current: "1.0.0"}).Status(context.Background())
	if status.Enabled {
		t.Fatal("the check reported itself enabled with no feed configured")
	}
	if status.UpdateAvailable || status.Latest != "" {
		t.Fatalf("status = %+v, want no claim about a newer release", status)
	}
	if status.Current != "1.0.0" {
		t.Fatalf("Current = %q, want the running build", status.Current)
	}
}

// A build with no version cannot be compared against anything, and saying so is
// better than comparing against the string "unknown".
func TestAnUnversionedBuildRefusesToCompare(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		t.Error("the feed was contacted for an unversioned build")
		writer.WriteHeader(http.StatusOK)
	}))
	defer feed.Close()

	status := New(Options{FeedURL: feed.URL, Current: "unknown"}).Status(context.Background())
	if !status.Unreachable || status.UpdateAvailable {
		t.Fatalf("status = %+v, want an honest refusal", status)
	}
}

func serving(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// Both documented shapes are read: a GitHub releases answer and an owner's own
// file.
func TestBothFeedShapesAreRead(t *testing.T) {
	for name, body := range map[string]string{
		"github releases": `{"tag_name":"v1.2.0","name":"1.2.0"}`,
		"an owner's file": `{"version":"1.2.0"}`,
	} {
		t.Run(name, func(t *testing.T) {
			feed := serving(t, body)
			status := New(Options{FeedURL: feed.URL, Current: "1.1.0"}).Status(context.Background())
			if !status.Enabled || status.Latest != "v1.2.0" && status.Latest != "1.2.0" {
				t.Fatalf("status = %+v, want the version the feed named", status)
			}
			if !status.UpdateAvailable {
				t.Fatalf("status = %+v, want an update reported", status)
			}
		})
	}
}

// A feed that refuses, hangs, or answers with something else leaves the
// installation knowing it does not know.
func TestAnUnusableFeedNeverReadsAsUpToDate(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer refusing.Close()

	for name, feedURL := range map[string]string{
		"a refusal":        refusing.URL,
		"nothing at all":   "http://127.0.0.1:1",
		"an unusable body": serving(t, `not json`).URL,
		"no version named": serving(t, `{"published_at":"2026-01-01T00:00:00Z"}`).URL,
	} {
		t.Run(name, func(t *testing.T) {
			status := New(Options{FeedURL: feedURL, Current: "1.0.0"}).Status(context.Background())
			if !status.Unreachable {
				t.Fatalf("status = %+v, want Unreachable", status)
			}
			if status.UpdateAvailable || status.Latest != "" {
				t.Fatalf("status = %+v, want no claim", status)
			}
			if status.Detail == "" {
				t.Fatal("no detail was recorded for an unusable feed")
			}
		})
	}
}

// The detail never carries the address, which for an owner's own feed can hold
// a token.
func TestTheDetailNeverCarriesTheAddress(t *testing.T) {
	status := New(Options{
		FeedURL: "http://127.0.0.1:1/releases?token=supersecret", Current: "1.0.0",
	}).Status(context.Background())
	if !status.Unreachable {
		t.Fatalf("status = %+v, want Unreachable", status)
	}
	if contains(status.Detail, "supersecret") || contains(status.Detail, "127.0.0.1") {
		t.Fatalf("Detail = %q, want no address in it", status.Detail)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) > 0 && stringIndex(haystack, needle) >= 0)
}

func stringIndex(haystack, needle string) int {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}

// The feed is read once per interval, however often the screen is refreshed.
func TestTheFeedIsReadOncePerInterval(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		reads++
		_, _ = writer.Write([]byte(`{"version":"2.0.0"}`))
	}))
	defer server.Close()

	moment := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	checker := New(Options{FeedURL: server.URL, Current: "1.0.0", Every: time.Hour})
	checker.now = func() time.Time { return moment }

	for range 5 {
		checker.Status(context.Background())
	}
	if reads != 1 {
		t.Fatalf("the feed was read %d times, want 1", reads)
	}

	moment = moment.Add(2 * time.Hour)
	checker.Status(context.Background())
	if reads != 2 {
		t.Fatalf("the feed was read %d times after the interval, want 2", reads)
	}
}

// The comparison is the whole point, so every case that could tell an operator
// to install something that is not newer is named.
func TestNewer(t *testing.T) {
	for _, testCase := range []struct {
		candidate, current string
		want               bool
	}{
		{"1.0.1", "1.0.0", true},
		{"1.1.0", "1.0.9", true},
		{"2.0.0", "1.9.9", true},
		{"v1.0.1", "1.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "1.0.1", false},
		{"1.0", "1.0.0", false},
		{"1.0.1", "1.0", true},
		// A prerelease is not an upgrade from the release it precedes, and the
		// release is an upgrade from the prerelease.
		{"1.1.0-rc.1", "1.1.0", false},
		{"1.1.0", "1.1.0-rc.1", true},
		// Nothing parseable, nothing claimed.
		{"latest", "1.0.0", false},
		{"1.0.0", "main", false},
		{"", "1.0.0", false},
		{"1.0.0-rc.1", "1.0.0-rc.1", false},
	} {
		if got := newer(testCase.candidate, testCase.current); got != testCase.want {
			t.Errorf("newer(%q, %q) = %v, want %v",
				testCase.candidate, testCase.current, got, testCase.want)
		}
	}
}
