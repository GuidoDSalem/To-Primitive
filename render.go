package main

import (
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"io"
	"math"
	"strings"

	"github.com/fogleman/gg"
	"github.com/fogleman/primitive/primitive"
)

// canvas draws shapes (in working-size coordinates) onto an ow x oh image,
// the same way primitive's Model does internally.
type canvas struct {
	dc    *gg.Context
	scale float64
}

// fitSize scales w x h so the longest side is size.
func fitSize(w, h, size int) (int, int) {
	scale := float64(size) / float64(max(w, h))
	return max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
}

func newCanvas(w, h, ow, oh int, bg primitive.Color) *canvas {
	sx, sy := float64(ow)/float64(w), float64(oh)/float64(h)
	dc := gg.NewContext(ow, oh)
	dc.Scale(sx, sy)
	dc.Translate(0.5, 0.5)
	dc.SetColor(bg.NRGBA())
	dc.Clear()
	return &canvas{dc, sx}
}

func (c *canvas) draw(rec shapeRec) {
	c.dc.SetRGBA255(rec.Color.R, rec.Color.G, rec.Color.B, rec.Color.A)
	rec.Shape.Draw(c.dc, c.scale)
}

func renderImage(w, h, ow, oh int, bg primitive.Color, shapes []shapeRec) *image.RGBA {
	c := newCanvas(w, h, ow, oh, bg)
	for _, rec := range shapes {
		c.draw(rec)
	}
	return c.dc.Image().(*image.RGBA)
}

func renderSVG(w, h, size int, bg primitive.Color, shapes []shapeRec) string {
	ow, oh := fitSize(w, h, size)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" version="1.1" width="%d" height="%d" viewBox="0 0 %d %d">`+"\n",
		ow, oh, w, h)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%d" height="%d" fill="#%02x%02x%02x" />`+"\n", w, h, bg.R, bg.G, bg.B)
	b.WriteString(`<g transform="translate(0.5 0.5)">` + "\n")
	for _, rec := range shapes {
		b.WriteString(rec.SVG)
		b.WriteByte('\n')
	}
	b.WriteString("</g>\n</svg>\n")
	return b.String()
}

// writeGIF animates the shapes being added, using at most maxFrames frames.
func writeGIF(out io.Writer, w, h, size int, bg primitive.Color, shapes []shapeRec, maxFrames int) error {
	ow, oh := fitSize(w, h, size)
	c := newCanvas(w, h, ow, oh, bg)
	g := &gif.GIF{}
	addFrame := func(delay int) {
		src := c.dc.Image()
		dst := image.NewPaletted(src.Bounds(), palette.Plan9)
		draw.Draw(dst, dst.Rect, src, image.Point{}, draw.Src)
		g.Image = append(g.Image, dst)
		g.Delay = append(g.Delay, delay)
	}
	addFrame(10)
	step := max(1, int(math.Ceil(float64(len(shapes))/float64(maxFrames))))
	for i, rec := range shapes {
		c.draw(rec)
		if (i+1)%step == 0 || i == len(shapes)-1 {
			addFrame(6)
		}
	}
	g.Delay[len(g.Delay)-1] = 300
	return gif.EncodeAll(out, g)
}
