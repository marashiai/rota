// Package icon draws the rota mark, for the app, the menu bar and the web pages.
package icon

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"os"
)

// The rota mark: an open envelope with the team in it, three agents in three colours, for a
// mail client of a team of agents. In the menu bar, the envelope's front with the three above it.
// Drawn with signed distances, so it is crisp at any size, with no asset files and no
// dependencies.

type pt struct{ x, y float64 }

func sub(a, b pt) pt                { return pt{a.x - b.x, a.y - b.y} }
func dot(a, b pt) float64           { return a.x*b.x + a.y*b.y }
func clamp(x, a, b float64) float64 { return math.Max(a, math.Min(b, x)) }

func circle(p, c pt, r float64) float64 { return math.Hypot(p.x-c.x, p.y-c.y) - r }

// polygon is the signed distance to a simple polygon: negative inside.
func polygon(p pt, v []pt) float64 {
	d, s := dot(sub(p, v[0]), sub(p, v[0])), 1.0
	for i, j := 0, len(v)-1; i < len(v); j, i = i, i+1 {
		e, w := sub(v[j], v[i]), sub(p, v[i])
		t := clamp(dot(w, e)/dot(e, e), 0, 1)
		b := sub(w, pt{e.x * t, e.y * t})
		d = math.Min(d, dot(b, b))
		c1, c2, c3 := p.y >= v[i].y, p.y < v[j].y, e.x*w.y > e.y*w.x
		if c1 && c2 && c3 || !c1 && !c2 && !c3 {
			s = -s
		}
	}
	return s * math.Sqrt(d)
}

// squircle is the distance, estimated, to the macOS icon's shape: a superellipse of half width
// half around the middle.
func squircle(p pt, half, n float64) float64 {
	x, y := math.Abs(p.x-0.5)/half, math.Abs(p.y-0.5)/half
	if x < 1e-9 && y < 1e-9 {
		return -half
	}
	return (math.Pow(math.Pow(x, n)+math.Pow(y, n), 1/n) - 1) * half
}

// The envelope and the team, in the tile's own coordinates: 0 to 1 across it.
var (
	left, right, bottom = 0.16, 0.84, 0.82
	back                = []pt{{left, 0.43}, {0.5, 0.15}, {right, 0.43}, {right, bottom}, {left, bottom}}  // with the flap open
	front               = []pt{{left, 0.52}, {0.5, 0.675}, {right, 0.52}, {right, bottom}, {left, bottom}} // its V the opening
	agents              = []struct {
		at  pt
		col color
	}{{pt{0.335, 0.47}, rgb(0x1f, 0xc8, 0xa9)}, {pt{0.665, 0.47}, rgb(0xff, 0x7a, 0x59)}, {pt{0.5, 0.405}, rgb(0xff, 0xc2, 0x3d)}}
)

const (
	corner = 0.022 // how round the envelope's corners are
	agentR = 0.098
	ringW  = 0.022 // between two agents
)

type color struct{ r, g, b, a float64 }

func rgb(r, g, b uint8) color { return color{float64(r) / 255, float64(g) / 255, float64(b) / 255, 1} }

func mix(a, b color, t float64) color {
	t = clamp(t, 0, 1)
	return color{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t, a.a + (b.a-a.a)*t}
}

// over paints c over dst, c's alpha times k.
func over(dst, c color, k float64) color {
	a := c.a * k
	oa := a + dst.a*(1-a)
	if oa == 0 {
		return color{}
	}
	f := func(x, y float64) float64 { return (x*a + y*dst.a*(1-a)) / oa }
	return color{f(c.r, dst.r), f(c.g, dst.g), f(c.b, dst.b), oa}
}

// cover is how much of a pixel px wide a shape covers at distance d: its edge anti-aliased.
func cover(d, px float64) float64 { return clamp(0.5-d/px, 0, 1) }

// soft is how dark a shadow is at distance d from its shape, fading over blur.
func soft(d, blur float64) float64 {
	t := clamp((d+blur)/(2*blur), 0, 1)
	return 1 - t*t*(3-2*t)
}

// tile is the tile's colour at q: violet, lighter at the top.
func tile(q pt) color {
	c := mix(rgb(0x7c, 0x7e, 0xff), rgb(0x3a, 0x2e, 0xc0), q.y)
	glow := clamp(1-math.Hypot(q.x-0.5, q.y)/0.75, 0, 1)
	return mix(c, rgb(0xa9, 0xaa, 0xff), 0.35*glow*glow)
}

// envelope is the open envelope with the team in it, at q in the tile's coordinates, over c; px
// is a pixel's width there.
func envelope(c color, q pt, px float64) color {
	c = over(c, color{0.10, 0.04, 0.35, 0.30}, soft(polygon(sub(q, pt{0, 0.028}), back)-corner, 0.045))
	inside := mix(rgb(0xe9, 0xeb, 0xff), rgb(0xc9, 0xcd, 0xff), (q.y-0.15)/(bottom-0.15))
	c = over(c, inside, cover(polygon(q, back)-corner, px))
	for _, a := range agents {
		c = over(c, inside, cover(circle(q, a.at, agentR+ringW), px))
		shade := mix(a.col, color{0, 0, 0, 1}, 0.12)
		c = over(c, mix(a.col, shade, (q.y-a.at.y+agentR)/(2*agentR)), cover(circle(q, a.at, agentR), px))
	}
	c = over(c, color{0.12, 0.06, 0.40, 0.22}, soft(polygon(sub(q, pt{0, -0.018}), front)-corner, 0.03)) // the front's edge on the team
	paper := mix(rgb(0xff, 0xff, 0xff), rgb(0xee, 0xf0, 0xff), (q.y-0.52)/(bottom-0.52))
	return over(c, paper, cover(polygon(q, front)-corner, px))
}

// glyph is the menu bar's mark at q, in the tile's coordinates: the envelope's front, its V the
// opening, with the team above it.
func glyph(q pt, px float64) float64 {
	const w = 0.09 // the line
	d := math.Abs(polygon(q, front)-corner) - w/2
	for _, a := range []pt{{0.29, 0.30}, {0.5, 0.25}, {0.71, 0.30}} {
		d = math.Min(d, circle(q, a, 0.08))
	}
	return cover(d, px)
}

// at is the colour of the icon at p, 0 to 1 across the image, in a style:
//   - app: the macOS app icon: the tile within its margin, with its shadow;
//   - web: the tile to the edges, for a browser's tab and the pages themselves;
//   - full: the square to the edges, which a phone's home screen rounds itself;
//   - template: the menu bar's mark in black, which macOS colours.
func at(p pt, px float64, style string) color {
	switch style {
	case "template":
		q := pt{0.5 + (p.x-0.5)*0.86, 0.528 + (p.y-0.5)*0.86}
		return color{0, 0, 0, glyph(q, px*0.86)}
	case "full":
		q := pt{0.5 + (p.x-0.5)*1.08, 0.5 + (p.y-0.5)*1.08} // with room for the phone's mask
		return envelope(tile(p), q, px*1.08)
	case "web":
		d := squircle(p, 0.5, 4.2)
		k := cover(d, px)
		e := envelope(color{}, p, px)
		return over(over(color{}, tile(p), k), color{e.r, e.g, e.b, 1}, e.a*k)
	}
	const half = 0.4023 // 824 of 1024, as macOS's icons
	c := over(color{}, color{0, 0, 0, 0.30}, soft(squircle(sub(p, pt{0, 0.014}), half, 5), 0.03))
	q := pt{0.5 + (p.x-0.5)/(2*half), 0.5 + (p.y-0.5)/(2*half)}
	k := cover(squircle(p, half, 5), px)
	c = over(c, tile(q), k)
	if k > 0 {
		e := envelope(color{}, q, px/(2*half))
		c = over(c, color{e.r, e.g, e.b, 1}, e.a*k)
	}
	return c
}

// PNG is the mark at size pixels, in a style: app, web, full or template (see at).
func PNG(size int, style string) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	px := 1 / float64(size)
	b := func(v float64) uint8 { return uint8(clamp(v, 0, 1)*255 + 0.5) }
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			c := at(pt{(float64(x) + 0.5) * px, (float64(y) + 0.5) * px}, px, style)
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = b(c.r), b(c.g), b(c.b), b(c.a)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// Write saves the app icon at size pixels.
func Write(path string, size int) error {
	return os.WriteFile(path, PNG(size, "app"), 0o644)
}
