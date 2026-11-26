//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/omniflow/omniflow/internal/clientmirror"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/connectpg"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// Downloads: store links and installers this installation mirrors and serves.
// The properties asserted here are the ones a customer depends on — a mirror
// is offered only once it holds a file, a file a mirror took from a source the
// operator has since replaced is never recorded, and nothing is served behind
// a switched-off client.

type memoryFiles map[string][]byte

func (files memoryFiles) OpenFile(_ context.Context, key string) (io.ReadSeekCloser, error) {
	content, ok := files[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return nopSeekCloser{bytes.NewReader(content)}, nil
}

type nopSeekCloser struct{ *bytes.Reader }

func (nopSeekCloser) Close() error { return nil }

func TestAMirrorIsOfferedOnlyOnceItHoldsAFile(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	catalogue := connectpg.New(harness.pool)
	mirrors := connectpg.NewMirrors(harness.pool)
	actor := harness.operator(ctx, t, "downloads@example.test")

	happ := seededClient(ctx, t, service, "android", "Happ")

	// Refusals an operator can act on, before anything is written.
	for _, bad := range []panelpg.ConnectDownload{
		{ClientID: happ.ID, Kind: "store", Label: "Plain", URL: "http://play.example.test/happ"},
		{ClientID: happ.ID, Kind: "mirror", Label: "Bad repo", SourceRepo: "happ", AssetPattern: "Happ.apk"},
		{ClientID: happ.ID, Kind: "mirror", Label: "Bad asset", SourceRepo: "o/r", AssetPattern: "../Happ.apk"},
		{ClientID: happ.ID, Kind: "torrent", Label: "Nope"},
	} {
		if _, err := service.SaveConnectDownload(ctx, bad, actor); !errors.Is(err, panelpg.ErrValidaton) {
			t.Errorf("%+v was accepted: %v", bad, err)
		}
	}

	store, err := service.SaveConnectDownload(ctx, panelpg.ConnectDownload{
		ClientID: happ.ID, Kind: "store", Label: "Google Play", Enabled: true, SortOrder: 20,
		URL: "https://play.google.com/store/apps/details?id=com.happproxy",
	}, actor)
	if err != nil {
		t.Fatalf("store link: %v", err)
	}
	mirror, err := service.SaveConnectDownload(ctx, panelpg.ConnectDownload{
		ClientID: happ.ID, Kind: "mirror", Label: "APK", Enabled: true, SortOrder: 10,
		SourceRepo: "https://github.com/Happ-proxy/happ-android", AssetPattern: "Happ.apk",
	}, actor)
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if mirror.SourceRepo != "Happ-proxy/happ-android" || mirror.NextCheckAt == nil {
		t.Fatalf("saved mirror %+v", mirror)
	}

	// Until the worker copies a file, customers see the store link only.
	if downloads := clientDownloads(ctx, t, catalogue, "android", "Happ"); len(downloads) != 1 || downloads[0].Kind != "store" {
		t.Fatalf("before a file: %+v", downloads)
	}

	due, err := mirrors.DueMirrors(ctx, 10)
	if err != nil || len(due) != 1 || due[0].ID != mirror.ID {
		t.Fatalf("due: %+v, %v", due, err)
	}
	source := due[0].Source
	content := []byte("not really an apk")
	checksum := strings.Repeat("a", 64)
	fetched := clientmirror.Fetched{
		ID: mirror.ID, Source: clientmirror.Source{Repo: "Happ-proxy/other", AssetPattern: "Happ.apk"},
		Version: "4.11.2", FileName: "Happ.apk", Size: int64(len(content)), Checksum: checksum,
		ContentType: clientmirror.ContentType("Happ.apk"), NextCheck: time.Now().Add(time.Hour),
	}
	if recorded, err := mirrors.RecordFile(ctx, fetched); err != nil || recorded {
		t.Fatalf("a file from a source the mirror no longer names was recorded: %v %v", recorded, err)
	}
	fetched.Source = source
	if recorded, err := mirrors.RecordFile(ctx, fetched); err != nil || !recorded {
		t.Fatalf("record: %v %v", recorded, err)
	}
	if due, _ := mirrors.DueMirrors(ctx, 10); len(due) != 0 {
		t.Fatalf("a mirror just checked is due again: %+v", due)
	}

	downloads := clientDownloads(ctx, t, catalogue, "android", "Happ")
	if len(downloads) != 2 || downloads[0].Kind != "file" || downloads[1].Label != "Google Play" {
		t.Fatalf("after a file: %+v", downloads)
	}
	path := "/v1/downloads/" + mirror.ID + "/Happ.apk"
	if downloads[0].URL != path || downloads[0].Version != "4.11.2" || downloads[0].SizeBytes != int64(len(content)) {
		t.Fatalf("file entry %+v", downloads[0])
	}

	// The public address serves it, with ranges, under its own name only.
	router := chi.NewRouter()
	apihttp.NewClientDownloadHandler(mirrors, memoryFiles{checksum: content}, nil, nil, nil).Mount(router)
	response := serve(router, path, "")
	if response.Code != http.StatusOK || response.Body.String() != string(content) {
		t.Fatalf("download: %d %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/vnd.android.package-archive" {
		t.Fatalf("content type %q", got)
	}
	if got := response.Header().Get("Content-Disposition"); got != `attachment; filename="Happ.apk"` {
		t.Fatalf("disposition %q", got)
	}
	if ranged := serve(router, path, "bytes=4-"); ranged.Code != http.StatusPartialContent || ranged.Body.String() != string(content[4:]) {
		t.Fatalf("range: %d %q", ranged.Code, ranged.Body.String())
	}
	if renamed := serve(router, "/v1/downloads/"+mirror.ID+"/Other.apk", ""); renamed.Code != http.StatusNotFound {
		t.Fatalf("another file name answered %d", renamed.Code)
	}

	// Changing the source makes the mirror due at once.
	mirror.TagPrefix = "android-v"
	if _, err := service.SaveConnectDownload(ctx, mirror, actor); err != nil {
		t.Fatalf("resave: %v", err)
	}
	if due, _ := mirrors.DueMirrors(ctx, 10); len(due) != 1 {
		t.Fatalf("a changed source is not due: %+v", due)
	}

	// A switched-off client is not served, as it is not listed.
	happ.Enabled = false
	if _, err := service.SaveConnectClient(ctx, happ, actor); err != nil {
		t.Fatalf("disable client: %v", err)
	}
	if response := serve(router, path, ""); response.Code != http.StatusNotFound {
		t.Fatalf("a disabled client's file answered %d", response.Code)
	}

	// Deleting the downloads leaves nothing referring to the file, which is
	// what lets the sweep reclaim it.
	for _, id := range []string{mirror.ID, store.ID} {
		if err := service.DeleteConnectDownload(ctx, id, actor); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	if sums, err := mirrors.Checksums(ctx); err != nil || len(sums) != 0 {
		t.Fatalf("checksums after delete: %v %v", sums, err)
	}
}

func seededClient(
	ctx context.Context, t *testing.T, service *panelpg.Service, platform, name string,
) panelpg.ConnectClient {
	t.Helper()
	catalogue, err := service.ConnectCatalogue(ctx)
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	for _, client := range catalogue.Clients {
		if client.Platform == platform && client.Name == name {
			return client
		}
	}
	t.Fatalf("no seeded %s on %s", name, platform)
	return panelpg.ConnectClient{}
}

func clientDownloads(
	ctx context.Context, t *testing.T, catalogue *connectpg.Catalogue, platform, name string,
) []commerce.ClientDownload {
	t.Helper()
	clients, err := catalogue.Clients(ctx, platform, "en")
	if err != nil {
		t.Fatalf("clients: %v", err)
	}
	for _, client := range clients {
		if client.Name == name {
			return client.Downloads
		}
	}
	t.Fatalf("no %s on %s", name, platform)
	return nil
}

func serve(handler http.Handler, path, rangeHeader string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if rangeHeader != "" {
		request.Header.Set("Range", rangeHeader)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
