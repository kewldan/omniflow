package botapp

import (
	"bytes"
	"image/png"
	"testing"
)

func TestReferralQRIsAReadablePNG(t *testing.T) {
	t.Parallel()
	data, err := referralQRPNG("https://t.me/omniflow_demo_bot?start=ref_ALEXEY2026")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	image, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the QR code is not a PNG: %v", err)
	}
	if size := image.Bounds().Dx(); size < 200 {
		t.Fatalf("the QR code is %dpx wide, too small to scan from a screen", size)
	}
}

func TestReferralScreenOffersTheQRCode(t *testing.T) {
	t.Parallel()
	view := referralView(LocaleRussian, "omniflow_demo_bot", "ALEXEY2026", 7)
	found := false
	for _, data := range callbackData(view) {
		if data == actionPrefix+"ref-qr" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the invitation screen must offer the QR code: %v", callbackData(view))
	}
}
