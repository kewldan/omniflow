package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/omniflow/omniflow/internal/connectpg"
	"github.com/omniflow/omniflow/internal/platform"
)

// clientDownloadsPrefix is where mirrored client installers are served. The
// router's request timeout does not apply under it: a 150 MB installer on a
// slow mobile connection takes longer than any API call should.
const clientDownloadsPrefix = "/v1/downloads/"

// clientDownloadMaxDuration bounds one download, so a connection that stalls
// forever does not hold a store reader forever.
const clientDownloadMaxDuration = 2 * time.Hour

// clientDownloadsPerHour bounds one address. A customer installs a client on a
// few devices; a carrier's NAT may put many customers behind one address, so
// the bound is generous, and a resumed download's range requests count too.
const clientDownloadsPerHour = 120

// ClientFileOpener opens a stored installer by its SHA-256.
type ClientFileOpener interface {
	OpenFile(ctx context.Context, key string) (io.ReadSeekCloser, error)
}

// ClientDownloadHandler serves mirrored client installers from the
// installation's own address: GET /v1/downloads/{downloadID}/{fileName}.
//
// It is public. An installer holds nothing about a customer, and somebody
// setting up a VPN client often is not signed in on that device yet — or is
// sending the link to a relative.
type ClientDownloadHandler struct {
	mirrors *connectpg.Mirrors
	files   ClientFileOpener
	limiter *platform.RateLimiter
	proxies *TrustedProxies
	logger  *slog.Logger
}

// NewClientDownloadHandler builds the handler. A nil limiter leaves the route
// unlimited; nil files answers that nothing can be served.
func NewClientDownloadHandler(
	mirrors *connectpg.Mirrors, files ClientFileOpener, limiter *platform.RateLimiter,
	proxies *TrustedProxies, logger *slog.Logger,
) *ClientDownloadHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ClientDownloadHandler{mirrors: mirrors, files: files, limiter: limiter, proxies: proxies, logger: logger}
}

// Mount registers the route.
func (handler *ClientDownloadHandler) Mount(router chi.Router) {
	router.Get(clientDownloadsPrefix+"{downloadID}/{fileName}", handler.serve)
	router.Head(clientDownloadsPrefix+"{downloadID}/{fileName}", handler.serve)
}

func (handler *ClientDownloadHandler) serve(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if handler.mirrors == nil || handler.files == nil {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "No such download")
		return
	}
	if !handler.allow(request) {
		writer.Header().Set("Retry-After", "600")
		writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many downloads from this address")
		return
	}

	file, err := handler.mirrors.File(request.Context(), chi.URLParam(request, "downloadID"))
	if errors.Is(err, connectpg.ErrNoFile) || (err == nil && file.FileName != chi.URLParam(request, "fileName")) {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "No such download")
		return
	}
	if err != nil {
		handler.logger.Error("client download could not be read", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "internal_error", "The download could not be read")
		return
	}

	// The request's own context carries the router's timeout, which this
	// prefix skips; the download gets its own bound and still ends when the
	// customer goes away.
	ctx, cancel := context.WithTimeout(request.Context(), clientDownloadMaxDuration)
	defer cancel()
	content, err := handler.files.OpenFile(ctx, file.Checksum)
	if errors.Is(err, os.ErrNotExist) {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "No such download")
		return
	}
	if err != nil {
		handler.logger.Warn("client download could not be opened", "error", err)
		writer.Header().Set("Retry-After", "30")
		writeProblem(writer, request, http.StatusServiceUnavailable, "store_unavailable", "The download is temporarily unavailable")
		return
	}
	defer content.Close()

	_ = http.NewResponseController(writer).SetWriteDeadline(time.Now().Add(clientDownloadMaxDuration))
	writer.Header().Set("Content-Type", file.ContentType)
	writer.Header().Set("Content-Disposition", `attachment; filename="`+file.FileName+`"`)
	// The address names a download rather than a version, so a cache may keep
	// it for a while but must revalidate: the ETag is the content's digest,
	// which makes that one cheap request.
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	writer.Header().Set("ETag", `"`+file.Checksum+`"`)
	// ServeContent answers ranges, so an interrupted download resumes rather
	// than starting over — which matters most on the connections this mirror
	// exists for.
	http.ServeContent(writer, request, file.FileName, file.FetchedAt, content)
}

func (handler *ClientDownloadHandler) allow(request *http.Request) bool {
	if handler.limiter == nil {
		return true
	}
	address := handler.proxies.ClientIP(request)
	if address == nil {
		return true
	}
	allowed, err := handler.limiter.Allow(request.Context(), "client_download", address.String(),
		clientDownloadsPerHour, time.Hour)
	return err != nil || allowed
}

// withRequestTimeout bounds every request except a client download, which
// takes as long as the customer's connection does.
func withRequestTimeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		bounded := middleware.Timeout(timeout)(next)
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if strings.HasPrefix(request.URL.Path, clientDownloadsPrefix) {
				next.ServeHTTP(writer, request)
				return
			}
			bounded.ServeHTTP(writer, request)
		})
	}
}
