package main

// Look of the player: NextUI's own theme colors and status icons, a light
// variant, and the image helpers (area-average scaling, alpha masks) that
// keep covers and icons free of aliasing.

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Fallback copy of NextUI's assets@2x.png (used off-device); on the console
// the file from .system/res wins, so a theme's assets show as in NextUI.
//
//go:embed nextui-assets@2x.png
var embeddedAssets []byte

// Music note drawn in the same style as the assets (24x24, white mask).
//
//go:embed note.png
var embeddedNote []byte

type palette struct {
	bg, fg, dim, chip, pill, pillFg, track, fill, card, btn, btnFg, hint, icon uint32
}

type rgb struct{ r, g, b uint8 }

// nextuiColors reads color1..6 from NextUI's settings (0xRRGGBBAA).
func nextuiColors() map[int]rgb {
	out := map[int]rgb{1: {255, 255, 255}, 2: {0x26, 0x26, 0x26}, 3: {0x99, 0x99, 0x99},
		4: {255, 255, 255}, 5: {0, 0, 0}, 6: {255, 255, 255}}
	sd := os.Getenv("SDCARD_PATH")
	if sd == "" {
		sd = "/mnt/SDCARD"
	}
	kv, err := readKV(filepath.Join(sd, ".userdata/shared/minuisettings.txt"))
	if err != nil {
		return out
	}
	for i := 1; i <= 6; i++ {
		v := strings.TrimPrefix(strings.ToLower(kv["color"+strconv.Itoa(i)]), "0x")
		if len(v) < 6 {
			continue
		}
		n, err := strconv.ParseUint(v[:6], 16, 32)
		if err == nil {
			out[i] = rgb{uint8(n >> 16), uint8(n >> 8), uint8(n)}
		}
	}
	return out
}

func makePalette(c *canvas, light bool) palette {
	p := func(r, g, b uint8) uint32 { return c.rgb(r, g, b) }
	if light {
		return palette{bg: p(0xf2, 0xf2, 0xf4), fg: p(0x0d, 0x0d, 0x0f), dim: p(0x73, 0x73, 0x7a), chip: p(0xe1, 0xe1, 0xe6),
			pill: p(0x0d, 0x0d, 0x0f), pillFg: p(255, 255, 255), track: p(0xcf, 0xcf, 0xd5), fill: p(0x0d, 0x0d, 0x0f),
			card: p(255, 255, 255), btn: p(0x0d, 0x0d, 0x0f), btnFg: p(0xa6, 0xa6, 0xad), hint: p(0x0d, 0x0d, 0x0f), icon: p(0x0d, 0x0d, 0x0f)}
	}
	n := nextuiColors()
	q := func(i int) uint32 { return c.rgb(n[i].r, n[i].g, n[i].b) }
	return palette{bg: p(0, 0, 0), fg: q(4), dim: p(0x8d, 0x8d, 0x93), chip: q(2), pill: q(1), pillFg: q(5),
		track: p(0x33, 0x33, 0x38), fill: q(4), card: p(0x12, 0x12, 0x12), btn: q(1), btnFg: q(3), hint: q(6), icon: q(6)}
}

// mask is an alpha-only image (icons, tinted when drawn).
type mask struct {
	w, h int
	a    []uint8
}

func maskFrom(img image.Image, r image.Rectangle) mask {
	m := mask{w: r.Dx(), h: r.Dy(), a: make([]uint8, r.Dx()*r.Dy())}
	for y := 0; y < m.h; y++ {
		for x := 0; x < m.w; x++ {
			_, _, _, al := img.At(r.Min.X+x, r.Min.Y+y).RGBA()
			m.a[y*m.w+x] = uint8(al >> 8)
		}
	}
	return m
}

// scaled resizes a mask with an area average (no aliasing).
func (m mask) scaled(w, h int) mask {
	out := mask{w: w, h: h, a: make([]uint8, w*h)}
	for y := 0; y < h; y++ {
		y0, y1 := y*m.h/h, max((y+1)*m.h/h, y*m.h/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*m.w/w, max((x+1)*m.w/w, x*m.w/w+1)
			sum, n := 0, 0
			for yy := y0; yy < y1 && yy < m.h; yy++ {
				for xx := x0; xx < x1 && xx < m.w; xx++ {
					sum += int(m.a[yy*m.w+xx])
					n++
				}
			}
			if n > 0 {
				out.a[y*w+x] = uint8(sum / n)
			}
		}
	}
	return out
}

func (c *canvas) mask(m mask, x, y int, col uint32) { c.maskClip(m, x, y, m.w, col) }

// maskClip draws the rightmost w columns' worth: NextUI fills the battery
// from the right edge of ASSET_BATTERY_FILL.
func (c *canvas) maskClip(m mask, x, y, w int, col uint32) {
	from := m.w - w
	if from < 0 {
		from = 0
	}
	for yy := 0; yy < m.h; yy++ {
		for xx := from; xx < m.w; xx++ {
			if a := m.a[yy*m.w+xx]; a != 0 {
				c.plot(x+xx-from, y+yy, col, a)
			}
		}
	}
}

type icons struct {
	wifi, wifiMed, wifiLow, bt, audio, battery, batteryLow, fill, bolt, volume, mute, note, gear mask
}

// loadIcons crops NextUI's sprite with the rects of api.c (x2 for @2x).
func loadIcons() icons {
	var sprite image.Image
	sd := os.Getenv("SDCARD_PATH")
	if sd == "" {
		sd = "/mnt/SDCARD"
	}
	if f, err := os.Open(filepath.Join(sd, ".system/res/assets@2x.png")); err == nil {
		sprite, _ = png.Decode(f)
		f.Close()
	}
	if sprite == nil || sprite.Bounds().Dx() < 256 {
		sprite, _ = png.Decode(bytes.NewReader(embeddedAssets))
	}
	r := func(x, y, w, h int) mask { return maskFrom(sprite, image.Rect(x*2, y*2, (x+w)*2, (y+h)*2)) }
	ic := icons{
		wifi: r(1, 104, 12, 12), wifiMed: r(14, 104, 12, 12), wifiLow: r(27, 104, 12, 12),
		bt: r(53, 104, 12, 12), audio: r(79, 104, 12, 12),
		battery: r(47, 51, 17, 10), batteryLow: r(66, 51, 17, 10), fill: r(81, 33, 12, 6), bolt: r(81, 41, 12, 6),
		volume: r(21, 85, 19, 19).scaled(24, 24), mute: r(21, 85, 10, 19).scaled(13, 24), gear: r(21, 117, 10, 10),
	}
	if n, err := png.Decode(bytes.NewReader(embeddedNote)); err == nil {
		ic.note = maskFrom(n, n.Bounds())
	}
	return ic
}

// boxScale resizes a cover with an area average (no aliasing).
func boxScale(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	rgba, ok := src.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	}
	sw, sh := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, max((x+1)*sw/w, x*sw/w+1)
			var r, g, bb, n int
			for yy := y0; yy < y1 && yy < sh; yy++ {
				i := yy*rgba.Stride + x0*4
				for xx := x0; xx < x1 && xx < sw; xx++ {
					r += int(rgba.Pix[i])
					g += int(rgba.Pix[i+1])
					bb += int(rgba.Pix[i+2])
					n++
					i += 4
				}
			}
			if n == 0 {
				n = 1
			}
			o := y*out.Stride + x*4
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8(r/n), uint8(g/n), uint8(bb/n), 255
		}
	}
	return out
}

// average is the mean color of an image (the tint of Now Playing).
func average(img *image.RGBA) color.RGBA {
	var r, g, b, n int
	for i := 0; i+3 < len(img.Pix); i += 16 {
		r += int(img.Pix[i])
		g += int(img.Pix[i+1])
		b += int(img.Pix[i+2])
		n++
	}
	if n == 0 {
		return color.RGBA{40, 40, 40, 255}
	}
	return color.RGBA{uint8(r / n), uint8(g / n), uint8(b / n), 255}
}

// cover draws a pre-scaled cover with antialiased rounded corners.
func (c *canvas) cover(img *image.RGBA, x, y, r int) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	for dy := 0; dy < h; dy++ {
		yy := y + dy
		if yy < 0 || yy >= c.h {
			continue
		}
		for dx := 0; dx < w; dx++ {
			a := roundAlpha(dx, dy, w, h, r)
			if a == 0 {
				continue
			}
			o := dy*img.Stride + dx*4
			c.plot(x+dx, yy, c.rgb(img.Pix[o], img.Pix[o+1], img.Pix[o+2]), a)
		}
	}
}

// tint lays a diagonal fade of col over a rounded box: strength at the
// top-left corner, gone at `reach` (0-1) of the diagonal.
func (c *canvas) tint(x, y, w, h, r int, col uint32, strength, reach float64, angleX float64) {
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			t := (float64(dx)*angleX/float64(w) + float64(dy)*(1-angleX)/float64(h)) / reach
			if t >= 1 {
				continue
			}
			a := strength * (1 - t)
			ra := roundAlpha(dx, dy, w, h, r)
			if ra == 0 {
				continue
			}
			c.plot(x+dx, y+dy, col, uint8(a*float64(ra)))
		}
	}
}

// ring draws a rounded outline of thickness t (outside the box).
func (c *canvas) ring(x, y, w, h, r, t int, col, inside uint32) {
	c.pill(x-t, y-t, w+2*t, h+2*t, r+t, col)
	c.pill(x, y, w, h, r, inside)
}

// circleRing is an antialiased ring (search icon).
func (c *canvas) circleRing(cx, cy, r, t int, col uint32) {
	for y := -r - 1; y <= r+1; y++ {
		for x := -r - 1; x <= r+1; x++ {
			in := 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					fx := float64(x) + (float64(sx)+.5)/4
					fy := float64(y) + (float64(sy)+.5)/4
					d := fx*fx + fy*fy
					if d <= float64(r*r) && d >= float64((r-t)*(r-t)) {
						in++
					}
				}
			}
			if in > 0 {
				c.plot(cx+x, cy+y, col, uint8(in*255/16))
			}
		}
	}
}

// triangle draws an antialiased play triangle pointing right.
func (c *canvas) triangle(x, y, w, h int, col uint32) {
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			in := 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					fx := (float64(dx) + (float64(sx)+.5)/4) / float64(w)
					fy := (float64(dy) + (float64(sy)+.5)/4) / float64(h)
					if fy >= fx/2 && fy <= 1-fx/2 {
						in++
					}
				}
			}
			if in > 0 {
				c.plot(x+dx, y+dy, col, uint8(in*255/16))
			}
		}
	}
}
