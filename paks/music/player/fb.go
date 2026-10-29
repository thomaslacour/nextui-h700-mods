package main

// Framebuffer, canvas and text drawing. Same approach as logview/viewer.go
// (direct /dev/fb0 writes, embedded glyph atlas), extended with alpha-aware
// pills, image blits and screen blanking for the music player.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const (
	fbGetVar = 0x4600
	fbGetFix = 0x4602
	fbBlank  = 0x4611 // FBIOBLANK
)

type channel struct{ off, length uint32 }

type framebuffer struct {
	fd                         int
	mapped                     []byte
	w, h, stride, virtH, bpp   int
	chanR, chanG, chanB, chanA channel
}

func le32(b []byte, n int) uint32 { return binary.LittleEndian.Uint32(b[n : n+4]) }
func le16(b []byte, n int) uint16 { return binary.LittleEndian.Uint16(b[n : n+2]) }

func openFramebuffer() (*framebuffer, error) {
	fd, err := syscall.Open("/dev/fb0", syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/fb0: %w", err)
	}
	bad := func(e error) (*framebuffer, error) { syscall.Close(fd); return nil, e }
	var v [256]byte
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), fbGetVar, uintptr(unsafe.Pointer(&v[0]))); e != 0 {
		return bad(fmt.Errorf("FBIOGET_VSCREENINFO: %w", e))
	}
	var f [128]byte
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), fbGetFix, uintptr(unsafe.Pointer(&f[0]))); e != 0 {
		return bad(fmt.Errorf("FBIOGET_FSCREENINFO: %w", e))
	}
	fb := &framebuffer{fd: fd, w: int(le32(v[:], 0)), h: int(le32(v[:], 4)), virtH: int(le32(v[:], 12)),
		bpp: int(le32(v[:], 24)), stride: int(le32(f[:], 48)),
		chanR: channel{le32(v[:], 32), le32(v[:], 36)},
		chanG: channel{le32(v[:], 44), le32(v[:], 48)},
		chanB: channel{le32(v[:], 56), le32(v[:], 60)},
		chanA: channel{le32(v[:], 68), le32(v[:], 72)},
	}
	if fb.w < 320 || fb.w > 2048 || fb.h < 200 || fb.h > 2048 || fb.bpp != 16 && fb.bpp != 24 && fb.bpp != 32 {
		return bad(fmt.Errorf("unexpected framebuffer %dx%d %dbpp", fb.w, fb.h, fb.bpp))
	}
	if fb.virtH < fb.h {
		fb.virtH = fb.h
	}
	size := fb.stride * fb.virtH
	if smem := int(le32(f[:], 24)); smem > 0 && smem < size {
		size = smem
	}
	if size <= 0 || size > 64*1024*1024 {
		return bad(errors.New("unexpected framebuffer size"))
	}
	buf, err := syscall.Mmap(fd, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return bad(fmt.Errorf("mmap: %w", err))
	}
	fb.mapped = buf
	return fb, nil
}

// previewFramebuffer is an in-memory 640x480 XRGB8888 target for tests.
func previewFramebuffer() *framebuffer {
	w, h := 640, 480
	return &framebuffer{w: w, h: h, virtH: h, stride: w * 4, bpp: 32,
		chanR: channel{16, 8}, chanG: channel{8, 8}, chanB: channel{0, 8}, chanA: channel{24, 8},
		mapped: make([]byte, w*h*4), fd: -1}
}

func (f *framebuffer) close() {
	if f.fd < 0 {
		return
	}
	syscall.Munmap(f.mapped)
	syscall.Close(f.fd)
}

// blank turns the panel off (true) or back on. FBIOBLANK first, then the
// sysfs knob as a fallback; errors are returned for the log only.
func (f *framebuffer) blank(off bool) error {
	mode := uintptr(0)
	val := "0"
	if off {
		mode, val = 4, "1" // FB_BLANK_POWERDOWN
	}
	var err error
	if f.fd >= 0 {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(f.fd), fbBlank, mode); e != 0 {
			err = fmt.Errorf("FBIOBLANK: %w", e)
		}
	}
	if werr := os.WriteFile("/sys/class/graphics/fb0/blank", []byte(val), 0); werr != nil && err != nil {
		return fmt.Errorf("%v; sysfs: %v", err, werr)
	}
	return nil
}

func (f *framebuffer) pack(r, g, b uint8) uint32 {
	pack := func(c uint8, p channel) uint32 {
		if p.length == 0 || p.length > 8 || p.off > 31 {
			return 0
		}
		return ((uint32(c)*((1<<p.length)-1) + 127) / 255) << p.off
	}
	v := pack(r, f.chanR) | pack(g, f.chanG) | pack(b, f.chanB)
	if f.chanA.length > 0 && f.chanA.length <= 8 && f.chanA.off < 32 {
		v |= ((1 << f.chanA.length) - 1) << f.chanA.off
	}
	return v
}

// present copies the canvas to every framebuffer page, so the image shows
// whatever page the display currently scans out.
func (f *framebuffer) present(img []uint32) { f.presentRows(img, 0, f.h) }

// presentRows copies rows [y0, y1) only (title animation).
func (f *framebuffer) presentRows(img []uint32, y0, y1 int) {
	if len(img) < f.w*f.h {
		return
	}
	pages := f.virtH / f.h
	if pages < 1 {
		pages = 1
	}
	if pages > 4 {
		pages = 4
	}
	px := f.bpp / 8
	row := make([]byte, f.w*px)
	for y := max(0, y0); y < min(f.h, y1); y++ {
		src := img[y*f.w : (y+1)*f.w]
		if px == 4 {
			for x, p := range src {
				binary.LittleEndian.PutUint32(row[x*4:], p)
			}
		} else {
			for x, p := range src {
				for k := 0; k < px; k++ {
					row[x*px+k] = byte(p >> (k * 8))
				}
			}
		}
		for page := 0; page < pages; page++ {
			off := (page*f.h + y) * f.stride
			if off+len(row) <= len(f.mapped) {
				copy(f.mapped[off:], row)
			}
		}
	}
}

type canvas struct {
	w, h           int
	pixels         []uint32
	fb             *framebuffer
	clipY0, clipY1 int // text clip band (0, 0 = none)
}

func newCanvas(f *framebuffer) *canvas {
	return &canvas{w: f.w, h: f.h, pixels: make([]uint32, f.w*f.h), fb: f}
}

func (c *canvas) rgb(r, g, b uint8) uint32 { return c.fb.pack(r, g, b) }

func (c *canvas) rect(x, y, w, h int, col uint32) {
	if x < 0 {
		w += x
		x = 0
	}
	if y < 0 {
		h += y
		y = 0
	}
	if x+w > c.w {
		w = c.w - x
	}
	if y+h > c.h {
		h = c.h - y
	}
	if w <= 0 || h <= 0 {
		return
	}
	for yy := y; yy < y+h; yy++ {
		row := c.pixels[yy*c.w+x : yy*c.w+x+w]
		for i := range row {
			row[i] = col
		}
	}
}

func (c *canvas) blend(dst, src uint32, a uint8) uint32 {
	if a == 255 {
		return src
	}
	if a == 0 {
		return dst
	}
	out := dst
	for _, ch := range []channel{c.fb.chanR, c.fb.chanG, c.fb.chanB} {
		if ch.length == 0 || ch.length > 8 || ch.off > 31 {
			continue
		}
		mask := uint32((1 << ch.length) - 1)
		o := (dst >> ch.off) & mask
		n := (src >> ch.off) & mask
		m := (o*uint32(255-a) + n*uint32(a) + 127) / 255
		out = (out &^ (mask << ch.off)) | ((m & mask) << ch.off)
	}
	return out
}

func (c *canvas) plot(x, y int, col uint32, a uint8) {
	if x < 0 || y < c.clipY0 || x >= c.w || y >= c.h || (c.clipY1 > 0 && y >= c.clipY1) || a == 0 {
		return
	}
	i := y*c.w + x
	c.pixels[i] = c.blend(c.pixels[i], col, a)
}

// roundAlpha is the coverage (0-255) of pixel (px,py) inside a rounded
// rectangle of size w x h and corner radius r, with 4x4 supersampling on
// the corners only.
func roundAlpha(px, py, w, h, r int) uint8 {
	cx, cy := -1, -1
	switch {
	case px < r && py < r:
		cx, cy = r, r
	case px >= w-r && py < r:
		cx, cy = w-r, r
	case px < r && py >= h-r:
		cx, cy = r, h-r
	case px >= w-r && py >= h-r:
		cx, cy = w-r, h-r
	default:
		return 255
	}
	in := 0
	for sy := 0; sy < 4; sy++ {
		for sx := 0; sx < 4; sx++ {
			dx := float64(px) + (float64(sx)+0.5)/4 - float64(cx)
			dy := float64(py) + (float64(sy)+0.5)/4 - float64(cy)
			if dx*dx+dy*dy <= float64(r*r) {
				in++
			}
		}
	}
	return uint8(in * 255 / 16)
}

// pill draws a filled, antialiased rounded rectangle.
func (c *canvas) pill(x, y, w, h, r int, col uint32) {
	if w <= 0 || h <= 0 {
		return
	}
	if r > h/2 {
		r = h / 2
	}
	if r > w/2 {
		r = w / 2
	}
	for dy := 0; dy < h; dy++ {
		if dy >= r && dy < h-r {
			c.rect(x, y+dy, w, 1, col)
			continue
		}
		for dx := 0; dx < w; dx++ {
			c.plot(x+dx, y+dy, col, roundAlpha(dx, dy, w, h, r))
		}
	}
}

func glyphOf(face *rasterFont, r rune) glyphInfo {
	if g, ok := face.glyphs[r]; ok {
		return g
	}
	return face.glyphs['?']
}

// The NextUI font draws "…" mid-height (Japanese style): text uses three
// periods instead, on the baseline, wherever "…" comes from.
func baselineDots(s string) string { return strings.ReplaceAll(s, "…", "...") }

func textWidth(face *rasterFont, s string) int {
	w := 0
	for _, r := range baselineDots(s) {
		w += glyphOf(face, r).advance
	}
	return w
}

// truncate shortens s with "..." so it fits in max pixels (three periods:
// the NextUI font draws "…" mid-height, not on the baseline).
func truncate(face *rasterFont, s string, max int) string {
	if max <= 0 {
		return ""
	}
	if textWidth(face, s) <= max {
		return s
	}
	limit := max - textWidth(face, "...")
	w := 0
	out := []rune{}
	for _, r := range baselineDots(s) {
		a := glyphOf(face, r).advance
		if w+a > limit {
			break
		}
		w += a
		out = append(out, r)
	}
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return string(out) + "..."
}

// text draws s with its top at y; returns the x after the last glyph.
func (c *canvas) text(x, y int, face *rasterFont, s string, col uint32) int {
	for _, r := range baselineDots(s) {
		g := glyphOf(face, r)
		for gy := 0; gy < face.height; gy++ {
			yy := y + gy
			if yy < 0 || yy >= c.h {
				continue
			}
			src := g.off + gy*g.width
			for gx := 0; gx < g.width; gx++ {
				if a := fontAtlas[src+gx]; a != 0 {
					c.plot(x+gx, yy, col, a)
				}
			}
		}
		x += g.advance
	}
	return x
}

// textMid draws s vertically centred in a band [y, y+h).
func (c *canvas) textMid(x, y, h int, face *rasterFont, s string, col uint32) int {
	return c.text(x, y+(h-face.height)/2+face.shift, face, s, col)
}

// wrap splits s into at most n lines of max pixels, the last one truncated.
func wrap(face *rasterFont, s string, max, n int) []string {
	var lines []string
	cur := ""
	for _, w := range splitWords(s) {
		try := w
		if cur != "" {
			try = cur + " " + w
		}
		if textWidth(face, try) <= max || cur == "" {
			cur = try
			continue
		}
		lines = append(lines, cur)
		cur = w
		if len(lines) == n-1 {
			break
		}
	}
	rest := cur
	if len(lines) == n-1 {
		// Everything not yet placed goes on the last line.
		placed := 0
		for _, l := range lines {
			placed += len([]rune(l)) + 1
		}
		rr := []rune(s)
		if placed < len(rr) {
			rest = string(rr[placed:])
		}
	}
	if rest != "" {
		lines = append(lines, truncate(face, rest, max))
	}
	return lines
}

func splitWords(s string) []string {
	var out []string
	cur := []rune{}
	for _, r := range s {
		if r == ' ' {
			if len(cur) > 0 {
				out = append(out, string(cur))
				cur = cur[:0]
			}
			continue
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}
