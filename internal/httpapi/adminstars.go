package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/goods"
	"github.com/omniflow/omniflow/internal/goods/fragmentdirect"
	"github.com/omniflow/omniflow/internal/goodsdelivery"
	"github.com/omniflow/omniflow/internal/tonrate"
)

// StarsShopOptions is what the panel needs to describe the direct Fragment
// provider. None of it is a secret: the cookies, the mnemonic, and the API key
// stay inside the registry and are never serialised.
type StarsShopOptions struct {
	Registry       *goodsdelivery.Registry
	Currency       string
	WalletVersion  string
	TONAPIProvider string
	TONAPIRPS      float64
	WalletLowStars int
	VPNOffer       bool
}

// starsStatus answers "can this installation sell Stars directly, and how".
func (handlers *AdminHandlers) starsStatus(writer http.ResponseWriter, request *http.Request) {
	options := handlers.stars
	status := options.Registry.FragmentDirectStatus()
	enabled := false
	if providers, err := handlers.operations.ListGoodsProviders(request.Context()); err == nil {
		for _, provider := range providers {
			if provider.Slug == fragmentdirect.Slug {
				enabled = provider.Enabled
			}
		}
	}
	payload := map[string]any{
		"provider":       fragmentdirect.Slug,
		"enabled":        enabled,
		"configured":     status.Configured,
		"dryRun":         status.DryRun,
		"walletVersion":  options.WalletVersion,
		"tonApiProvider": options.TONAPIProvider,
		"tonApiRps":      options.TONAPIRPS,
		"walletLowStars": options.WalletLowStars,
		"vpnOffer":       options.VPNOffer,
		"currency":       options.Currency,
	}
	if status.Problem != "" {
		// The adapter's refusal names the setting that is wrong — "cookies are
		// missing stel_token" — and never a value.
		payload["problem"] = status.Problem
	}
	if rate := handlers.starsRate(request.Context()); rate != nil {
		payload["rate"] = map[string]any{
			"minorPerTon": rate.MinorPerTON, "source": rate.Source, "currency": rate.Currency,
		}
	}
	writeJSON(writer, http.StatusOK, payload)
}

// starsStats reports Stars sales for the last N days.
func (handlers *AdminHandlers) starsStats(writer http.ResponseWriter, request *http.Request) {
	days := 30
	if raw := query(request, "days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeProblem(writer, request, http.StatusBadRequest, "invalid_days", "days must be a whole number")
			return
		}
		days = parsed
	}
	sales, err := handlers.operations.GoodsSalesStats(
		request.Context(), days, handlers.stars.Currency, handlers.starsRate(request.Context()))
	handlers.respond(writer, request, sales, err)
}

// starsWallet reads the paying wallet: its address, its balance, and what 100
// Stars cost on Fragment right now.
//
// It answers 503 rather than an empty card when the provider is not
// configured, so the panel says why instead of showing a zero balance an
// operator might believe.
func (handlers *AdminHandlers) starsWallet(writer http.ResponseWriter, request *http.Request) {
	provider, err := handlers.stars.Registry.FragmentDirect()
	if err != nil {
		writeProblem(writer, request, http.StatusServiceUnavailable,
			"fragment_not_configured", "The direct Fragment provider is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	wallet, err := provider.Wallet(ctx)
	if err != nil {
		if errors.Is(err, goods.ErrUnsupported) {
			writeProblem(writer, request, http.StatusServiceUnavailable,
				"wallet_not_configured", "No wallet is configured; the provider runs in dry-run mode")
			return
		}
		handlers.logger.Warn("TON wallet read failed", "provider", fragmentdirect.Slug, "error", err)
		writeProblem(writer, request, http.StatusBadGateway,
			"wallet_unavailable", "The TON API did not answer; try again shortly")
		return
	}
	payload := map[string]any{
		"address": wallet.Address, "version": wallet.Version, "asset": wallet.Asset,
		"balanceNano": wallet.BalanceNano,
		"explorerUrl": "https://tonviewer.com/" + wallet.Address,
	}
	if price, priceErr := provider.StarsPriceNano(ctx, 100); priceErr == nil {
		payload["price100Nano"] = price
		if price > 0 {
			payload["starsCovered"] = wallet.BalanceNano * 100 / price
		}
	}
	if rate := handlers.starsRate(ctx); rate != nil {
		payload["balanceMinor"] = rate.MinorFromNano(wallet.BalanceNano)
		payload["rateMinor"] = rate.MinorPerTON
		payload["currency"] = rate.Currency
	}
	writeJSON(writer, http.StatusOK, payload)
}

func (handlers *AdminHandlers) starsRate(ctx context.Context) *tonrate.Rate {
	rates := handlers.stars.Registry.Rates()
	if rates == nil {
		return nil
	}
	rateCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	rate, err := rates.Current(rateCtx)
	if err != nil {
		return nil
	}
	return &rate
}

// retryGoodsDelivery sends a parked delivery back to the worker. The operator
// asserts the payment did not leave; the reason header records why they
// believe so.
func (handlers *AdminHandlers) retryGoodsDelivery(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.RetryGoodsDelivery(
		request.Context(), chi.URLParam(request, "orderID"), actorFrom(request))
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// refundGoodsOrder returns the customer's money for a delivery that will not
// happen.
func (handlers *AdminHandlers) refundGoodsOrder(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.RefundGoodsOrder(
		request.Context(), chi.URLParam(request, "orderID"), actorFrom(request))
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// completeGoodsDelivery closes a parked delivery as delivered.
func (handlers *AdminHandlers) completeGoodsDelivery(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		TxHash string `json:"txHash"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.CompleteGoodsDelivery(
		request.Context(), chi.URLParam(request, "orderID"), body.TxHash, actorFrom(request))
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
