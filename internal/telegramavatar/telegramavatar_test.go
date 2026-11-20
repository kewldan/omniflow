package telegramavatar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omniflow/omniflow/internal/config"
)

const testToken = "4242:avatar-test"

// fakeBotAPI answers the three calls a photo needs. User 1 has a photo, user 2
// has none.
func fakeBotAPI(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		path := request.URL.Path
		switch {
		case strings.HasSuffix(path, "/getUserProfilePhotos"):
			_ = request.ParseMultipartForm(1 << 20)
			writer.Header().Set("Content-Type", "application/json")
			if request.FormValue("user_id") == "2" {
				_, _ = writer.Write([]byte(`{"ok":true,"result":{"total_count":0,"photos":[]}}`))
				return
			}
			_, _ = writer.Write([]byte(`{"ok":true,"result":{"total_count":1,"photos":[[` +
				`{"file_id":"small","file_unique_id":"s","width":160,"height":160},` +
				`{"file_id":"medium","file_unique_id":"m","width":320,"height":320},` +
				`{"file_id":"large","file_unique_id":"l","width":640,"height":640}]]}}`))
		case strings.HasSuffix(path, "/getFile"):
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"ok":true,"result":{"file_id":"medium","file_unique_id":"m","file_path":"photos/m.jpg"}}`))
		case strings.HasSuffix(path, "/photos/m.jpg"):
			writer.Header().Set("Content-Type", "image/jpeg")
			_, _ = writer.Write([]byte("\xff\xd8jpeg-bytes"))
		default:
			http.NotFound(writer, request)
		}
	}))
}

func TestAvatarIsFetchedOnceAndCached(t *testing.T) {
	var calls atomic.Int32
	server := fakeBotAPI(t, &calls)
	defer server.Close()

	source, err := New(testToken, config.TelegramTransportConfig{APIURL: server.URL})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	data, err := source.Avatar(context.Background(), 1)
	if err != nil {
		t.Fatalf("avatar: %v", err)
	}
	if string(data) != "\xff\xd8jpeg-bytes" {
		t.Fatalf("avatar bytes = %q", data)
	}
	first := calls.Load()
	if _, err = source.Avatar(context.Background(), 1); err != nil {
		t.Fatalf("cached avatar: %v", err)
	}
	if calls.Load() != first {
		t.Fatalf("the second read went to Telegram again (%d calls, want %d)", calls.Load(), first)
	}
}

func TestNoPhotoIsReportedAndRemembered(t *testing.T) {
	var calls atomic.Int32
	server := fakeBotAPI(t, &calls)
	defer server.Close()

	source, err := New(testToken, config.TelegramTransportConfig{APIURL: server.URL})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err = source.Avatar(context.Background(), 2); !errors.Is(err, ErrNoAvatar) {
		t.Fatalf("got %v, want ErrNoAvatar", err)
	}
	first := calls.Load()
	if _, err = source.Avatar(context.Background(), 2); !errors.Is(err, ErrNoAvatar) {
		t.Fatalf("cached miss: got %v, want ErrNoAvatar", err)
	}
	if calls.Load() != first {
		t.Fatal("a known miss went to Telegram again")
	}
}

func TestNoTokenMeansNoSource(t *testing.T) {
	source, err := New("", config.TelegramTransportConfig{})
	if err != nil || source != nil {
		t.Fatalf("New(\"\") = %v, %v; want nil, nil", source, err)
	}
}
