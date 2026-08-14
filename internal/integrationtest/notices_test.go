//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/notice"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// Transactional notice wording, against a real database.
//
// The property that matters most is the one a Go test cannot assert: that an
// override is an exception and its absence is the shipped wording. Everything
// downstream depends on it — reverting has to be a delete, an installation that
// has never opened the screen has to have no rows, and an upgrade has to be
// able to improve a default nobody has touched.

func TestAnOverrideIsAnExceptionAndRevertingRemovesIt(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "wording@example.test")

	// Nothing is overridden to begin with, and every notice still reports the
	// wording a customer would receive.
	notices, err := service.Notices(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(notices) == 0 {
		t.Fatal("no notices are overridable")
	}
	for _, item := range notices {
		if len(item.Overrides) != 0 {
			t.Fatalf("%s has an override on a fresh installation", item.Code)
		}
		if item.Default["en"] == "" || item.Default["ru"] == "" {
			t.Fatalf("%s is missing a shipped default", item.Code)
		}
	}

	body := "Your access ends in {days} day(s). <b>Renew now.</b>"
	if _, err := service.SaveNotice(ctx, "expiry", "en", body, actor); err != nil {
		t.Fatalf("save: %v", err)
	}

	notices, err = service.Notices(ctx)
	if err != nil {
		t.Fatalf("list after save: %v", err)
	}
	expiry := findNotice(t, notices, "expiry")
	if expiry.Overrides["en"].Body != body {
		t.Fatalf("the override reads %q", expiry.Overrides["en"].Body)
	}
	// The other locale is untouched. Rewriting the English warning decides
	// nothing about the Russian one.
	if _, overridden := expiry.Overrides["ru"]; overridden {
		t.Fatal("saving English created a Russian override")
	}

	if err := service.RevertNotice(ctx, "expiry", "en", actor); err != nil {
		t.Fatalf("revert: %v", err)
	}
	var rows int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM notice_overrides WHERE code = 'expiry'`,
	).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		// A revert that wrote the current default back would leave a row, and
		// the installation would then be frozen on today's wording forever.
		t.Fatalf("reverting left %d rows behind", rows)
	}

	// And reverting something that was never overridden is not an error.
	if err := service.RevertNotice(ctx, "traffic", "ru", actor); err != nil {
		t.Fatalf("reverting an untouched notice failed: %v", err)
	}
}

func TestAnUnusableBodyNeverReachesTheTable(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "bad-wording@example.test")

	for _, refused := range []struct {
		name string
		body string
	}{
		{"a value the notice does not carry", "Hello {name}, you have {days} left."},
		{"markup Telegram refuses", "<div>{days} days</div>"},
		{"a link that is not https", `<a href="javascript:alert(1)">{days}</a>`},
		{"nothing at all", "   "},
		{"more than the message limit", strings.Repeat("a", 2100)},
	} {
		if _, err := service.SaveNotice(ctx, "expiry", "en", refused.body, actor); !errors.Is(
			err, panelpg.ErrValidaton,
		) {
			t.Fatalf("%s was accepted: %v", refused.name, err)
		}
	}

	var rows int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM notice_overrides`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Fatalf("%d refused bodies were stored anyway", rows)
	}
}

// The preview and the test send have to agree, because an operator who reviews
// one and ships the other is reviewing nothing.
func TestThePreviewAndTheTestSendRenderTheSameThing(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "preview@example.test")

	body := "Renewing <b>{plan}</b> — {days} days left, valid to {until}."
	preview, err := service.PreviewNotice(ctx, "renewal", "en", body)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if strings.ContainsAny(preview.Rendered, "{}") {
		t.Fatalf("a placeholder survived the preview: %q", preview.Rendered)
	}
	definition, _ := notice.Lookup("renewal")
	for _, variable := range definition.Variables {
		if !strings.Contains(preview.Rendered, variable.Sample) {
			t.Fatalf("the preview omits the sample for {%s}: %q", variable.Name, preview.Rendered)
		}
	}

	if _, err := service.SendNoticeTest(ctx, "renewal", "en", body, actor); err != nil {
		t.Fatalf("test send: %v", err)
	}
	var queued string
	if err := harness.pool.QueryRow(ctx,
		`SELECT body FROM notice_test_sends WHERE code = 'renewal' AND status = 'pending'`,
	).Scan(&queued); err != nil {
		t.Fatalf("read queued test: %v", err)
	}
	if queued != preview.Rendered {
		// The body is rendered and stored when the button is pressed rather than
		// resolved at delivery, precisely so these cannot diverge.
		t.Fatalf("the queued test reads %q, the preview read %q", queued, preview.Rendered)
	}

	// An empty editor previews the shipped wording, which is what a customer
	// would receive if nothing were saved.
	shipped, err := service.PreviewNotice(ctx, "renewal", "ru", "")
	if err != nil {
		t.Fatalf("preview of the default: %v", err)
	}
	if !strings.Contains(shipped.Rendered, "Pro") {
		t.Fatalf("the shipped Russian renewal notice rendered as %q", shipped.Rendered)
	}
}

// A test send never creates a customer delivery. Manufacturing an expiry
// warning against a real subscription to see how it reads would tell somebody
// their access is ending when it is not.
func TestANoticeTestNeverReachesACustomer(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "notest@example.test")

	if _, err := service.SendNoticeTest(ctx, "expiry", "en", "", actor); err != nil {
		t.Fatalf("test send: %v", err)
	}
	var deliveries int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM notification_deliveries`,
	).Scan(&deliveries); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if deliveries != 0 {
		t.Fatalf("a notice test created %d customer deliveries", deliveries)
	}

	tests, err := service.NoticeTests(ctx, "expiry", 10)
	if err != nil {
		t.Fatalf("list tests: %v", err)
	}
	if len(tests) != 1 || tests[0].Status != "pending" {
		t.Fatalf("the test list reads %+v", tests)
	}
}

func findNotice(t *testing.T, notices []panelpg.Notice, code string) panelpg.Notice {
	t.Helper()
	for _, item := range notices {
		if item.Code == code {
			return item
		}
	}
	t.Fatalf("%s is not in the notice list", code)
	return panelpg.Notice{}
}
