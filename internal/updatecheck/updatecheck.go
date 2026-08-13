// Package updatecheck answers "is there a newer release than the one running?".
//
// The diagnostics bundle has always reported the running version, the schema
// state, and every applied migration. It could not say whether a newer release
// existed, because answering that means reaching a feed, and a network call an
// installation makes without being asked is a disclosure its owner did not
// choose: the request tells whoever serves the feed that this installation
// exists, roughly where it is, and which version it runs.
//
// So the check is off unless an owner sets `APP_UPDATE_FEED_URL`. There is no
// default address. A default would be a decision made on the owner's behalf, and
// it would also be a fact this project's documentation does not state — the docs
// name no repository, and inventing one here would put an address in a binary
// that nothing else in the installation can corroborate.
//
// Nothing here can block anything. A feed that hangs, refuses, or answers with
// nonsense produces a status that says so, and every caller renders that
// differently from "you are up to date" — an installation that cannot reach the
// feed has not been told it is current.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Status is what the diagnostics bundle renders.
type Status struct {
	// Enabled is false when no feed is configured, which is the default. The
	// panel says "not enabled" rather than "up to date", because they are not
	// the same claim.
	Enabled bool `json:"enabled"`
	// Current is the running build, repeated here so the comparison the status
	// reports can be checked rather than trusted.
	Current string `json:"current"`
	// Latest is the newest version the feed named, empty when the feed could not
	// be read or did not name one.
	Latest string `json:"latest,omitempty"`
	// UpdateAvailable is true only when a newer version was positively
	// identified. An unreadable feed leaves it false and sets Unreachable, so no
	// caller can mistake silence for reassurance.
	UpdateAvailable bool `json:"updateAvailable"`
	// Unreachable reports that the last attempt failed. The reason is a category
	// rather than the transport's message, which can carry the address and any
	// token embedded in it.
	Unreachable bool      `json:"unreachable"`
	Detail      string    `json:"detail,omitempty"`
	CheckedAt   time.Time `json:"checkedAt,omitzero"`
}

// Checker reads a release feed, at most once per interval.
//
// It caches because the diagnostics bundle is a screen an operator refreshes,
// and one request per refresh would be both wasteful and a louder disclosure
// than the owner agreed to.
type Checker struct {
	feedURL string
	current string
	client  *http.Client
	every   time.Duration
	now     func() time.Time

	mu     sync.Mutex
	cached Status
}

// Options configures the checker.
type Options struct {
	// FeedURL is the address to read. Empty disables the check entirely, which
	// is the default state of an installation.
	FeedURL string
	// Current is the running build. An unset version disables the check as
	// well: a comparison against "unknown" can only produce a wrong answer.
	Current string
	// Every bounds how often the feed is actually read. Zero uses the default.
	Every time.Duration
}

// DefaultInterval is how often the feed is read at most. Six hours is far more
// often than releases happen and far less often than an operator refreshes a
// screen.
const DefaultInterval = 6 * time.Hour

// requestTimeout bounds one attempt. The diagnostics bundle is rendered while
// somebody waits, and a feed that needs longer than this has failed the check
// as far as that person is concerned.
const requestTimeout = 5 * time.Second

// maxFeedBody bounds what is read from a feed nobody here controls.
const maxFeedBody = 1 << 20

// New builds the checker. A checker with no feed is valid and reports the
// disabled status to everything, which is what an installation that has not
// opted in gets.
func New(options Options) *Checker {
	every := options.Every
	if every <= 0 {
		every = DefaultInterval
	}
	return &Checker{
		feedURL: strings.TrimSpace(options.FeedURL),
		current: strings.TrimSpace(options.Current),
		client:  &http.Client{Timeout: requestTimeout},
		every:   every,
		now:     time.Now,
	}
}

// Status answers, reading the feed only when the cached answer has expired.
func (checker *Checker) Status(ctx context.Context) Status {
	if checker == nil || checker.feedURL == "" {
		return Status{Current: checker.currentOrUnknown()}
	}
	if checker.current == "" || checker.current == "unknown" {
		return Status{
			Enabled: true, Current: "unknown", Unreachable: true,
			Detail: "the running build has no version, so nothing can be compared against it",
		}
	}

	checker.mu.Lock()
	defer checker.mu.Unlock()
	if !checker.cached.CheckedAt.IsZero() &&
		checker.now().Sub(checker.cached.CheckedAt) < checker.every {
		return checker.cached
	}

	status := Status{Enabled: true, Current: checker.current, CheckedAt: checker.now()}
	latest, err := checker.latest(ctx)
	switch {
	case err != nil:
		status.Unreachable = true
		status.Detail = detailFor(err)
	default:
		status.Latest = latest
		status.UpdateAvailable = newer(latest, checker.current)
	}
	checker.cached = status
	return status
}

func (checker *Checker) currentOrUnknown() string {
	if checker == nil || checker.current == "" {
		return "unknown"
	}
	return checker.current
}

// Reasons a feed could not be used. They are categories rather than transport
// messages: a URL error carries the address it dialled, and an owner's feed
// address can hold a token.
var (
	errFeedRefused     = errors.New("the release feed refused the request")
	errFeedUnreachable = errors.New("the release feed could not be reached")
	errFeedUnreadable  = errors.New("the release feed did not name a version")
)

func detailFor(err error) string {
	switch {
	case errors.Is(err, errFeedRefused):
		return errFeedRefused.Error()
	case errors.Is(err, errFeedUnreadable):
		return errFeedUnreadable.Error()
	default:
		return errFeedUnreachable.Error()
	}
}

// latest reads the feed and returns the version it names.
//
// Two shapes are accepted, and both are documented. `tag_name` is what a GitHub
// releases endpoint answers, so an owner can point straight at one. `version` is
// for an owner who publishes their own file, which is the case for anybody
// running a fork or an internal mirror.
func (checker *Checker) latest(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, checker.feedURL, nil)
	if err != nil {
		return "", errFeedUnreachable
	}
	request.Header.Set("Accept", "application/json")

	response, err := checker.client.Do(request)
	if err != nil {
		return "", errFeedUnreachable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", errFeedRefused
	}

	var document struct {
		TagName string `json:"tag_name"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxFeedBody)).Decode(&document); err != nil {
		return "", errFeedUnreadable
	}
	named := strings.TrimSpace(document.TagName)
	if named == "" {
		named = strings.TrimSpace(document.Version)
	}
	if named == "" {
		return "", errFeedUnreadable
	}
	return named, nil
}

// newer reports whether `candidate` is a later release than `current`.
//
// It compares the dotted numeric parts and nothing else. A version it cannot
// parse answers false, because the failure mode of a guess here is telling an
// operator to upgrade to something that is not newer, and the failure mode of
// refusing is a status that stays quiet — the second is the one to have.
//
// A prerelease sorts below the plain release of the same numbers: 1.0.0-rc.1 is
// not an upgrade from 1.0.0. It is not otherwise ordered, because ordering
// prereleases correctly needs the whole of the semver spec and no installation
// should learn about a release candidate from this.
func newer(candidate, current string) bool {
	candidateNumbers, candidatePre := split(candidate)
	currentNumbers, currentPre := split(current)
	if candidateNumbers == nil || currentNumbers == nil {
		return false
	}

	for index := 0; index < len(candidateNumbers) || index < len(currentNumbers); index++ {
		left, right := at(candidateNumbers, index), at(currentNumbers, index)
		if left != right {
			return left > right
		}
	}
	// The numbers are equal: only a plain release beats a prerelease.
	return currentPre && !candidatePre
}

// split separates the numeric components from a prerelease or build suffix.
func split(version string) ([]int, bool) {
	trimmed := strings.TrimSpace(version)
	trimmed = strings.TrimPrefix(trimmed, "v")
	prerelease := false
	if cut := strings.IndexAny(trimmed, "-+"); cut >= 0 {
		prerelease = true
		trimmed = trimmed[:cut]
	}
	if trimmed == "" {
		return nil, prerelease
	}

	parts := strings.Split(trimmed, ".")
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return nil, prerelease
		}
		numbers = append(numbers, value)
	}
	return numbers, prerelease
}

func at(numbers []int, index int) int {
	if index < len(numbers) {
		return numbers[index]
	}
	return 0
}
