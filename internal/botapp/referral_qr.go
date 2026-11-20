package botapp

import (
	"bytes"
	"context"
	"errors"
	"html"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"rsc.io/qr"
)

// referralQRPNG renders an invitation link as a QR code. It is generated here
// rather than by an outside QR service, so the link — which identifies the
// customer who shares it — never leaves the installation.
func referralQRPNG(link string) ([]byte, error) {
	code, err := qr.Encode(link, qr.M)
	if err != nil {
		return nil, err
	}
	// Ten pixels a module keeps the code readable from a phone held up to
	// another phone's screen.
	code.Scale = 10
	return code.PNG(), nil
}

// sendReferralQR sends the customer's invitation link as a QR code photo, with
// the link itself in the caption for anyone who cannot scan.
func (app *App) sendReferralQR(ctx context.Context, client *telegram.Bot, session commerceContext, chatID int64) error {
	summary, err := app.customers.ReferralSummary(ctx, session.Customer.ID)
	if err != nil {
		return err
	}
	link := referralURL(app.identity.get(ctx), summary.Code)
	if !safeURL(link) {
		return errors.New("referral link is not available")
	}
	png, err := referralQRPNG(link)
	if err != nil {
		return err
	}
	_, err = client.SendPhoto(ctx, &telegram.SendPhotoParams{
		ChatID:    chatID,
		Photo:     &models.InputFileUpload{Filename: "invite.png", Data: bytes.NewReader(png)},
		Caption:   text(session.Locale, "referral.qrCaption", html.EscapeString(link)),
		ParseMode: models.ParseModeHTML,
	})
	return err
}
