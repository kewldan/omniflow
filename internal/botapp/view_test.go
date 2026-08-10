package botapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/remnawave"
)

func TestHomeViewEscapesDynamicContent(t *testing.T) {
	t.Parallel()
	view := homeView(LocaleEnglish, remnawave.User{
		Username:          `<admin&friend>`,
		Status:            "ACTIVE",
		TrafficLimitBytes: 10 * 1024 * 1024 * 1024,
		ExpireAt:          time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		Traffic:           remnawave.UserTraffic{UsedBytes: 5 * 1024 * 1024 * 1024},
	})
	if strings.Contains(view.Text, `<admin&friend>`) || !strings.Contains(view.Text, `&lt;admin&amp;friend&gt;`) {
		t.Fatalf("dynamic HTML was not escaped: %s", view.Text)
	}
	if !strings.Contains(view.Text, "■■■■■□□□□□ 50%") {
		t.Fatalf("traffic progress is missing: %s", view.Text)
	}
}

func TestSubscriptionViewProvidesOpenAndCopyActions(t *testing.T) {
	t.Parallel()
	view := subscriptionView(LocaleRussian, remnawave.Subscription{
		Found:           true,
		SubscriptionURL: "https://sub.example/secret",
		User: remnawave.SubscriptionUser{
			DaysLeft:     30,
			TrafficUsed:  "1 GB",
			TrafficLimit: "100 GB",
			ExpiresAt:    time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			Status:       "ACTIVE",
		},
	})
	rows := view.Keyboard.InlineKeyboard
	if len(rows) < 3 || rows[0][0].URL == "" || rows[1][0].CopyText == nil {
		t.Fatalf("expected open and copy actions: %#v", rows)
	}
	if !view.Protect {
		t.Fatal("subscription view must be protected")
	}
}

func TestDevicesViewDoesNotExposeIdentifiers(t *testing.T) {
	t.Parallel()
	platform, model := "iOS", "Phone <Pro>"
	view := devicesView(LocaleEnglish, remnawave.Devices{
		Total: 1,
		Devices: []remnawave.Device{{
			Platform:    &platform,
			DeviceModel: &model,
			UpdatedAt:   time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		}},
	}, nil)
	if !strings.Contains(view.Text, "Phone &lt;Pro&gt;") || !strings.Contains(view.Text, "never displays HWIDs or IP addresses") {
		t.Fatalf("unexpected privacy-safe device view: %s", view.Text)
	}
}

func TestLoadViewAutoLinksExactTelegramMatch(t *testing.T) {
	t.Parallel()
	store := &fakeIdentityStore{lookupErr: ErrNotLinked}
	service := &fakeRemnawave{telegramUser: remnawave.User{ID: 44, Username: "linked", Status: "ACTIVE"}}
	app := New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, service, "")

	view := app.loadView(context.Background(), 123456789, LocaleEnglish, routeHome)
	if store.linkedTelegram != 123456789 || store.linkedRemnawave != 44 {
		t.Fatalf("identity was not persisted: %#v", store)
	}
	if !strings.Contains(view.Text, "linked") {
		t.Fatalf("linked account view was not rendered: %s", view.Text)
	}
}

type fakeIdentityStore struct {
	lookupID        int64
	lookupErr       error
	linkedTelegram  int64
	linkedRemnawave int64
}

func (store *fakeIdentityStore) RemnawaveUserID(context.Context, int64) (int64, error) {
	return store.lookupID, store.lookupErr
}

func (store *fakeIdentityStore) Link(_ context.Context, telegramID, remnawaveID int64) (int64, error) {
	store.linkedTelegram = telegramID
	store.linkedRemnawave = remnawaveID
	return remnawaveID, nil
}

type fakeRemnawave struct {
	telegramUser remnawave.User
}

func (service *fakeRemnawave) User(context.Context, int64) (remnawave.User, error) {
	return service.telegramUser, nil
}

func (service *fakeRemnawave) UserByTelegramID(context.Context, int64) (remnawave.User, error) {
	if service.telegramUser.ID == 0 {
		return remnawave.User{}, errors.New("missing test user")
	}
	return service.telegramUser, nil
}

func (service *fakeRemnawave) Subscription(context.Context, int64) (remnawave.Subscription, error) {
	return remnawave.Subscription{}, nil
}

func (service *fakeRemnawave) Devices(context.Context, int64) (remnawave.Devices, error) {
	return remnawave.Devices{}, nil
}
