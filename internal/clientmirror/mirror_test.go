package clientmirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPickReleaseHonoursPrefixAndSkipsUnreleased(t *testing.T) {
	releases := []githubRelease{
		{TagName: "desktop-v4.0.0", Draft: true},
		{TagName: "android-v4.0.3"},
		{TagName: "desktop-v3.9.2", Prerelease: true},
		{TagName: "desktop-v3.9.1"},
	}
	asset := func(name string) struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		Digest             string `json:"digest"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} {
		return struct {
			Name               string `json:"name"`
			Size               int64  `json:"size"`
			Digest             string `json:"digest"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{Name: name, Size: 10, Digest: "sha256:ABC", BrowserDownloadURL: "https://github.com/o/r/releases/download/t/" + name}
	}
	releases[0].Assets = append(releases[0].Assets, asset("incy-windows-setup.exe"))
	releases[1].Assets = append(releases[1].Assets, asset("Incy.apk"))
	releases[2].Assets = append(releases[2].Assets, asset("incy-windows-setup.exe"))
	releases[3].Assets = append(releases[3].Assets, asset("incy-linux-x64.deb"), asset("incy-windows-setup.exe"))

	release, err := pickRelease(releases, Source{Repo: "o/r", TagPrefix: "desktop-v", AssetPattern: "incy-windows-*.exe"})
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if release.Version != "3.9.1" || release.Asset.Name != "incy-windows-setup.exe" {
		t.Fatalf("picked %+v; drafts and pre-releases must be skipped and the prefix honoured", release)
	}
	if release.Asset.Digest != "abc" {
		t.Fatalf("digest %q, want the hex without its sha256: prefix", release.Asset.Digest)
	}

	if _, err := pickRelease(releases, Source{Repo: "o/r", AssetPattern: "nothing.dmg"}); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("no matching asset: %v", err)
	}
}

func TestPickReleaseRefusesAnAssetOffGitHub(t *testing.T) {
	releases := []githubRelease{{TagName: "4.8.3"}}
	releases[0].Assets = append(releases[0].Assets, struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		Digest             string `json:"digest"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}{Name: "Happ.apk", BrowserDownloadURL: "https://evil.example/Happ.apk"})
	if _, err := pickRelease(releases, Source{Repo: "o/r", AssetPattern: "Happ.apk"}); err == nil {
		t.Fatal("an asset whose address is not github.com was accepted")
	}
}

func TestNormaliseAndValidateSource(t *testing.T) {
	source := NormaliseSource(Source{
		Repo: " https://github.com/Happ-proxy/happ-desktop/releases ", AssetPattern: " setup-Happ.x64.exe ",
	})
	if source.Repo != "Happ-proxy/happ-desktop" || source.AssetPattern != "setup-Happ.x64.exe" {
		t.Fatalf("normalised to %+v", source)
	}
	if err := ValidateSource(source); err != nil {
		t.Fatalf("valid source refused: %v", err)
	}
	for _, bad := range []Source{
		{Repo: "happ", AssetPattern: "a.exe"},
		{Repo: "o/r/x", AssetPattern: "a.exe"},
		{Repo: "o/r", AssetPattern: "../a.exe"},
		{Repo: "o/r", AssetPattern: "[a.exe"},
		{Repo: "o/r", TagPrefix: "v 1", AssetPattern: "a.exe"},
	} {
		if ValidateSource(bad) == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
}

func TestContentTypeLetsAndroidInstallAnAPK(t *testing.T) {
	cases := map[string]string{
		"Happ.apk":                    "application/vnd.android.package-archive",
		"setup-Happ.x64.exe":          "application/vnd.microsoft.portable-executable",
		"Happ.macOS.universal.dmg":    "application/x-apple-diskimage",
		"Happ.linux.x64.deb":          "application/vnd.debian.binary-package",
		"Happ.linux.x64.rpm":          "application/x-rpm",
		"Happ.linux.x64.pkg.tar.zst":  "application/zstd",
		"incy-linux-x64-portable.zip": "application/zip",
		"something-without-extension": "application/octet-stream",
	}
	for name, want := range cases {
		if got := ContentType(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}

func TestVersionFromTag(t *testing.T) {
	for tag, want := range map[string]string{"4.8.3": "4.8.3", "v1.2": "1.2", "desktop-v3.9.1": "3.9.1"} {
		prefix := ""
		if strings.HasPrefix(tag, "desktop-v") {
			prefix = "desktop-v"
		}
		if got := versionFromTag(tag, prefix); got != want {
			t.Errorf("%s: %q, want %q", tag, got, want)
		}
	}
	if got := versionFromTag("latest", "latest"); got != "latest" {
		t.Errorf("an empty remainder fell to %q rather than the tag", got)
	}
}

// fakes

type fakeRepository struct {
	due      []Due
	recorded []Fetched
	checked  map[string]string
	refuse   bool
	sums     []string
}

func (repo *fakeRepository) DueMirrors(context.Context, int) ([]Due, error) { return repo.due, nil }
func (repo *fakeRepository) RecordFile(_ context.Context, fetched Fetched) (bool, error) {
	if repo.refuse {
		return false, nil
	}
	repo.recorded = append(repo.recorded, fetched)
	return true, nil
}
func (repo *fakeRepository) RecordChecked(_ context.Context, id, failure string, _ time.Time) error {
	if repo.checked == nil {
		repo.checked = map[string]string{}
	}
	repo.checked[id] = failure
	return nil
}
func (repo *fakeRepository) Checksums(context.Context) ([]string, error) { return repo.sums, nil }

type fakeFiles struct {
	stored  map[string][]byte
	listed  []StoredFile
	removed []string
}

func (files *fakeFiles) PutFile(_ context.Context, key string, content io.Reader, size int64, _ string) error {
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return errors.New("size mismatch")
	}
	if files.stored == nil {
		files.stored = map[string][]byte{}
	}
	files.stored[key] = data
	return nil
}
func (files *fakeFiles) ListFiles(context.Context) ([]StoredFile, error) { return files.listed, nil }
func (files *fakeFiles) RemoveFile(_ context.Context, key string) error {
	files.removed = append(files.removed, key)
	return nil
}

type fakeReleases struct {
	release Release
	err     error
	calls   int
}

func (releases *fakeReleases) Latest(context.Context, Source) (Release, error) {
	releases.calls++
	return releases.release, releases.err
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func assetServer(t *testing.T, content []byte) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = writer.Write(content)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestPassCopiesANewReleaseAndChecksIt(t *testing.T) {
	content := bytes.Repeat([]byte("installer"), 1000)
	server, hits := assetServer(t, content)
	source := Source{Repo: "Happ-proxy/happ-android", AssetPattern: "Happ.apk"}
	repo := &fakeRepository{due: []Due{{ID: "m1", Source: source}}}
	files := &fakeFiles{}
	service := New(Options{
		Repository: repo, Files: files, Download: server.Client(),
		Releases: &fakeReleases{release: Release{Tag: "4.11.2", Version: "4.11.2", Asset: Asset{
			Name: "Happ.apk", Size: int64(len(content)), Digest: digestOf(content), URL: server.URL + "/Happ.apk",
		}}},
	})

	if err := service.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if *hits != 1 || len(repo.recorded) != 1 {
		t.Fatalf("downloads %d, recorded %d", *hits, len(repo.recorded))
	}
	got := repo.recorded[0]
	if got.Checksum != digestOf(content) || got.Size != int64(len(content)) || got.Version != "4.11.2" ||
		got.ContentType != "application/vnd.android.package-archive" || got.Source != source {
		t.Fatalf("recorded %+v", got)
	}
	if !bytes.Equal(files.stored[got.Checksum], content) {
		t.Fatal("the file was not stored under its digest")
	}
}

func TestPassDoesNotDownloadAnUnchangedRelease(t *testing.T) {
	content := []byte("same bytes")
	server, hits := assetServer(t, content)
	repo := &fakeRepository{due: []Due{{
		ID: "m1", Source: Source{Repo: "o/r", AssetPattern: "a.exe"}, Checksum: digestOf(content),
		Version: "1.0", FileName: "a.exe", Size: int64(len(content)),
	}}}
	service := New(Options{
		Repository: repo, Files: &fakeFiles{}, Download: server.Client(),
		Releases: &fakeReleases{release: Release{Version: "1.0.1", Asset: Asset{
			Name: "a.exe", Size: int64(len(content)), Digest: digestOf(content), URL: server.URL,
		}}},
	})
	if err := service.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if *hits != 0 {
		t.Fatal("an asset whose digest the mirror already holds was downloaded again")
	}
	if len(repo.recorded) != 1 || repo.recorded[0].Version != "1.0.1" {
		t.Fatalf("the new tag was not recorded: %+v", repo.recorded)
	}
}

func TestPassRecordsWhyAMirrorFailed(t *testing.T) {
	content := []byte("tampered")
	server, _ := assetServer(t, content)
	cases := map[string]Release{
		"digest": {Version: "1", Asset: Asset{Name: "a.exe", Size: int64(len(content)), Digest: digestOf([]byte("real")), URL: server.URL}},
		"size":   {Version: "1", Asset: Asset{Name: "a.exe", Size: int64(len(content)) + 1, URL: server.URL}},
		"limit":  {Version: "1", Asset: Asset{Name: "a.exe", Size: MaxFileBytes + 1, URL: server.URL}},
	}
	for name, release := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &fakeRepository{due: []Due{{ID: "m1", Source: Source{Repo: "o/r", AssetPattern: "a.exe"}}}}
			files := &fakeFiles{}
			service := New(Options{
				Repository: repo, Files: files, Download: server.Client(),
				Releases: &fakeReleases{release: release},
			})
			if err := service.Pass(context.Background()); err != nil {
				t.Fatalf("a mirror's own failure failed the pass: %v", err)
			}
			if repo.checked["m1"] == "" || len(repo.recorded) != 0 || len(files.stored) != 0 {
				t.Fatalf("checked %q, recorded %d, stored %d", repo.checked["m1"], len(repo.recorded), len(files.stored))
			}
		})
	}

	repo := &fakeRepository{due: []Due{{ID: "m2", Source: Source{Repo: "o/r", AssetPattern: "a.exe"}}}}
	service := New(Options{Repository: repo, Files: &fakeFiles{}, Releases: &fakeReleases{err: ErrNoRelease}})
	if err := service.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if repo.checked["m2"] != ErrNoRelease.Error() {
		t.Fatalf("recorded %q", repo.checked["m2"])
	}
}

func TestSweepRemovesOnlyOldUnreferencedFiles(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	files := &fakeFiles{listed: []StoredFile{
		{Key: "kept", Written: now.Add(-48 * time.Hour)},
		{Key: "orphan", Written: now.Add(-2 * time.Hour)},
		{Key: "fresh", Written: now.Add(-10 * time.Minute)},
	}}
	service := New(Options{
		Repository: &fakeRepository{sums: []string{"kept"}}, Files: files,
		Releases: &fakeReleases{}, Now: func() time.Time { return now },
	})
	if err := service.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(files.removed) != 1 || files.removed[0] != "orphan" {
		t.Fatalf("removed %v; a referenced file and one written within the hour must stay", files.removed)
	}
}
