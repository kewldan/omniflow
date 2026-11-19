package tonwallet

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// GasReserveNano is kept on top of a payment's amount when checking the
// balance, the fork's MIN_GRAM_BALANCE of 0.01 TON.
const GasReserveNano int64 = 10_000_000

// ErrBeforeBroadcast marks a failure that happened before anything was sent.
// A payment that fails with it may be retried safely.
var ErrBeforeBroadcast = errors.New("TON payment failed before broadcast")

// InsufficientBalanceError reports a wallet that cannot cover the payment plus
// the gas reserve. It is always a before-broadcast failure.
type InsufficientBalanceError struct {
	BalanceNano  int64
	RequiredNano int64
}

func (err InsufficientBalanceError) Error() string {
	return fmt.Sprintf("TON wallet balance %d nanoton is below the %d required",
		err.BalanceNano, err.RequiredNano)
}

func (err InsufficientBalanceError) Is(target error) bool { return target == ErrBeforeBroadcast }

// BroadcastError reports a payment whose message may have reached the network.
// The caller must not retry it: the outcome is unknown.
type BroadcastError struct {
	Hash  string
	Cause error
	// Unconfirmed is true when the message was accepted by the API but the
	// wallet was not seen to spend within the confirmation window.
	Unconfirmed bool
}

func (err *BroadcastError) Error() string {
	if err.Unconfirmed {
		return "TON payment was sent but not confirmed in time"
	}
	return "TON payment broadcast failed: " + err.Cause.Error()
}

func (err *BroadcastError) Unwrap() error { return err.Cause }

// PayOptions tunes the confirmation wait and the rate-limit resend.
type PayOptions struct {
	// ConfirmInterval and ConfirmAttempts default to the fork's 3 s × 40.
	ConfirmInterval time.Duration
	ConfirmAttempts int
	// MaxResends bounds re-sending the same bytes after HTTP 429. The fork
	// allows five resends after the first attempt.
	MaxResends int
	// ResendDelay is the pause before a resend; the fork waits 2–3 s.
	ResendDelay func() time.Duration
	// Sleep waits between polls. It exists so tests do not wait in real time.
	Sleep func(context.Context, time.Duration) error
}

func (options PayOptions) withDefaults() PayOptions {
	if options.ConfirmInterval <= 0 {
		options.ConfirmInterval = 3 * time.Second
	}
	if options.ConfirmAttempts <= 0 {
		options.ConfirmAttempts = 40
	}
	if options.MaxResends <= 0 {
		options.MaxResends = 5
	}
	if options.ResendDelay == nil {
		options.ResendDelay = func() time.Duration {
			return 2*time.Second + time.Duration(rand.Int64N(int64(time.Second)))
		}
	}
	if options.Sleep == nil {
		options.Sleep = sleepContext
	}
	return options
}

// Prepared is a signed payment that has not been sent.
type Prepared struct {
	Signed
	TotalNano   int64
	BalanceNano int64
}

// Prepare reads the wallet, checks the balance covers the payment, and signs.
//
// Every failure here is before broadcast and wraps ErrBeforeBroadcast, so the
// caller can record "nothing left" and retry. Nothing is sent.
func Prepare(ctx context.Context, api API, w *Wallet, transfers []Transfer) (Prepared, error) {
	total := int64(0)
	for _, transfer := range transfers {
		if transfer.AmountNano <= 0 {
			return Prepared{}, fmt.Errorf("%w: a message has no amount", ErrBeforeBroadcast)
		}
		total += transfer.AmountNano
	}
	account, err := api.Account(ctx, w.Address())
	if err != nil {
		return Prepared{}, fmt.Errorf("%w: read wallet: %w", ErrBeforeBroadcast, err)
	}
	if required := total + GasReserveNano; account.BalanceNano < required {
		return Prepared{}, InsufficientBalanceError{BalanceNano: account.BalanceNano, RequiredNano: required}
	}
	signed, err := w.Sign(transfers, account.Seqno, account.Deployed)
	if err != nil {
		return Prepared{}, fmt.Errorf("%w: %w", ErrBeforeBroadcast, err)
	}
	return Prepared{Signed: signed, TotalNano: total, BalanceNano: account.BalanceNano}, nil
}

// Broadcast sends a prepared payment and waits until the wallet is seen to
// have spent it.
//
// The same signed bytes are re-sent only after HTTP 429, which a provider
// answers before forwarding anything. Every other failure — a 5xx, a
// "duplicate message", a lost connection — may mean the first send reached
// the network, so it is returned as a BroadcastError and never retried. The
// bytes are never re-signed: a new signature would carry the next seqno and
// pay a second time if the first one landed.
func Broadcast(ctx context.Context, api API, w *Wallet, prepared Prepared, options PayOptions) (Result, error) {
	options = options.withDefaults()
	var sendErr error
	for attempt := 0; attempt <= options.MaxResends; attempt++ {
		sendErr = api.SendBOC(ctx, prepared.BOC)
		if sendErr == nil || !RateLimited(sendErr) || attempt == options.MaxResends {
			break
		}
		if err := options.Sleep(ctx, options.ResendDelay()); err != nil {
			sendErr = err
			break
		}
	}
	if sendErr != nil {
		return Result{}, &BroadcastError{Hash: prepared.Hash, Cause: sendErr}
	}

	for range options.ConfirmAttempts {
		if err := options.Sleep(ctx, options.ConfirmInterval); err != nil {
			break
		}
		account, err := api.Account(ctx, w.Address())
		if err != nil {
			// A poll that fails says nothing about the payment; the next one
			// may answer.
			continue
		}
		if account.Seqno > prepared.Seqno && account.BalanceNano < prepared.BalanceNano {
			return Result{Hash: prepared.Hash, BOC: prepared.BOC, BalanceAfterNano: account.BalanceNano}, nil
		}
	}
	return Result{}, &BroadcastError{
		Hash: prepared.Hash, Cause: errors.New("confirmation timed out"), Unconfirmed: true,
	}
}

// Result is a confirmed payment.
type Result struct {
	Hash             string
	BOC              string
	BalanceAfterNano int64
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
