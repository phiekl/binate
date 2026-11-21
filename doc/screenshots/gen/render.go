// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"image"
	"image/color"
	"image/draw"

	"github.com/go-fonts/dejavu/dejavusansmono"
	"github.com/go-fonts/dejavu/dejavusansmonobold"
	"github.com/go-fonts/liberation/liberationmonobold"
	"github.com/go-fonts/liberation/liberationmonoregular"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// The look of the terminal: the Tango palette on a dark blue, bold text
// in bright colours, and hyperlinks in blue and underlined.
var (
	bgDefault = rgb(0x00002c)
	fgDefault = rgb(0xcbcbcb)
	linkColor = rgb(0x2563eb)
	ansi16    = [16]color.RGBA{
		rgb(0x2e3436), rgb(0xcc0000), rgb(0x4e9a06), rgb(0xc4a000),
		rgb(0x3465a4), rgb(0x75507b), rgb(0x06989a), rgb(0xd3d7cf),
		rgb(0x555753), rgb(0xef2929), rgb(0x8ae234), rgb(0xfce94f),
		rgb(0x729fcf), rgb(0xad7fa8), rgb(0x34e2e2), rgb(0xeeeeec),
	}
)

const (
	cellW, cellH = 14, 28
	padX, padY   = 2, 10
	fontSize     = 23.3
)

func rgb(v uint32) color.RGBA {
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 0xff}
}

// palette returns colour i of the 256-colour palette.
func palette(i uint8) color.RGBA {
	switch {
	case i < 16:
		return ansi16[i]
	case i < 232:
		i -= 16
		level := func(v uint8) uint8 {
			if v == 0 {
				return 0
			}
			return 55 + v*40
		}
		return color.RGBA{level(i / 36), level(i / 6 % 6), level(i % 6), 0xff}
	}
	g := 8 + (i-232)*10
	return color.RGBA{g, g, g, 0xff}
}

func resolve(c tcolor, def color.RGBA, bold bool) color.RGBA {
	switch c.kind {
	case colorIndex:
		if bold && c.idx < 8 {
			return palette(c.idx + 8)
		}
		return palette(c.idx)
	case colorRGB:
		return color.RGBA{c.r, c.g, c.b, 0xff}
	}
	return def
}

func blend(a, b color.RGBA, t float64) color.RGBA {
	m := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t + 0.5) }
	return color.RGBA{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B), 0xff}
}

// faces is a font with fallbacks for the characters it lacks.
type faces struct {
	fonts []*sfnt.Font
	faces []font.Face
}

func loadFaces(ttfs ...[]byte) (*faces, error) {
	fs := &faces{}
	for _, ttf := range ttfs {
		f, err := opentype.Parse(ttf)
		if err != nil {
			return nil, err
		}
		face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: fontSize, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return nil, err
		}
		fs.fonts = append(fs.fonts, f)
		fs.faces = append(fs.faces, face)
	}
	return fs, nil
}

func (fs *faces) pick(r rune) font.Face {
	var buf sfnt.Buffer
	for i, f := range fs.fonts {
		if g, err := f.GlyphIndex(&buf, r); err == nil && g != 0 {
			return fs.faces[i]
		}
	}
	return fs.faces[0]
}

// renderer draws screens with its fonts.
type renderer struct {
	regular, bold *faces
}

func newRenderer() (*renderer, error) {
	regular, err := loadFaces(liberationmonoregular.TTF, dejavusansmono.TTF)
	if err != nil {
		return nil, err
	}
	bold, err := loadFaces(liberationmonobold.TTF, dejavusansmonobold.TTF)
	if err != nil {
		return nil, err
	}
	return &renderer{regular: regular, bold: bold}, nil
}

func (rd *renderer) render(t *term) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, t.w*cellW+2*padX, t.h*cellH+2*padY))
	fill(img, img.Bounds(), bgDefault)
	m := rd.regular.faces[0].Metrics()
	asc, desc := m.Ascent.Round(), m.Descent.Round()
	base := (cellH-(asc+desc))/2 + asc

	for y := 0; y < t.h; y++ {
		for x := 0; x < t.w; x++ {
			c := t.cells[y*t.w+x]
			fg := resolve(c.a.fg, fgDefault, c.a.bold)
			bg := resolve(c.a.bg, bgDefault, false)
			if c.a.rev {
				fg, bg = bg, fg
			}
			if c.a.link {
				fg = linkColor
			}
			if c.a.dim {
				fg = blend(fg, bg, 0.5)
			}
			x0, y0 := padX+x*cellW, padY+y*cellH
			rect := image.Rect(x0, y0, x0+cellW, y0+cellH)
			fill(img, rect, bg)
			if c.a.under || c.a.link {
				fill(img, image.Rect(x0, y0+base+2, x0+cellW, y0+base+4), fg)
			}
			if c.r == ' ' || boxDraw(img, c.r, rect, fg) {
				continue
			}
			fs := rd.regular
			if c.a.bold {
				fs = rd.bold
			}
			d := font.Drawer{Dst: img, Src: image.NewUniform(fg), Face: fs.pick(c.r), Dot: fixed.P(x0, y0+base)}
			// A fallback font's glyphs may be narrower or wider than the cell.
			if adv, ok := d.Face.GlyphAdvance(c.r); ok {
				d.Dot.X += (fixed.I(cellW) - adv) / 2
			}
			d.DrawString(string(c.r))
		}
	}
	return img
}

func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
}

// boxDraw draws a light box-drawing character as lines meeting at the
// cell's centre, so that borders join across cells, and reports whether r
// is one.
func boxDraw(img *image.RGBA, r rune, rc image.Rectangle, c color.RGBA) bool {
	const (
		left = 1 << iota
		right
		up
		down
	)
	m := map[rune]int{
		'─': left | right, '│': up | down,
		'┌': right | down, '┐': left | down, '└': right | up, '┘': left | up,
		'╭': right | down, '╮': left | down, '╰': right | up, '╯': left | up,
		'├': up | down | right, '┤': up | down | left,
		'┬': left | right | down, '┴': left | right | up, '┼': left | right | up | down,
	}[r]
	if m == 0 {
		return false
	}
	cx, cy := rc.Min.X+rc.Dx()/2, rc.Min.Y+rc.Dy()/2
	if m&left != 0 {
		fill(img, image.Rect(rc.Min.X, cy, cx+1, cy+2), c)
	}
	if m&right != 0 {
		fill(img, image.Rect(cx, cy, rc.Max.X, cy+2), c)
	}
	if m&up != 0 {
		fill(img, image.Rect(cx, rc.Min.Y, cx+2, cy+1), c)
	}
	if m&down != 0 {
		fill(img, image.Rect(cx, cy, cx+2, rc.Max.Y), c)
	}
	return true
}
