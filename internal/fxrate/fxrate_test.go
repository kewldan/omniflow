package fxrate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStarsForRoundsUp(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct{ amount, rate, want int64 }{
		{amount: 18000, rate: 180, want: 100},
		{amount: 18001, rate: 180, want: 101},
		{amount: 1, rate: 180, want: 1},
		{amount: 0, rate: 180, want: 0},
		{amount: 100, rate: 0, want: 0},
	} {
		if got := StarsFor(testCase.amount, testCase.rate); got != testCase.want {
			t.Errorf("StarsFor(%d, %d) = %d, want %d", testCase.amount, testCase.rate, got, testCase.want)
		}
	}
}

func TestSourcesAreParsed(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/cbr":
			_, _ = writer.Write([]byte(`{"Valute":{"USD":{"Nominal":1,"Value":92.4511}}}`))
		case "/open":
			_, _ = writer.Write([]byte(`{"result":"success","rates":{"RUB":93.1}}`))
		default:
			writer.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(server.Close)
	service := New(nil, Options{CBRURL: server.URL + "/cbr", OpenURL: server.URL + "/open"})
	if value, err := service.readCBR(context.Background()); err != nil || value != 92.4511 {
		t.Fatalf("cbr = %v (%v)", value, err)
	}
	if value, err := service.readOpenER(context.Background()); err != nil || value != 93.1 {
		t.Fatalf("open = %v (%v)", value, err)
	}
	broken := New(nil, Options{CBRURL: server.URL + "/down"})
	if _, err := broken.readCBR(context.Background()); err == nil {
		t.Fatal("a failing source was read")
	}
}
