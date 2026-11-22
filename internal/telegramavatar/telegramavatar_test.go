package telegramavatar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// memoryStore is a Store kept in a map, with the fetch time settable.
type memoryStore struct {
	photos  map[int64][]byte
	fetched map[int64]time.Time
	saves   int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{photos: map[int64][]byte{}, fetched: map[int64]time.Time{}}
}

func (store *memoryStore) Load(_ context.Context, id int64) ([]byte, time.Time, bool, error) {
	at, ok := store.fetched[id]
	return store.photos[id], at, ok, nil
}

func (store *memoryStore) Save(_ context.Context, id int64, photo []byte) error {
	store.saves++
	store.photos[id], store.fetched[id] = photo, time.Now()
	return nil
}

// TestStoredPhotoIsServedWithoutTelegram: a fresh stored photo answers
// without asking Telegram, and a fetched one is stored.
func TestStoredPhotoIsServedWithoutTelegram(t *testing.T) {
	var calls atomic.Int32
	server := fakeBotAPI(t, &calls)
	defer server.Close()
	store := newMemoryStore()
	source, err := New(testToken, config.TelegramTransportConfig{APIURL: server.URL})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	source.WithStore(store)

	if _, err = source.Avatar(context.Background(), 1); err != nil || store.saves != 1 {
		t.Fatalf("first read: err %v, saves %d", err, store.saves)
	}
	// A new process: no memory cache, the stored copy is fresh.
	fresh, _ := New(testToken, config.TelegramTransportConfig{APIURL: server.URL})
	fresh.WithStore(store)
	before := calls.Load()
	data, err := fresh.Avatar(context.Background(), 1)
	if err != nil || string(data) != "\xff\xd8jpeg-bytes" || calls.Load() != before {
		t.Fatalf("stored photo: %q, %v, %d new calls", data, err, calls.Load()-before)
	}

	// "No photo" is stored too, and answered as no photo.
	if _, err = source.Avatar(context.Background(), 2); !errors.Is(err, ErrNoAvatar) {
		t.Fatalf("user without photo gave %v", err)
	}
	if _, found := store.fetched[2]; !found || store.photos[2] != nil {
		t.Fatal("the missing photo was not recorded")
	}
}

// TestStalePhotoIsServedWhenTelegramFails: a day-old copy is refreshed, and
// when Telegram does not answer the copy is served instead of an error.
func TestStalePhotoIsServedWhenTelegramFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	store := newMemoryStore()
	store.photos[1], store.fetched[1] = []byte("old-photo"), time.Now().Add(-48*time.Hour)
	source, err := New(testToken, config.TelegramTransportConfig{APIURL: server.URL})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	source.WithStore(store)
	data, err := source.Avatar(context.Background(), 1)
	if err != nil || string(data) != "old-photo" {
		t.Fatalf("stale copy: %q, %v", data, err)
	}
	if _, err = source.Avatar(context.Background(), 3); err == nil || errors.Is(err, ErrNoAvatar) {
		t.Fatalf("with nothing stored a Telegram failure must surface, got %v", err)
	}
}
