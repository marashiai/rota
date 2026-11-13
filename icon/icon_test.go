package icon

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func decode(t *testing.T, b []byte) *image.NRGBA {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	n := image.NewNRGBA(img.Bounds()) // an opaque one decodes as RGBA
	draw.Draw(n, n.Bounds(), img, image.Point{}, draw.Src)
	return n
}

// Each style is what it is used for: the app icon a tile within a clear margin; the pages' tile
// to its edges with its corners clear; a phone's to its edges everywhere, which the phone rounds;
// the menu bar's only black, which macOS colours, its envelope's paper white on the others.
func TestTheIconInEachStyle(t *testing.T) {
	p := filepath.Join(t.TempDir(), "icon.png")
	if err := Write(p, 128); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	app := decode(t, b)
	if _, _, _, a := app.At(4, 4).RGBA(); a != 0 {
		t.Fatal("the app icon's margin is not clear")
	}
	if _, _, _, a := app.At(64, 20).RGBA(); a != 0xffff {
		t.Fatal("the app icon's tile is not opaque")
	}
	if r, g, bl, _ := app.At(64, 92).RGBA(); r>>8 < 230 || g>>8 < 230 || bl>>8 < 230 {
		t.Fatal("the envelope's front is not paper white")
	}
	web := decode(t, PNG(64, "web"))
	if _, _, _, a := web.At(0, 0).RGBA(); a != 0 {
		t.Fatal("the pages' icon has its corners filled")
	}
	if _, _, _, a := web.At(32, 2).RGBA(); a != 0xffff {
		t.Fatal("the pages' icon does not reach its edges")
	}
	full := decode(t, PNG(64, "full"))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if _, _, _, a := full.At(x, y).RGBA(); a != 0xffff {
				t.Fatalf("a phone's icon is not opaque at %d,%d: the phone would show what is behind it", x, y)
			}
		}
	}
	glyph, inked := decode(t, PNG(32, "template")), 0
	for i := 0; i < len(glyph.Pix); i += 4 {
		if glyph.Pix[i] != 0 || glyph.Pix[i+1] != 0 || glyph.Pix[i+2] != 0 {
			t.Fatal("the menu bar's glyph has colour: macOS colours it")
		}
		if glyph.Pix[i+3] > 128 {
			inked++
		}
	}
	if inked < 100 || inked > 600 {
		t.Fatalf("the menu bar's glyph inks %d of 1024 pixels", inked)
	}
}
