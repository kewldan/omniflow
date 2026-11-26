// Package clientmirror keeps copies of client installers on the installation's
// own address.
//
// Customers in Russia often cannot open GitHub or a vendor's site without a
// VPN, which is exactly what they are trying to install. A mirror names a
// GitHub release asset; the worker copies the newest release's asset into the
// object store, and the API serves it. Store links (App Store, Google Play)
// need no copy and live beside mirrors in the same table.
package clientmirror

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// MaxFileBytes bounds one mirrored file. The largest desktop build in use is
// about 160 MB; the table carries the same limit.
const MaxFileBytes = 300 << 20

// CheckInterval is how long a mirror waits after a successful check.
const CheckInterval = 6 * time.Hour

// RetryInterval is how long a mirror waits after a failed one. Shorter than a
// check, because a failure is usually a network that recovers.
const RetryInterval = 30 * time.Minute

// Kinds of download.
const (
	KindStore  = "store"
	KindMirror = "mirror"
)

// Source names a GitHub release asset.
type Source struct {
	// Repo is `owner/name`.
	Repo string
	// TagPrefix is what a release's tag must start with. Empty accepts every
	// release; INCY needs `desktop-v` or `android-v`, because its Android and
	// desktop builds are released apart in one repository.
	TagPrefix string
	// AssetPattern is the asset's file name, with * and ? as wildcards.
	AssetPattern string
}

var (
	repoPattern      = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	tagPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9_.+-]{0,40}$`)
	assetPattern     = regexp.MustCompile(`^[A-Za-z0-9_.+*?-]{1,120}$`)
	fileNamePattern  = regexp.MustCompile(`^[A-Za-z0-9_.+-]{1,120}$`)
)

// NormaliseSource trims a source and accepts a repository's GitHub address in
// place of `owner/name`, since that is what an operator has open in a tab.
func NormaliseSource(source Source) Source {
	repo := strings.TrimSpace(source.Repo)
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "github.com/"} {
		if strings.HasPrefix(strings.ToLower(repo), prefix) {
			repo = repo[len(prefix):]
			parts := strings.SplitN(repo, "/", 3)
			if len(parts) >= 2 {
				repo = parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
			}
			break
		}
	}
	return Source{
		Repo:         strings.Trim(repo, "/"),
		TagPrefix:    strings.TrimSpace(source.TagPrefix),
		AssetPattern: strings.TrimSpace(source.AssetPattern),
	}
}

// ValidateSource refuses a source the table would refuse, with a message that
// names the field.
func ValidateSource(source Source) error {
	if !repoPattern.MatchString(source.Repo) {
		return fmt.Errorf("%q is not a GitHub repository; write it as owner/name", source.Repo)
	}
	if !tagPrefixPattern.MatchString(source.TagPrefix) {
		return fmt.Errorf("the tag prefix may hold letters, digits, and . _ + - only, up to 40")
	}
	if !assetPattern.MatchString(source.AssetPattern) {
		return fmt.Errorf("the asset name may hold letters, digits, . _ + - and the wildcards * and ?")
	}
	if _, err := path.Match(source.AssetPattern, ""); err != nil {
		return fmt.Errorf("the asset name is not a valid pattern")
	}
	return nil
}

// matchAsset reports whether an asset's name matches the pattern.
func matchAsset(pattern, name string) bool {
	matched, err := path.Match(pattern, name)
	return err == nil && matched
}

// versionFromTag strips the source's prefix and a leading v from a tag:
// `desktop-v3.9.1` with prefix `desktop-v` and `v4.8.3` both read as the
// version a customer recognises.
func versionFromTag(tag, prefix string) string {
	version := strings.TrimPrefix(tag, prefix)
	version = strings.TrimLeft(version, "vV")
	version = strings.TrimSpace(version)
	if version == "" {
		version = tag
	}
	if len(version) > 60 {
		version = version[:60]
	}
	return version
}

// ContentType is the type a file is served with. An APK in particular must be
// served as one, or Android offers to open it as a document rather than to
// install it.
func ContentType(fileName string) string {
	name := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(name, ".apk"):
		return "application/vnd.android.package-archive"
	case strings.HasSuffix(name, ".exe"), strings.HasSuffix(name, ".msi"):
		return "application/vnd.microsoft.portable-executable"
	case strings.HasSuffix(name, ".dmg"):
		return "application/x-apple-diskimage"
	case strings.HasSuffix(name, ".pkg.tar.zst"), strings.HasSuffix(name, ".zst"):
		return "application/zstd"
	case strings.HasSuffix(name, ".deb"):
		return "application/vnd.debian.binary-package"
	case strings.HasSuffix(name, ".rpm"):
		return "application/x-rpm"
	case strings.HasSuffix(name, ".zip"):
		return "application/zip"
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return "application/gzip"
	}
	return "application/octet-stream"
}

// ValidFileName reports whether a name is safe to put in a download address
// and a Content-Disposition header as it is.
func ValidFileName(name string) bool {
	return fileNamePattern.MatchString(name) && name != "." && name != ".."
}

// ErrNoRelease means the repository has no published release with a matching
// tag and asset.
var ErrNoRelease = errors.New("no published release has a matching asset")
