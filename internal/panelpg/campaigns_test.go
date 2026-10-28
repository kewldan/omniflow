package panelpg

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/notice"
)

// A campaign body is sent with parse_mode HTML, and Telegram refuses the whole
// message for markup it cannot parse. "Скидка <50%" used to be saved and then
// fail for every recipient; it is refused when the operator saves it, with the
// markup rule the reworded notices already follow. The check runs before the
// database is touched, which is why a Service with no pool can answer it.
func TestSaveTemplateRefusesMarkupTelegramWouldReject(t *testing.T) {
	t.Parallel()
	service := &Service{}
	for _, body := range []string{"Скидка <50%", "<div>Hello</div>", "<b>unclosed", `<a href="http://x.test">x</a>`} {
		_, err := service.SaveTemplate(context.Background(), MessageTemplate{
			Code: "sale", Class: "marketing", BodyEN: "Fine", BodyRU: body, Variables: []string{},
		}, Actor{})
		if !errors.Is(err, ErrValidaton) || !errors.Is(err, notice.ErrUnsupportedMarkup) {
			t.Fatalf("SaveTemplate(%q) = %v, want a markup validation error", body, err)
		}
	}
}
