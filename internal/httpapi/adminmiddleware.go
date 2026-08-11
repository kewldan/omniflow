package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// principalContextKey carries the resolved operator through a request.
type principalContextKey struct{}

// PrincipalFrom returns the authenticated operator, if the request passed
// through requireSession.
func PrincipalFrom(ctx context.Context) (adminauthpg.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(adminauthpg.Principal)
	return principal, ok
}

// SecurityHeaders applies the restrictive default headers every admin response
// carries.
//
// The Content-Security-Policy is deliberately strict and frame-ancestors is
// 'none', so the panel cannot be embedded and clickjacked. It is applied at the
// API layer as defence in depth; the Next.js surface sets its own policy for
// the documents it serves.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		header := writer.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		header.Set(
			"Content-Security-Policy",
			"default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
		)
		// A response carrying operator data must never be stored by a shared
		// cache or replayed from the browser's back-forward cache.
		header.Set("Cache-Control", "no-store")
		next.ServeHTTP(writer, request)
	})
}

// TrustedProxies decides whether a forwarded client address may be believed.
//
// An X-Forwarded-For header is trivially forged, so it is honoured only when
// the immediate peer is a configured proxy. With no proxies configured, the
// header is ignored entirely and the socket address is used, which is the
// correct default for a directly exposed deployment.
type TrustedProxies struct {
	networks []netip.Prefix
}

// NewTrustedProxies parses CIDR blocks. An entry that is a bare address is
// accepted and treated as a single-host network.
func NewTrustedProxies(entries []string) (*TrustedProxies, error) {
	trusted := &TrustedProxies{}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(entry); err == nil {
			trusted.networks = append(trusted.networks, prefix)
			continue
		}
		address, err := netip.ParseAddr(entry)
		if err != nil {
			return nil, errors.New("trusted proxy entry must be an IP address or CIDR block")
		}
		trusted.networks = append(trusted.networks, netip.PrefixFrom(address, address.BitLen()))
	}
	return trusted, nil
}

// ClientIP resolves the address to record against a session or an audit event.
func (trusted *TrustedProxies) ClientIP(request *http.Request) *netip.Addr {
	peer := socketAddr(request.RemoteAddr)
	if peer == nil || trusted == nil || len(trusted.networks) == 0 {
		return peer
	}
	if !trusted.contains(*peer) {
		return peer
	}

	// Walk right to left and stop at the first address that is not itself a
	// trusted proxy: that is the closest hop we still have grounds to believe.
	forwarded := request.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return peer
	}
	hops := strings.Split(forwarded, ",")
	for index := len(hops) - 1; index >= 0; index-- {
		address, err := netip.ParseAddr(strings.TrimSpace(hops[index]))
		if err != nil {
			continue
		}
		if !trusted.contains(address) {
			return &address
		}
	}
	return peer
}

func (trusted *TrustedProxies) contains(address netip.Addr) bool {
	for _, network := range trusted.networks {
		if network.Contains(address) {
			return true
		}
	}
	return false
}

func socketAddr(remote string) *netip.Addr {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	address, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return nil
	}
	unmapped := address.Unmap()
	return &unmapped
}

// requestContext gathers the transport detail recorded against state changes.
func (handlers *AdminHandlers) requestContext(request *http.Request) adminauthpg.RequestContext {
	return adminauthpg.RequestContext{
		IP:        handlers.proxies.ClientIP(request),
		UserAgent: request.UserAgent(),
		RequestID: middleware.GetReqID(request.Context()),
	}
}

// requireSession resolves the session cookie into a principal.
//
// A rotated token is written back as a fresh cookie here rather than in each
// handler, so rotation is invisible to the routes above it.
func (handlers *AdminHandlers) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie(handlers.cookieName)
		if err != nil || cookie.Value == "" {
			writeProblem(writer, request, http.StatusUnauthorized, "unauthenticated", "Sign-in is required")
			return
		}

		principal, err := handlers.service.Resolve(request.Context(), cookie.Value)
		switch {
		case errors.Is(err, adminauthpg.ErrChallengeRequired):
			writeProblem(
				writer, request, http.StatusUnauthorized,
				"challenge_required", "Two-factor verification is required",
			)
			return
		case errors.Is(err, adminauthpg.ErrSessionInvalid), errors.Is(err, adminauthpg.ErrAccountDisabled):
			// The cookie is cleared so a browser holding a dead session stops
			// presenting it on every subsequent request.
			handlers.clearSessionCookie(writer)
			writeProblem(writer, request, http.StatusUnauthorized, "unauthenticated", "Sign-in is required")
			return
		case err != nil:
			writeProblem(writer, request, http.StatusInternalServerError, "session_unavailable", "Session lookup failed")
			return
		}

		if principal.RotatedToken != "" {
			handlers.setSessionCookie(writer, principal.RotatedToken, principal.ExpiresAt)
		}
		// The panel reads this to attach the token to unsafe requests.
		writer.Header().Set("X-CSRF-Token", principal.CSRFToken)

		ctx := context.WithValue(request.Context(), principalContextKey{}, principal)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// requireCSRF enforces the double-submit token on every state-changing request.
//
// Safe methods are exempt because they change nothing. SameSite=Lax already
// blocks most cross-site form posts, but it is a browser-side control only, so
// the server verifies the token itself rather than relying on it.
func (handlers *AdminHandlers) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(writer, request)
			return
		}

		principal, ok := PrincipalFrom(request.Context())
		if !ok {
			writeProblem(writer, request, http.StatusUnauthorized, "unauthenticated", "Sign-in is required")
			return
		}
		submitted := request.Header.Get("X-CSRF-Token")
		if submitted == "" || submitted != principal.CSRFToken {
			writeProblem(writer, request, http.StatusForbidden, "csrf_failed", "CSRF token is missing or invalid")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// requirePermission gates a route on the operator's effective permissions.
//
// A denial is written to the audit trail: an operator repeatedly reaching for
// something they cannot do is exactly the signal an audit review looks for.
func (handlers *AdminHandlers) requirePermission(permissions ...rbac.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			principal, ok := PrincipalFrom(request.Context())
			if !ok {
				writeProblem(writer, request, http.StatusUnauthorized, "unauthenticated", "Sign-in is required")
				return
			}
			if !principal.Grant.AllowsAll(permissions...) {
				required := make([]string, 0, len(permissions))
				for _, permission := range permissions {
					required = append(required, string(permission))
				}
				_ = handlers.service.AppendAudit(request.Context(), adminauthpg.AuditEntry{
					ActorType: "admin", ActorID: principal.Account.ID,
					Action: "admin.authorization.denied", Category: "authorization", Outcome: "denied",
					TargetType: "endpoint", TargetID: request.Method + " " + request.URL.Path,
					RequestID: middleware.GetReqID(request.Context()),
					Metadata:  map[string]any{"required": required},
				})
				writeProblem(
					writer, request, http.StatusForbidden,
					"forbidden", "This account does not have permission for that action",
				)
				return
			}
			next.ServeHTTP(writer, request)
		})
	}
}
