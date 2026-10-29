// Command genicon renders the application icon used by the macOS bundle.
//
// It is invoked by scripts/build-app.sh and writes a 1024x1024 PNG that
// iconutil turns into FakeWebCamera.icns.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const (
	size   = 1024
	scale  = 3 // supersampling factor for anti-aliasing
	canvas = size * scale
	margin = 92 * scale
	radius = 190 * scale
)

func main() {
	out := flag.String("out", "icon.png", "output PNG path")
	flag.Parse()

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	big := image.NewNRGBA(image.Rect(0, 0, canvas, canvas))

	bgTop := color.NRGBA{R: 0x4f, G: 0xe6, B: 0xa8, A: 0xff}
	bgBottom := color.NRGBA{R: 0x12, G: 0x7a, B: 0x59, A: 0xff}
	body := color.NRGBA{R: 0x0b, G: 0x14, B: 0x1b, A: 0xff}
	rim := color.NRGBA{R: 0xea, G: 0xff, B: 0xf6, A: 0xff}
	lens := color.NRGBA{R: 0x2b, G: 0xd6, B: 0x92, A: 0xff}
	glow := color.NRGBA{R: 0x9f, G: 0xff, B: 0xd8, A: 0xff}
	white := color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}

	// Rounded square background with a vertical gradient.
	for y := 0; y < canvas; y++ {
		t := clamp01(float64(y-margin) / float64(canvas-2*margin))
		c := blend(bgTop, bgBottom, t)
		for x := 0; x < canvas; x++ {
			if insideRoundRect(x, y, margin, margin, canvas-margin, canvas-margin, radius) {
				big.SetNRGBA(x, y, c)
			}
		}
	}

	// Camera body, viewfinder bump and side lens barrel.
	fillRoundRect(big, 236*scale, 356*scale, 788*scale, 700*scale, 56*scale, body)
	fillRoundRect(big, 372*scale, 296*scale, 612*scale, 380*scale, 34*scale, body)
	fillRoundRect(big, 788*scale, 470*scale, 856*scale, 600*scale, 26*scale, body)

	// Inner rim.
	fillRoundRect(big, 316*scale, 436*scale, 708*scale, 620*scale, 44*scale, rim)

	// Lens.
	cx, cy := float64(512*scale), float64(528*scale)
	r := float64(112 * scale)
	fillCircle(big, cx, cy, r, body)
	fillCircle(big, cx, cy, r*0.72, lens)
	fillCircle(big, cx-14*scale, cy-16*scale, r*0.34, glow)
	fillCircle(big, cx-22*scale, cy-26*scale, r*0.16, white)

	// Record LED.
	fillCircle(big, float64(700*scale), float64(392*scale), float64(20*scale), lens)

	// Downsample with box filtering.
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a, n uint64
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					c := big.NRGBAAt(x*scale+dx, y*scale+dy)
					r += uint64(c.R) * uint64(c.A)
					g += uint64(c.G) * uint64(c.A)
					b += uint64(c.B) * uint64(c.A)
					a += uint64(c.A)
					n++
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r / a),
				G: uint8(g / a),
				B: uint8(b / a),
				A: uint8(a / n),
			})
		}
	}

	if dir := filepath.Dir(*out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fail(err)
		}
	}
	f, err := os.Create(*out)
	if err != nil {
		fail(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "genicon: %v\n", err)
	os.Exit(1)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func blend(a, b color.NRGBA, t float64) color.NRGBA {
	mix := func(x, y uint8) uint8 {
		return uint8(float64(x)*(1-t) + float64(y)*t)
	}
	return color.NRGBA{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: 0xff}
}

func insideRoundRect(x, y, x0, y0, x1, y1, r int) bool {
	if x < x0 || x >= x1 || y < y0 || y >= y1 {
		return false
	}
	fx, fy := float64(x)+0.5, float64(y)+0.5
	cx := fx
	if fx < float64(x0+r) {
		cx = float64(x0 + r)
	} else if fx > float64(x1-r) {
		cx = float64(x1 - r)
	}
	cy := fy
	if fy < float64(y0+r) {
		cy = float64(y0 + r)
	} else if fy > float64(y1-r) {
		cy = float64(y1 - r)
	}
	return math.Hypot(fx-cx, fy-cy) <= float64(r)
}

func fillRoundRect(img *image.NRGBA, x0, y0, x1, y1, r int, c color.NRGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if insideRoundRect(x, y, x0, y0, x1, y1, r) {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func fillCircle(img *image.NRGBA, cx, cy, r float64, c color.NRGBA) {
	for y := int(cy-r) - 1; y <= int(cy+r)+1; y++ {
		for x := int(cx-r) - 1; x <= int(cx+r)+1; x++ {
			if math.Hypot(float64(x)-cx, float64(y)-cy) <= r {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}
