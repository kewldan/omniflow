// Package accountpg is the read and action model behind the customer web panel.
//
// It answers the questions the panel asks — what subscriptions do I have, how
// much traffic is left, which devices are connected, how do I connect a new one
// — by joining Omniflow's own entitlement records to the state Remnawave holds.
// The division of authority is the one the rest of the system uses: Omniflow
// owns the plan, the period, and the label; Remnawave owns traffic, devices, and
// the access link.
//
// Nothing here reimplements a business rule. Phase evaluation comes from
// internal/commerce, the documented client applications come from the same
// table the bot renders, and any state change goes through the same fulfillment
// and Remnawave paths the bot uses.
package accountpg

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/connectpg"
	"github.com/omniflow/omniflow/internal/remnawave"
)

var (
	// ErrNotFound reports a subscription or device that does not belong to the
	// calling customer. It is the same error for "does not exist" and "is not
	// yours", so an identifier cannot be probed for existence.
	ErrNotFound = errors.New("not found")
	// ErrRemnawaveUnavailable reports that the upstream panel could not be
	// reached. The customer's own records are still shown; the live figures are
	// marked stale instead of the page failing.
	ErrRemnawaveUnavailable = errors.New("remnawave is unavailable")
	// ErrNotProvisioned reports a subscription that has no Remnawave user yet,
	// which is a normal state between payment and fulfillment.
	ErrNotProvisioned = errors.New("subscription is not provisioned yet")
	// ErrInvalidInput reports a value the customer supplied that the domain
	// refused. It wraps the domain's own message, so the panel can show why
	// without this package restating the rule.
	ErrInvalidInput = errors.New("invalid input")
)

// SecurityRecorder receives the account events a customer can read back.
//
// It is an interface rather than a direct dependency on customerauthpg so this
// package stays a read model with one narrow write: a device removal or a link
// rotation has to appear in the customer's security log, and nothing else here
// touches identity.
type SecurityRecorder interface {
	RecordSecurityEvent(
		ctx context.Context, customerID, event string,
		request SecurityRequest, metadata map[string]any,
	) error
}

// SecurityRequest mirrors the transport detail the security log records.
type SecurityRequest struct {
	IP        string
	UserAgent string
	RequestID string
}

// SecurityRecorderFunc adapts a plain function to SecurityRecorder, so the
// process that owns both this package and the identity adapter can bridge them
// without either one importing the other.
type SecurityRecorderFunc func(
	ctx context.Context, customerID, event string,
	request SecurityRequest, metadata map[string]any,
) error

// RecordSecurityEvent satisfies SecurityRecorder.
func (record SecurityRecorderFunc) RecordSecurityEvent(
	ctx context.Context, customerID, event string,
	request SecurityRequest, metadata map[string]any,
) error {
	return record(ctx, customerID, event, request, metadata)
}

// Service is the customer account adapter.
type Service struct {
	pool      *pgxpool.Pool
	remnawave *remnawave.Client
	security  SecurityRecorder
	logger    *slog.Logger
	clock     func() time.Time
	// connect reads the operator's connection guidance. It is built from the
	// same pool rather than passed in, because the alternative is a caller that
	// can construct this service without one — and a connect screen with no
	// catalogue behind it is a screen with no buttons.
	connect *connectpg.Catalogue
}

// Options configures a Service.
type Options struct {
	Security SecurityRecorder
	Logger   *slog.Logger
	Clock    func() time.Time
}

// New builds the adapter. A nil Remnawave client is allowed: the panel still
// renders Omniflow's own records and reports the live figures as unavailable,
// which is what a maintenance window looks like from the customer's side.
func New(pool *pgxpool.Pool, client *remnawave.Client, options Options) (*Service, error) {
	if pool == nil {
		return nil, errors.New("a database pool is required")
	}
	service := &Service{
		pool: pool, remnawave: client,
		security: options.Security, logger: options.Logger, clock: options.Clock,
		connect: connectpg.New(pool),
	}
	if service.logger == nil {
		service.logger = slog.Default()
	}
	if service.clock == nil {
		service.clock = time.Now
	}
	return service, nil
}

func (service *Service) now() time.Time { return service.clock().UTC() }

// recordSecurity appends to the customer's security log when a recorder is
// attached. A failure is logged and swallowed: the action the customer asked
// for has already happened, and failing the response afterwards would tell them
// it did not.
func (service *Service) recordSecurity(
	ctx context.Context, customerID, event string, request SecurityRequest, metadata map[string]any,
) {
	if service.security == nil {
		return
	}
	if err := service.security.RecordSecurityEvent(ctx, customerID, event, request, metadata); err != nil {
		service.logger.Warn("customer security event was not recorded", "event", event, "error", err)
	}
}
