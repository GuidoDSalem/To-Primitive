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

// canvas draws shapes at an output size whose longest side is `size`,
// the same way primitive's Model does internally.
type canvas struct {
	dc    *gg.Context
	scale float64
}

func newCanvas(w, h, size int, bg primitive.Color) *canvas {
	scale := float64(size) / float64(max(w, h))
	ow := max(1, int(math.Round(float64(w)*scale)))
	oh := max(1, int(math.Round(float64(h)*scale)))
	dc := gg.NewContext(ow, oh)
	dc.Scale(scale, scale)
	dc.Translate(0.5, 0.5)
	dc.SetColor(bg.NRGBA())
	dc.Clear()
	return &canvas{dc, scale}
}

func (c *canvas) draw(rec shapeRec) {
	c.dc.SetRGBA255(rec.Color.R, rec.Color.G, rec.Color.B, rec.Color.A)
	rec.Shape.Draw(c.dc, c.scale)
}

func renderPNG(w, h, size int, bg primitive.Color, shapes []shapeRec) image.Image {
	c := newCanvas(w, h, size, bg)
	for _, rec := range shapes {
		c.draw(rec)
	}
	return c.dc.Image()
}

func renderSVG(w, h, size int, bg primitive.Color, shapes []shapeRec) string {
	scale := float64(size) / float64(max(w, h))
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" version="1.1" width="%d" height="%d" viewBox="0 0 %d %d">`+"\n",
		int(math.Round(float64(w)*scale)), int(math.Round(float64(h)*scale)), w, h)
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
	c := newCanvas(w, h, size, bg)
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
