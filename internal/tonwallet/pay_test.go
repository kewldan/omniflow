package tonwallet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI is a scripted TON API: a queue of send answers and a sequence of
// account states.
type fakeAPI struct {
	mu       sync.Mutex
	sends    []error
	sent     []string
	accounts []Account
	reads    int
}

func (api *fakeAPI) Account(context.Context, string) (Account, error) {
	api.mu.Lock()
	defer api.mu.Unlock()
	index := min(api.reads, len(api.accounts)-1)
	api.reads++
	return api.accounts[index], nil
}

func (api *fakeAPI) SendBOC(_ context.Context, boc string) error {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.sent = append(api.sent, boc)
	if len(api.sends) == 0 {
		return nil
	}
	answer := api.sends[0]
	api.sends = api.sends[1:]
	return answer
}

func noWait() PayOptions {
	return PayOptions{
		ConfirmAttempts: 3,
		ResendDelay:     func() time.Duration { return 0 },
		Sleep:           func(context.Context, time.Duration) error { return nil },
	}
}

func testWallet(t *testing.T) *Wallet {
	t.Helper()
	w, err := FromMnemonic(testMnemonic, VersionV5R1)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

var transfer = []Transfer{{Destination: vectorDestination, AmountNano: 1_000_000_000, PayloadBOC: vectorPayload}}

func TestPrepareRefusesInsufficientBalanceBeforeBroadcast(t *testing.T) {
	api := &fakeAPI{accounts: []Account{{BalanceNano: 1_000_000_000, Deployed: true, Seqno: 7}}}
	_, err := Prepare(context.Background(), api, testWallet(t), transfer)
	var low InsufficientBalanceError
	if !errors.As(err, &low) || !errors.Is(err, ErrBeforeBroadcast) {
		t.Fatalf("err = %v", err)
	}
	if low.RequiredNano != 1_000_000_000+GasReserveNano {
		t.Fatalf("required = %d", low.RequiredNano)
	}
	if len(api.sent) != 0 {
		t.Fatal("nothing may be sent when the balance is short")
	}
}

func TestBroadcastResendsSameBytesOnlyAfter429(t *testing.T) {
	api := &fakeAPI{
		accounts: []Account{
			{BalanceNano: 5_000_000_000, Deployed: true, Seqno: 7},
			{BalanceNano: 3_990_000_000, Deployed: true, Seqno: 8},
		},
		sends: []error{StatusError{Status: 429}, StatusError{Status: 429}},
	}
	w := testWallet(t)
	prepared, err := Prepare(context.Background(), api, w, transfer)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Broadcast(context.Background(), api, w, prepared, noWait())
	if err != nil {
		t.Fatal(err)
	}
	if len(api.sent) != 3 {
		t.Fatalf("sent %d times, want three (two 429s then success)", len(api.sent))
	}
	for _, boc := range api.sent {
		if boc != prepared.BOC {
			t.Fatal("a resend must carry the identical signed message, never a re-signed one")
		}
	}
	if result.Hash != prepared.Hash || result.BalanceAfterNano != 3_990_000_000 {
		t.Fatalf("result = %+v", result)
	}
}

func TestBroadcastNeverResendsAfterOtherFailures(t *testing.T) {
	for _, failure := range []error{
		StatusError{Status: 500}, StatusError{Status: 406}, errors.New("connection reset"),
	} {
		api := &fakeAPI{
			accounts: []Account{{BalanceNano: 5_000_000_000, Deployed: true, Seqno: 7}},
			sends:    []error{failure},
		}
		w := testWallet(t)
		prepared, err := Prepare(context.Background(), api, w, transfer)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Broadcast(context.Background(), api, w, prepared, noWait())
		var broadcast *BroadcastError
		if !errors.As(err, &broadcast) || errors.Is(err, ErrBeforeBroadcast) {
			t.Fatalf("%v: err = %v, want a broadcast (unknown-outcome) failure", failure, err)
		}
		if len(api.sent) != 1 {
			t.Fatalf("%v: sent %d times; anything but 429 may have reached the network", failure, len(api.sent))
		}
	}
}

func TestBroadcastExhausted429IsStillUnknown(t *testing.T) {
	sends := make([]error, 10)
	for index := range sends {
		sends[index] = StatusError{Status: 429}
	}
	api := &fakeAPI{accounts: []Account{{BalanceNano: 5_000_000_000, Deployed: true, Seqno: 7}}, sends: sends}
	w := testWallet(t)
	prepared, _ := Prepare(context.Background(), api, w, transfer)
	_, err := Broadcast(context.Background(), api, w, prepared, noWait())
	var broadcast *BroadcastError
	if !errors.As(err, &broadcast) {
		t.Fatalf("err = %v", err)
	}
	if len(api.sent) != 6 {
		t.Fatalf("sent %d times, want the first send plus five resends", len(api.sent))
	}
}

func TestBroadcastUnconfirmedIsUnknown(t *testing.T) {
	api := &fakeAPI{accounts: []Account{{BalanceNano: 5_000_000_000, Deployed: true, Seqno: 7}}}
	w := testWallet(t)
	prepared, _ := Prepare(context.Background(), api, w, transfer)
	_, err := Broadcast(context.Background(), api, w, prepared, noWait())
	var broadcast *BroadcastError
	if !errors.As(err, &broadcast) || !broadcast.Unconfirmed || broadcast.Hash != prepared.Hash {
		t.Fatalf("err = %v", err)
	}
}

func TestToncenterClient(t *testing.T) {
	var sentBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-Key") != "secret-key" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/getWalletInformation":
			_, _ = writer.Write([]byte(`{"ok":true,"result":{"balance":"2500000000","account_state":"active","seqno":12}}`))
		case "/sendBoc":
			_ = json.NewDecoder(request.Body).Decode(&sentBody)
			_, _ = writer.Write([]byte(`{"ok":true}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	api, err := NewAPI(APIOptions{Provider: ProviderToncenter, Key: "secret-key", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	account, err := api.Account(context.Background(), "UQ")
	if err != nil {
		t.Fatal(err)
	}
	if account != (Account{BalanceNano: 2_500_000_000, Deployed: true, Seqno: 12}) {
		t.Fatalf("account = %+v", account)
	}
	if err := api.SendBOC(context.Background(), "Ym9j"); err != nil || sentBody["boc"] != "Ym9j" {
		t.Fatalf("send: %v %v", err, sentBody)
	}

	bad, _ := NewAPI(APIOptions{Provider: ProviderToncenter, Key: "wrong", BaseURL: server.URL})
	_, err = bad.Account(context.Background(), "UQ")
	if err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("a refused key must fail without echoing it: %v", err)
	}
}

func TestTonapiClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer key" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(request.URL.Path, "/seqno"):
			_, _ = writer.Write([]byte(`{"seqno":3}`))
		case strings.HasPrefix(request.URL.Path, "/accounts/"):
			_, _ = writer.Write([]byte(`{"balance":900000000,"status":"active"}`))
		case request.URL.Path == "/blockchain/message":
			writer.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer server.Close()

	api, _ := NewAPI(APIOptions{Provider: ProviderTonapi, Key: "key", BaseURL: server.URL})
	account, err := api.Account(context.Background(), "UQ")
	if err != nil || account != (Account{BalanceNano: 900_000_000, Deployed: true, Seqno: 3}) {
		t.Fatalf("account = %+v, %v", account, err)
	}
	if err := api.SendBOC(context.Background(), "x"); !RateLimited(err) {
		t.Fatalf("a 429 must be recognisable: %v", err)
	}
}

func TestLimiterSpacesRequests(t *testing.T) {
	limit := sharedLimiter("test-limiter", 20) // 55 ms apart
	start := time.Now()
	for range 3 {
		if err := limit.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("three requests at 20 rps took %v, want at least two intervals", elapsed)
	}
	if sharedLimiter("test-limiter", 20) != limit {
		t.Fatal("clients with the same key must share one queue")
	}
}
