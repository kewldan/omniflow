// Package telegramavatar fetches a customer's Telegram profile photo for the
// operator panel.
//
// The photo is personal data, so nothing here is written to the database or to
// disk: it is read from the Bot API when an operator opens a customer card,
// kept briefly in memory so a card that re-renders does not ask Telegram again,
// and handed to the operator's browser as a private, short-lived response. A
// customer who removes their photo in Telegram stops appearing with it within
// the cache window.
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
)

// Source reads profile photos through the bot.
type Source struct {
	bot    *telegram.Bot
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	entries map[int64]entry
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
	data, err := source.fetch(ctx, telegramID)
	switch {
	case errors.Is(err, ErrNoAvatar):
		source.store(telegramID, nil, missTTL)
		return nil, err
	case err != nil:
		return nil, err
	}
	source.store(telegramID, data, cacheTTL)
	return data, nil
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

func (source *Source) store(telegramID int64, data []byte, ttl time.Duration) {
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
