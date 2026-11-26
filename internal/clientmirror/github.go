package clientmirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Release is the newest published release that carries a matching asset.
type Release struct {
	Tag     string
	Version string
	Asset   Asset
}

// Asset is one file of a release.
type Asset struct {
	Name string
	Size int64
	// Digest is the SHA-256 GitHub publishes for the asset, in hex; empty for
	// an asset uploaded before GitHub started computing them.
	Digest string
	URL    string
}

// GitHub reads releases through GitHub's REST API, without a token: a mirror
// is checked every few hours, far inside the anonymous limit.
type GitHub struct {
	client *http.Client
	base   string
}

// NewGitHub builds a reader. A nil client uses one with a short timeout.
func NewGitHub(client *http.Client) *GitHub {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &GitHub{client: client, base: "https://api.github.com"}
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		Digest             string `json:"digest"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// Latest finds the newest published release whose tag starts with the prefix
// and which carries an asset matching the pattern. Drafts and pre-releases are
// skipped: customers are offered what the vendor calls released.
func (github *GitHub) Latest(ctx context.Context, source Source) (Release, error) {
	address := github.base + "/repos/" + source.Repo + "/releases?per_page=30"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return Release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "Omniflow-client-mirror")
	response, err := github.client.Do(request)
	if err != nil {
		return Release{}, fmt.Errorf("GitHub could not be reached: %w", err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusNotFound:
		return Release{}, fmt.Errorf("GitHub has no repository %s", source.Repo)
	case response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests:
		return Release{}, errors.New("GitHub refused the request; its rate limit resets within an hour")
	case response.StatusCode != http.StatusOK:
		return Release{}, fmt.Errorf("GitHub answered %d", response.StatusCode)
	}
	var releases []githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&releases); err != nil {
		return Release{}, fmt.Errorf("GitHub's answer could not be read: %w", err)
	}
	return pickRelease(releases, source)
}

// pickRelease applies the source's rules to a release list in GitHub's order,
// newest first.
func pickRelease(releases []githubRelease, source Source) (Release, error) {
	for _, release := range releases {
		if release.Draft || release.Prerelease || !strings.HasPrefix(release.TagName, source.TagPrefix) {
			continue
		}
		for _, asset := range release.Assets {
			if !matchAsset(source.AssetPattern, asset.Name) {
				continue
			}
			if !ValidFileName(asset.Name) {
				return Release{}, fmt.Errorf("the asset %q has a name that cannot be served as it is", asset.Name)
			}
			if !githubDownload(asset.BrowserDownloadURL) {
				return Release{}, fmt.Errorf("the asset %q is not on github.com", asset.Name)
			}
			return Release{
				Tag:     release.TagName,
				Version: versionFromTag(release.TagName, source.TagPrefix),
				Asset: Asset{
					Name: asset.Name, Size: asset.Size, URL: asset.BrowserDownloadURL,
					Digest: strings.ToLower(strings.TrimPrefix(asset.Digest, "sha256:")),
				},
			}, nil
		}
	}
	return Release{}, ErrNoRelease
}

// githubDownload reports whether an address is a github.com release download.
// GitHub then redirects to its own file host; the downloader follows https
// redirects only.
func githubDownload(address string) bool {
	parsed, err := url.Parse(address)
	return err == nil && parsed.Scheme == "https" && parsed.Host == "github.com" &&
		strings.Contains(parsed.Path, "/releases/download/")
}
