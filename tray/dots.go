package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The status dots before each agent: macOS's own colours for working, asks you, failed and done,
// grey while it waits, an empty grey ring while it is off.
var dotColors = map[string]color.NRGBA{
	"working": {0x34, 0xc7, 0x59, 0xff},
	"needs":   {0xff, 0x95, 0x00, 0xff},
	"failed":  {0xff, 0x3b, 0x30, 0xff},
	"done":    {0x0a, 0x84, 0xff, 0xff},
	"idle":    {0x8e, 0x8e, 0x93, 0xff},
	"off":     {0x8e, 0x8e, 0x93, 0xff},
}

var dots = map[string][]byte{} // drawn once each; read and written under the menu's lock

// dot is the PNG of a status dot: 32 pixels square, which the menu shows at 16 points.
func dot(look string) []byte {
	if b, ok := dots[look]; ok {
		return b
	}
	c, ok := dotColors[look]
	if !ok {
		c = dotColors["idle"]
	}
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	const centre, radius, ring = 16.0, 7.0, 2.4
	clamp := func(v float64) float64 { return math.Max(0, math.Min(1, v)) }
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			d := math.Hypot(float64(x)+0.5-centre, float64(y)+0.5-centre)
			a := clamp(radius - d + 0.5) // the disc, its edge smoothed over a pixel
			if look == "off" {
				a = math.Min(a, clamp(d-(radius-ring)+0.5)) // only its rim
			}
			if a > 0 {
				img.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, uint8(a*float64(c.A) + 0.5)})
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	dots[look] = buf.Bytes()
	return dots[look]
}
