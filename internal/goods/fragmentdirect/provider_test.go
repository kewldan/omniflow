package fragmentdirect

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

	"github.com/omniflow/omniflow/internal/goods"
	"github.com/omniflow/omniflow/internal/tonwallet"
)

// A throwaway phrase that has never held funds; see internal/tonwallet tests.
const testMnemonic = "sense soldier wrong wool trap more goose raw unfold rack magic increase " +
	"flower false echo girl error rare chief tray grant extra bridge lift"

const testCookies = "stel_ssid=a; stel_dt=-180; stel_token=t; stel_ton_token=tt"

// world is a scripted Fragment and TON API, recording what reached each.
type world struct {
	t *testing.T

	mu             sync.Mutex
	calls          []string
	searchAnswer   string
	initAnswer     string
	linkAnswer     string
	confirmStatus  int
	initStatus     []int
	balance        int64
	seqno          int64
	sendAnswers    []int
	sent           []string
	confirmedBOC   string
	markerAtSend   bool
	markerCommited bool

	fragment *httptest.Server
	ton      *httptest.Server
}

const paymentPayload = "te6ccgEBAQEAIwAAQgAAAABUZWxlZ3JhbSBTdGFycyBSZWYjVEVTVFJFRjEyMw=="

func newWorld(t *testing.T) *world {
	w := &world{
		t:            t,
		searchAnswer: `{"found":{"recipient":"rcpt-hash","name":"Alice"}}`,
		initAnswer:   `{"req_id":"req-1"}`,
		linkAnswer: `{"transaction":{"validUntil":1,"messages":[{"address":"EQBAjaOyi2wGWlk-EDkSabqqnF-MrrwMadnwqrurKpkla9nE",` +
			`"amount":"750000000","payload":"` + paymentPayload + `"}]}}`,
		confirmStatus: http.StatusOK,
		balance:       5_000_000_000,
		seqno:         4,
	}
	w.fragment = httptest.NewServer(http.HandlerFunc(w.serveFragment))
	w.ton = httptest.NewServer(http.HandlerFunc(w.serveTON))
	t.Cleanup(func() { w.fragment.Close(); w.ton.Close() })
	return w
}

func (w *world) serveFragment(writer http.ResponseWriter, request *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !strings.Contains(request.Header.Get("Cookie"), "stel_ton_token=tt") {
		writer.WriteHeader(http.StatusForbidden)
		return
	}
	if request.URL.Path == "/" {
		_, _ = writer.Write([]byte(`<script>var api="https://fragment.com/api?hash=abc123";</script>`))
		return
	}
	if request.URL.Query().Get("hash") != "abc123" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = request.ParseForm()
	method := request.PostForm.Get("method")
	w.calls = append(w.calls, method)
	switch method {
	case "searchStarsRecipient":
		_, _ = writer.Write([]byte(w.searchAnswer))
	case "initBuyStarsRequest":
		if len(w.initStatus) > 0 {
			status := w.initStatus[0]
			w.initStatus = w.initStatus[1:]
			writer.WriteHeader(status)
			return
		}
		_, _ = writer.Write([]byte(w.initAnswer))
	case "getBuyStarsLink":
		var account map[string]string
		if err := json.Unmarshal([]byte(request.PostForm.Get("account")), &account); err != nil ||
			account["chain"] != "-239" || account["walletStateInit"] == "" {
			w.t.Errorf("getBuyStarsLink account = %q", request.PostForm.Get("account"))
		}
		if request.PostForm.Get("id") != "req-1" {
			w.t.Errorf("getBuyStarsLink id = %q", request.PostForm.Get("id"))
		}
		_, _ = writer.Write([]byte(w.linkAnswer))
	case "confirmReq":
		w.confirmedBOC = request.PostForm.Get("boc")
		writer.WriteHeader(w.confirmStatus)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	case "updateStarsPrices":
		_, _ = writer.Write([]byte(`{"cur_price":"<div class=\"tm-value icon-before icon-ton\">0.3<span class=\"mini-frac\">712</span></div><div>~ &#036;1.50</div>"}`))
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (w *world) serveTON(writer http.ResponseWriter, request *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch request.URL.Path {
	case "/getWalletInformation":
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{
			"balance": w.balance, "account_state": "active", "seqno": w.seqno,
		}})
	case "/sendBoc":
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		w.sent = append(w.sent, body["boc"])
		w.markerAtSend = w.markerCommited
		if len(w.sendAnswers) > 0 {
			status := w.sendAnswers[0]
			w.sendAnswers = w.sendAnswers[1:]
			if status != http.StatusOK {
				writer.WriteHeader(status)
				return
			}
		}
		w.seqno++
		w.balance -= 760_000_000
		_, _ = writer.Write([]byte(`{"ok":true}`))
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (w *world) provider(t *testing.T, mutate func(*Config)) *Provider {
	t.Helper()
	config := Config{
		Cookies: testCookies, Mnemonic: testMnemonic, WalletVersion: "V5R1",
		APIProvider: "toncenter", APIKey: "key", Currency: "RUB",
		FragmentURL: w.fragment.URL, APIURL: w.ton.URL,
		Pay: tonwallet.PayOptions{
			ConfirmAttempts: 3,
			ResendDelay:     func() time.Duration { return 0 },
			Sleep:           func(context.Context, time.Duration) error { return nil },
		},
	}
	if mutate != nil {
		mutate(&config)
	}
	provider, err := New(config, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if provider.fragment != nil {
		provider.fragment.retryDelay = func(int) time.Duration { return 0 }
	}
	return provider
}

func (w *world) request() goods.DeliveryRequest {
	return goods.DeliveryRequest{
		Request: goods.Request{Kind: goods.KindTelegramStars, StarQuantity: 250, Recipient: "@alice_stars"},
		OrderID: "order-1",
		BeforeSettle: func(_ context.Context, marker goods.SettlementMarker) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			if marker.ProviderRequestID != "req-1" && !strings.HasPrefix(marker.ProviderRequestID, "dry-run") {
				w.t.Errorf("marker = %+v", marker)
			}
			w.markerCommited = true
			return nil
		},
	}
}

func TestDeliverPaysAfterMarkerAndConfirms(t *testing.T) {
	w := newWorld(t)
	delivery, err := w.provider(t, nil).Deliver(context.Background(), w.request())
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Status != "delivered" || delivery.Reference != "req-1" || delivery.Settlement == nil {
		t.Fatalf("delivery = %+v", delivery)
	}
	settlement := delivery.Settlement
	if settlement.CostNano != 750_000_000 || settlement.TxHash == "" || !settlement.ProviderConfirmed {
		t.Fatalf("settlement = %+v", settlement)
	}
	if !w.markerAtSend {
		t.Fatal("the settlement marker must be committed before the payment is sent")
	}
	if len(w.sent) != 1 || w.confirmedBOC != w.sent[0] {
		t.Fatal("confirmReq must carry the broadcast message")
	}
	want := []string{"searchStarsRecipient", "initBuyStarsRequest", "getBuyStarsLink", "confirmReq"}
	if strings.Join(w.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("fragment calls = %v", w.calls)
	}
}

func TestDeliverClassifiesFailuresByWhetherMoneyLeft(t *testing.T) {
	cases := map[string]struct {
		setup  func(*world)
		class  string
		code   string
		paid   bool
		marked bool
	}{
		"recipient not found": {
			setup: func(w *world) { w.searchAnswer = `{"error":"No Telegram users found."}` },
			class: goods.FailureRecipientInvalid, code: "recipient_not_found",
		},
		"search refused for another reason": {
			setup: func(w *world) { w.searchAnswer = `{"error":"Access denied"}` },
			class: goods.FailureRetryable, code: "fragment_unavailable",
		},
		"request rejected": {
			setup: func(w *world) { w.initAnswer = `{"error":"Too many requests"}` },
			class: goods.FailureRetryable, code: "fragment_unavailable",
		},
		"fragment server errors are retried, then reported retryable": {
			setup: func(w *world) { w.initStatus = []int{502, 502, 502} },
			class: goods.FailureRetryable, code: "fragment_unavailable",
		},
		"kyc required": {
			setup: func(w *world) { w.linkAnswer = `{"need_verify":true}` },
			class: goods.FailureProviderUnavailable, code: "fragment_configuration",
		},
		"wallet too low": {
			setup: func(w *world) { w.balance = 700_000_000 },
			class: goods.FailureProviderBalance, code: "wallet_balance_low",
		},
		"broadcast rejected with a server error": {
			setup: func(w *world) { w.sendAnswers = []int{500} },
			class: goods.FailureAmbiguous, code: "broadcast_uncertain", paid: true, marked: true,
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			test.setup(w)
			delivery, err := w.provider(t, nil).Deliver(context.Background(), w.request())
			if err != nil {
				t.Fatal(err)
			}
			if delivery.Status != "failed" || delivery.FailureClass != test.class || delivery.ErrorCode != test.code {
				t.Fatalf("delivery = %+v, want %s/%s", delivery, test.class, test.code)
			}
			if (len(w.sent) > 0) != test.paid {
				t.Fatalf("sent = %d, paid expected = %v", len(w.sent), test.paid)
			}
			if w.markerCommited != test.marked {
				t.Fatalf("marker committed = %v, want %v", w.markerCommited, test.marked)
			}
			if len(w.sent) > 1 {
				t.Fatal("a non-429 broadcast failure must never be sent again")
			}
		})
	}
}

func TestDeliverResendsOnlyOn429(t *testing.T) {
	w := newWorld(t)
	w.sendAnswers = []int{429, 429, 200}
	delivery, err := w.provider(t, nil).Deliver(context.Background(), w.request())
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Status != "delivered" {
		t.Fatalf("delivery = %+v", delivery)
	}
	if len(w.sent) != 3 || w.sent[0] != w.sent[1] || w.sent[1] != w.sent[2] {
		t.Fatal("429 resends must repeat the identical signed message")
	}
}

func TestDeliverStopsWhenMarkerCannotBeCommitted(t *testing.T) {
	w := newWorld(t)
	request := w.request()
	request.BeforeSettle = func(context.Context, goods.SettlementMarker) error {
		return errors.New("database unavailable")
	}
	delivery, err := w.provider(t, nil).Deliver(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.FailureClass != goods.FailureRetryable || len(w.sent) != 0 {
		t.Fatalf("delivery = %+v, sent = %d: without a marker nothing may be paid", delivery, len(w.sent))
	}
}

func TestDeliverWithFailedConfirmIsStillDelivered(t *testing.T) {
	w := newWorld(t)
	w.confirmStatus = http.StatusBadRequest
	delivery, err := w.provider(t, nil).Deliver(context.Background(), w.request())
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Status != "delivered" || delivery.Settlement.ProviderConfirmed {
		t.Fatalf("delivery = %+v: paid Stars are delivered whatever confirmReq says", delivery)
	}
	if len(w.sent) != 1 {
		t.Fatal("a failed confirmReq must not cause a second payment")
	}
}

func TestDeliverUnconfirmedPaymentIsAmbiguous(t *testing.T) {
	w := newWorld(t)
	provider := w.provider(t, nil)
	// The API accepts the message but the wallet never spends.
	w.ton.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/sendBoc" {
			w.mu.Lock()
			w.sent = append(w.sent, "x")
			w.mu.Unlock()
			_, _ = writer.Write([]byte(`{"ok":true}`))
			return
		}
		w.serveTON(writer, request)
	})
	delivery, err := provider.Deliver(context.Background(), w.request())
	if err != nil {
		t.Fatal(err)
	}
	if delivery.FailureClass != goods.FailureAmbiguous || delivery.Settlement == nil || delivery.Settlement.TxHash == "" {
		t.Fatalf("delivery = %+v", delivery)
	}
}

func TestDryRunTouchesNothing(t *testing.T) {
	w := newWorld(t)
	provider := w.provider(t, func(config *Config) {
		config.DryRun = true
		config.Cookies, config.Mnemonic, config.APIKey = "", "", ""
	})
	delivery, err := provider.Deliver(context.Background(), w.request())
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Status != "delivered" || !delivery.Settlement.DryRun || !IsDryRunHash(delivery.Settlement.TxHash) {
		t.Fatalf("delivery = %+v", delivery)
	}
	if !w.markerCommited || len(w.calls) != 0 || len(w.sent) != 0 {
		t.Fatal("a dry run passes the marker step and contacts nobody")
	}
}

func TestDeliverRefusesOutOfRangeQuantity(t *testing.T) {
	w := newWorld(t)
	request := w.request()
	request.StarQuantity = 49
	delivery, _ := w.provider(t, nil).Deliver(context.Background(), request)
	if delivery.FailureClass != goods.FailurePermanent || len(w.calls) != 0 {
		t.Fatalf("delivery = %+v", delivery)
	}
}

func TestStarsPriceAndRecipientValidation(t *testing.T) {
	w := newWorld(t)
	provider := w.provider(t, nil)
	nano, err := provider.StarsPriceNano(context.Background(), 100)
	if err != nil || nano != 371_200_000 {
		t.Fatalf("price = %d, %v", nano, err)
	}
	if err := provider.ValidateRecipient(context.Background(), "alice_stars"); err != nil {
		t.Fatal(err)
	}
	w.searchAnswer = `{"error":"No Telegram users found."}`
	if err := provider.ValidateRecipient(context.Background(), "nobody_here"); !errors.Is(err, goods.ErrInvalidRecipient) {
		t.Fatalf("err = %v", err)
	}
	info, err := provider.Wallet(context.Background())
	if err != nil || info.Address != "UQCAjt9m9mVLiEQj6nlxLs4CG_Kp5czy81DUiKvhS6p3v1Vn" || info.BalanceNano != 5_000_000_000 {
		t.Fatalf("wallet = %+v, %v", info, err)
	}
}

func TestNewRefusesIncompleteSecretsWithoutEchoingThem(t *testing.T) {
	_, err := New(Config{Cookies: "stel_ssid=secretvalue", Mnemonic: testMnemonic, APIKey: "k"}, nil, nil)
	if !errors.Is(err, ErrConfiguration) || strings.Contains(err.Error(), "secretvalue") {
		t.Fatalf("err = %v", err)
	}
	_, err = New(Config{Cookies: testCookies, Mnemonic: "not a phrase", APIKey: "k"}, nil, nil)
	if !errors.Is(err, ErrConfiguration) || strings.Contains(err.Error(), "phrase") {
		t.Fatalf("err = %v", err)
	}
	if _, err = New(Config{}, nil, nil); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseCookies(t *testing.T) {
	cookies, err := ParseCookies(`{"stel_ssid":"a","stel_dt":"-180","stel_token":"t","stel_ton_token":"tt"}`)
	if err != nil || cookies["stel_dt"] != "-180" {
		t.Fatalf("json cookies = %v, %v", cookies, err)
	}
	if _, err := ParseCookies("stel_ssid=a; stel_dt=b"); err == nil ||
		!strings.Contains(err.Error(), "stel_token, stel_ton_token") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseTON(t *testing.T) {
	for input, want := range map[string]int64{"0.3712": 371_200_000, "12": 12_000_000_000, "1.0000000019": 1_000_000_001} {
		if got, err := parseTON(input); err != nil || got != want {
			t.Fatalf("parseTON(%q) = %d, %v", input, got, err)
		}
	}
	if _, err := parseTON("abc"); err == nil {
		t.Fatal("expected a refusal")
	}
}
