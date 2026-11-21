package botapp

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"rsc.io/qr"
)

// The drawing of a QR code the bot sends: the same rounded style the web
// panel draws — rounded finder eyes and softly rounded modules on white — so
// a code looks the same wherever a customer meets it. The modules sit where
// the encoder put them, which is what keeps it scannable.
const (
	qrModulePixels = 20 // a module's side; 20 keeps the curves smooth in a photo
	qrMargin       = 2  // the quiet zone, in modules
)

// roundedQRPNG draws an encoded QR code as a PNG.
func roundedQRPNG(code *qr.Code) ([]byte, error) {
	size := code.Size
	side := (size + 2*qrMargin) * qrModulePixels
	canvas := image.NewGray(image.Rect(0, 0, side, side))
	for index := range canvas.Pix {
		canvas.Pix[index] = 0xff
	}
	finder := func(x, y int) bool {
		return (x < 7 && y < 7) || (x >= size-7 && y < 7) || (x < 7 && y >= size-7)
	}
	at := func(module float64) int { return int((module + qrMargin) * qrModulePixels) }
	for y := range size {
		for x := range size {
			if code.Black(x, y) && !finder(x, y) {
				fillRounded(canvas, at(float64(x)+0.05), at(float64(y)+0.05), qrModulePixels*9/10, qrModulePixels*9/10,
					qrModulePixels*3/10, color.Gray{})
			}
		}
	}
	for _, origin := range [][2]int{{0, 0}, {size - 7, 0}, {0, size - 7}} {
		x, y := float64(origin[0]), float64(origin[1])
		fillRounded(canvas, at(x), at(y), 7*qrModulePixels, 7*qrModulePixels, qrModulePixels*2, color.Gray{})
		fillRounded(canvas, at(x+1), at(y+1), 5*qrModulePixels, 5*qrModulePixels, qrModulePixels*6/5, color.Gray{Y: 0xff})
		fillRounded(canvas, at(x+2), at(y+2), 3*qrModulePixels, 3*qrModulePixels, qrModulePixels*9/10, color.Gray{})
	}
	var out bytes.Buffer
	if err := png.Encode(&out, canvas); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// fillRounded fills a rectangle whose corners are quarter circles of radius r.
func fillRounded(canvas *image.Gray, x0, y0, width, height, radius int, fill color.Gray) {
	for y := y0; y < y0+height; y++ {
		for x := x0; x < x0+width; x++ {
			cx, cy := x, y
			switch {
			case x < x0+radius:
				cx = x0 + radius
			case x >= x0+width-radius:
				cx = x0 + width - radius - 1
			}
			switch {
			case y < y0+radius:
				cy = y0 + radius
			case y >= y0+height-radius:
				cy = y0 + height - radius - 1
			}
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= radius*radius {
				canvas.SetGray(x, y, fill)
			}
		}
	}
}
