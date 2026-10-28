package egress

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"
)

func TestIsPublicRefusesEveryInternalRange(t *testing.T) {
	internal := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"0.0.0.0", "100.64.0.1", "::1", "::", "fe80::1", "fc00::1", "fd12:3456::1",
		// IPv4-mapped spellings of internal IPv4 addresses.
		"::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:10.0.0.1",
		// Translation prefixes that embed an IPv4 address.
		"64:ff9b::a9fe:a9fe", "2002:7f00:1::1",
		"224.0.0.1", "255.255.255.255", "198.18.0.1",
	}
	for _, raw := range internal {
		if IsPublic(netip.MustParseAddr(raw)) {
			t.Errorf("IsPublic(%s) = true, want false", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111", "::ffff:1.1.1.1"} {
		if !IsPublic(netip.MustParseAddr(raw)) {
			t.Errorf("IsPublic(%s) = false, want true", raw)
		}
	}
}

func TestCheckURLAppliesSchemeAndLiteralAddressRules(t *testing.T) {
	strict := Policy{}
	cases := []struct {
		raw  string
		want error
	}{
		{"https://api.example.com/v1", nil},
		{"http://api.example.com/v1", ErrInsecureScheme},
		{"https://127.0.0.1/", ErrBlockedDestination},
		{"https://[::ffff:169.254.169.254]/", ErrBlockedDestination},
		{"https://localhost:8443/", ErrBlockedDestination},
		{"https://user:pass@api.example.com/", ErrInvalidURL},
		{"ftp://api.example.com/", ErrInvalidURL},
		{"https:///no-host", ErrInvalidURL},
	}
	for _, testCase := range cases {
		if err := strict.CheckURL(testCase.raw); !errors.Is(err, testCase.want) {
			t.Errorf("CheckURL(%q) = %v, want %v", testCase.raw, err, testCase.want)
		}
	}

	local := Policy{AllowHTTP: true, AllowPrivate: true}
	if err := local.CheckURL("http://ollama:11434/v1"); err != nil {
		t.Fatalf("an explicit private-network policy refused a local model: %v", err)
	}
}

// The dialer is the enforcement: a URL that passes CheckURL because it names a
// hostname must still be refused when the name resolves somewhere internal.
func TestClientRefusesToConnectToALoopbackServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := Policy{AllowHTTP: true}.Client(5 * time.Second).Get(server.URL)
	if !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("GET %s = %v, want ErrBlockedDestination", server.URL, err)
	}

	response, err := Policy{AllowHTTP: true, AllowPrivate: true}.Client(5 * time.Second).Get(server.URL)
	if err != nil {
		t.Fatalf("the private-network policy could not reach a local server: %v", err)
	}
	_ = response.Body.Close()
}

func TestCheckRedirectRefusesADowngradeAndBoundsTheChain(t *testing.T) {
	policy := Policy{}
	next := &http.Request{URL: &url.URL{Scheme: "http", Host: "api.example.com", Path: "/"}}
	if err := policy.CheckRedirect(next, []*http.Request{{}}); !errors.Is(err, ErrInsecureScheme) {
		t.Fatalf("a redirect to plain HTTP = %v, want ErrInsecureScheme", err)
	}
	internal := &http.Request{URL: &url.URL{Scheme: "https", Host: "169.254.169.254", Path: "/"}}
	if err := policy.CheckRedirect(internal, []*http.Request{{}}); !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("a redirect to the metadata address = %v, want ErrBlockedDestination", err)
	}
	fine := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.com", Path: "/"}}
	if err := policy.CheckRedirect(fine, make([]*http.Request, maxRedirects)); !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("an over-long chain = %v, want ErrTooManyRedirects", err)
	}
}

func TestDialControlChecksTheResolvedAddress(t *testing.T) {
	policy := Policy{}
	for _, address := range []string{"127.0.0.1:443", "[::ffff:10.0.0.1]:443", "169.254.169.254:80"} {
		if err := policy.control("tcp", address, nil); !errors.Is(err, ErrBlockedDestination) {
			t.Errorf("control(%s) = %v, want ErrBlockedDestination", address, err)
		}
	}
	if err := policy.control("tcp", "1.1.1.1:443", nil); err != nil {
		t.Fatalf("control(1.1.1.1:443) = %v, want nil", err)
	}
}
