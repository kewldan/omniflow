// Package telegramavatar fetches customers' Telegram profile photos and serves
// them from the installation itself.
//
// Telegram's picture hosts (t.me and its CDN) are not reachable from every
// network, so neither panel points a browser at them. The photo is read
// through the Bot API by the server, kept in the database (telegram_avatars)
// and refreshed once a day, and kept in memory for a short while on top of
// that. When Telegram does not answer, the stored copy is served. A customer
// who removes their photo stops appearing with it at the next refresh, and
// anonymising a customer erases the stored photo with the rest of their data.
package telegramavatar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/omniflow/omniflow/internal/app"
	"github.com/omniflow/omniflow/internal/config"
)

// ErrNoAvatar reports a customer with no profile photo the bot may see.
var ErrNoAvatar = errors.New("telegram account has no visible profile photo")

const (
	// maxBytes bounds one photo. Telegram's 320-pixel size is a few tens of
	// kilobytes; anything far larger is not a profile thumbnail.
	maxBytes = 512 << 10
	cacheTTL = time.Hour
	// missTTL keeps "no photo" for less time, so a customer who adds one is
	// seen sooner than one who removes one.
	missTTL    = 10 * time.Minute
	maxEntries = 512
	// refreshAfter is how old a stored photo may be before Telegram is asked
	// again; a stored "no photo" is asked about again sooner.
	refreshAfter     = 24 * time.Hour
	refreshMissAfter = time.Hour
)

// Store keeps photos between restarts and serves them when Telegram does not
// answer. A nil photo records that the bot could see none.
type Store interface {
	Load(ctx context.Context, telegramID int64) (photo []byte, fetchedAt time.Time, found bool, err error)
	Save(ctx context.Context, telegramID int64, photo []byte) error
}

// Source reads profile photos through the bot.
type Source struct {
	bot    *telegram.Bot
	client *http.Client
	now    func() time.Time

	store Store

	mu      sync.Mutex
	entries map[int64]entry
}

// WithStore keeps photos in store as well as in memory.
func (source *Source) WithStore(store Store) *Source {
	if source != nil {
		source.store = store
	}
	return source
}

type entry struct {
	data    []byte
	expires time.Time
}

// New builds a source for the given bot token. An empty token returns nil: an
// installation without a bot has no avatars to show.
func New(token string, transport config.TelegramTransportConfig) (*Source, error) {
	if token == "" {
		return nil, nil
	}
	options := append(app.TelegramOptions(transport), telegram.WithSkipGetMe())
	bot, err := telegram.New(token, options...)
	if err != nil {
		return nil, fmt.Errorf("telegram avatars: %w", err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if transport.ProxyURL != "" {
		if proxy, parseErr := url.Parse(transport.ProxyURL); parseErr == nil {
			client.Transport = &http.Transport{Proxy: http.ProxyURL(proxy), ForceAttemptHTTP2: true}
		}
	}
	return &Source{bot: bot, client: client, now: time.Now, entries: map[int64]entry{}}, nil
}

// Avatar returns the JPEG bytes of the customer's current profile photo.
func (source *Source) Avatar(ctx context.Context, telegramID int64) ([]byte, error) {
	if cached, ok := source.cached(telegramID); ok {
		if cached == nil {
			return nil, ErrNoAvatar
		}
		return cached, nil
	}
	var (
		stored      []byte
		storedFound bool
	)
	if source.store != nil {
		photo, fetchedAt, found, loadErr := source.store.Load(ctx, telegramID)
		if loadErr == nil && found {
			stored, storedFound = photo, true
			age := source.now().Sub(fetchedAt)
			if (photo != nil && age < refreshAfter) || (photo == nil && age < refreshMissAfter) {
				return source.remember(telegramID, photo)
			}
		}
	}
	data, err := source.fetch(ctx, telegramID)
	switch {
	case errors.Is(err, ErrNoAvatar):
		source.save(ctx, telegramID, nil)
		return source.remember(telegramID, nil)
	case err != nil:
		// Telegram did not answer: the copy kept from before still does.
		if storedFound {
			return source.remember(telegramID, stored)
		}
		return nil, err
	}
	source.save(ctx, telegramID, data)
	return source.remember(telegramID, data)
}

// remember caches an answer in memory and returns it as Avatar does.
func (source *Source) remember(telegramID int64, data []byte) ([]byte, error) {
	if data == nil {
		source.storeEntry(telegramID, nil, missTTL)
		return nil, ErrNoAvatar
	}
	source.storeEntry(telegramID, data, cacheTTL)
	return data, nil
}

// save writes a fetched answer to the store. A failure only costs a refetch.
func (source *Source) save(ctx context.Context, telegramID int64, data []byte) {
	if source.store != nil {
		_ = source.store.Save(ctx, telegramID, data)
	}
}

func (source *Source) fetch(ctx context.Context, telegramID int64) ([]byte, error) {
	photos, err := source.bot.GetUserProfilePhotos(ctx, &telegram.GetUserProfilePhotosParams{
		UserID: telegramID, Limit: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("profile photos: %w", err)
	}
	if photos == nil || len(photos.Photos) == 0 || len(photos.Photos[0]) == 0 {
		return nil, ErrNoAvatar
	}
	// Sizes come smallest first. The second is 320 pixels, which is plenty
	// for a card and far smaller than the original.
	sizes := photos.Photos[0]
	chosen := sizes[min(1, len(sizes)-1)]
	file, err := source.bot.GetFile(ctx, &telegram.GetFileParams{FileID: chosen.FileID})
	if err != nil {
		return nil, fmt.Errorf("profile photo file: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.bot.FileDownloadLink(file), nil)
	if err != nil {
		return nil, err
	}
	response, err := source.client.Do(request)
	if err != nil {
		// The download link carries the bot token; the error is reported
		// without it.
		return nil, errors.New("profile photo download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("profile photo download answered %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, errors.New("profile photo download failed")
	}
	if len(data) > maxBytes {
		return nil, errors.New("profile photo is larger than expected")
	}
	return data, nil
}

func (source *Source) cached(telegramID int64) ([]byte, bool) {
	source.mu.Lock()
	defer source.mu.Unlock()
	found, ok := source.entries[telegramID]
	if !ok || source.now().After(found.expires) {
		return nil, false
	}
	return found.data, true
}

func (source *Source) storeEntry(telegramID int64, data []byte, ttl time.Duration) {
	source.mu.Lock()
	defer source.mu.Unlock()
	if len(source.entries) >= maxEntries {
		// Expired entries go first; if none have, the map is simply reset.
		// An avatar cache that forgets is a refetch, not a fault.
		now := source.now()
		for key, value := range source.entries {
			if now.After(value.expires) {
				delete(source.entries, key)
			}
		}
		if len(source.entries) >= maxEntries {
			source.entries = map[int64]entry{}
		}
	}
	source.entries[telegramID] = entry{data: data, expires: source.now().Add(ttl)}
}
