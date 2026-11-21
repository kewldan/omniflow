package botapp

import (
	"context"
	"testing"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/omniflow/omniflow/internal/platform"
)

func TestObservabilityMiddlewareOutcomes(t *testing.T) {
	metrics := platform.NewMetrics("bot")
	middleware := ObservabilityMiddleware(metrics)
	update := &models.Update{Message: &models.Message{}}

	middleware(func(context.Context, *telegram.Bot, *models.Update) {})(context.Background(), nil, update)
	middleware(func(ctx context.Context, _ *telegram.Bot, _ *models.Update) {
		markUpdateFailed(ctx)
	})(context.Background(), nil, update)
	middleware(func(context.Context, *telegram.Bot, *models.Update) {
		panic("handler bug")
	})(context.Background(), nil, update)

	for _, outcome := range []string{"handled", "failed", "panicked"} {
		if got := testutil.ToFloat64(metrics.TelegramUpdates.WithLabelValues("message", outcome)); got != 1 {
			t.Errorf("%s = %v, want 1", outcome, got)
		}
	}
}

func TestMarkUpdateFailedOutsideAnUpdateIsANoOp(t *testing.T) {
	markUpdateFailed(context.Background())
}
