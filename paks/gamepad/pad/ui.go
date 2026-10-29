package main

// The pak screen, laid out like NextUI's own Input tool (Tools > Input),
// which this pak replaces: the same groups, circles and pills at the same
// places, lit while pressed, and the same "SELECT START QUIT" row. The top
// band shows the Bluetooth state (off, or who the pad talks to); while MENU
// is held the bottom row shows the combos it starts (pad on/off, d-pad as
// stick); the MENU row is Home (or Reconnect while no host is connected). Drawing primitives
// and fonts come from the Music player (fb.go and font_generated.go are
// symlinks to paks/music/player).

import (
	"bufio"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type view struct {
	device string // the status chip's text: host, our name, or the error
	state  string // stOff, stReady, stConnected, ...
	on     bool   // the Bluetooth pad is on (else the key tester)
	pad    padState
	menu   bool
	// reconnect: no host connected but a known one; X reconnects (shown
	// in the MENU row, MENU has nothing to send then).
	reconnect bool
	stick     bool // d-pad sent as the left stick ("LS" in the cross)
}

type colors struct{ bg, fg, chip, btn, btnFg, hint, lit, litFg, red uint32 }

type rgb struct{ r, g, b uint8 }

// themeColors reads NextUI's color1..6 (0xRRGGBB) like the Music player.
func themeColors() map[int]rgb {
	out := map[int]rgb{1: {255, 255, 255}, 2: {0x26, 0x26, 0x26}, 3: {0x99, 0x99, 0x99},
		4: {255, 255, 255}, 5: {0, 0, 0}, 6: {255, 255, 255}}
	sd := os.Getenv("SDCARD_PATH")
	if sd == "" {
		sd = "/mnt/SDCARD"
	}
	f, err := os.Open(filepath.Join(sd, ".userdata/shared/minuisettings.txt"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || !strings.HasPrefix(k, "color") {
			continue
		}
		i, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(k), "color"))
		v = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), "0x")
		if err != nil || i < 1 || i > 6 || len(v) < 6 {
			continue
		}
		if n, err := strconv.ParseUint(v[:6], 16, 32); err == nil {
			out[i] = rgb{uint8(n >> 16), uint8(n >> 8), uint8(n)}
		}
	}
	return out
}

func makeColors(c *canvas) colors {
	n := themeColors()
	q := func(i int) uint32 { return c.rgb(n[i].r, n[i].g, n[i].b) }
	// Input: groups color2, idle buttons black with white text, pressed
	// buttons color1 with color3 text.
	return colors{bg: c.rgb(0, 0, 0), fg: q(4), chip: q(2), btn: q(1), btnFg: q(3),
		hint: q(6), lit: q(1), litFg: q(3), red: c.rgb(0xeb, 0x57, 0x57)}
}

type screen struct {
	fb  *framebuffer
	cv  *canvas
	col colors
}

func newScreen(fb *framebuffer) *screen {
	cv := newCanvas(fb)
	return &screen{fb: fb, cv: cv, col: makeColors(cv)}
}

// Input's metrics (640x480): groups 60 high with 10 px padding, buttons 40
// high, circles 40 wide, pills 85 wide.
const (
	groupH = 60
	keyH   = 40
	pillW  = 85
	pad    = 10
)

var (
	faceKey   = &face24 // one or two letters in a circle
	faceWord  = &face21 // words in a pill (SELECT, MENU)
	faceLabel = &face21 // label after the pills (QUIT in Input)
)

func (s *screen) group(x, y, w, h int) { s.cv.pill(x, y, w, h, groupH/2, s.col.chip) }

// key draws one button (circle when w == keyH), lit when pressed.
func (s *screen) key(x, y, w int, label string, on bool) {
	c := s.cv
	bg, fg := c.rgb(0, 0, 0), s.col.fg
	if on {
		bg, fg = s.col.lit, s.col.litFg
	}
	c.pill(x, y, w, keyH, keyH/2, bg)
	face := faceKey
	if len([]rune(label)) > 2 {
		face = faceWord
	}
	if w > keyH {
		label = truncate(face, label, w-16)
	}
	c.textMid(x+(w-textWidth(face, label))/2, y, keyH, face, label, fg)
}

// keyW: circle for one letter, else a pill at least Input's width.
func keyW(label string) int {
	if len([]rune(label)) == 1 {
		return keyH
	}
	return max(pillW, textWidth(faceWord, label)+20)
}

// rowWidth: the width of a row() group.
func rowWidth(keys []string, label string) int {
	w := pad
	for _, k := range keys {
		w += keyW(k) + pad
	}
	if label != "" {
		w += textWidth(faceLabel, label) + 16 - pad + 6
	}
	return w
}

// row draws a group of keys followed by an optional label, from x.
func (s *screen) row(x, y int, keys []string, lit []bool, label string) {
	s.group(x, y, rowWidth(keys, label), groupH)
	kx := x + pad
	for i, k := range keys {
		s.key(kx, y+pad, keyW(k), k, lit[i])
		kx += keyW(k) + pad
	}
	if label != "" {
		s.cv.textMid(kx, y, groupH, faceLabel, label, s.col.fg)
	}
}

// cross draws a d-pad shaped group centred on (cx, cy) with four circles.
func (s *screen) cross(cx, cy int, labels [4]string, on [4]bool) {
	s.group(cx-30, cy-90, 60, 180)
	s.group(cx-90, cy-30, 180, 60)
	s.key(cx-20, cy-80, keyH, labels[0], on[0]) // top
	s.key(cx-20, cy+40, keyH, labels[1], on[1]) // bottom
	s.key(cx-80, cy-20, keyH, labels[2], on[2]) // left
	s.key(cx+40, cy-20, keyH, labels[3], on[3]) // right
}

// Link states: the colour of the chip's dot.
const (
	stOff          = "off" // key tester, Bluetooth untouched
	stStarting     = "starting"
	stReady        = "ready" // discoverable, waiting for a host
	stReconnecting = "reconnecting"
	stConnected    = "connected"
	stError        = "error"
)

// drawStatus: one chip in the top band, a dot for the link state and one
// short text (host name, our name while waiting, or the error).
func (s *screen) drawStatus(text, state string) {
	c := s.cv
	dot := map[string]uint32{
		stOff: c.rgb(0x8d, 0x8d, 0x93), stStarting: c.rgb(0x8d, 0x8d, 0x93), stReady: s.col.fg, stReconnecting: c.rgb(0xf5, 0xb7, 0x31),
		stConnected: c.rgb(0x4c, 0xd9, 0x64), stError: s.col.red,
	}[state]
	text = truncate(faceWord, text, 520)
	w := 18 + 12 + 10 + textWidth(faceWord, text) + 22
	x := (640 - w) / 2
	c.pill(x, 18, w, 44, 22, s.col.chip)
	c.pill(x+18, 18+16, 12, 12, 6, dot)
	c.textMid(x+18+12+10, 18, 44, faceWord, text, s.col.fg)
}

func (s *screen) draw(v view) {
	p := v.pad
	on := func(b int) bool { return p.buttons&(1<<(b-1)) != 0 }
	s.cv.rect(0, 0, 640, 480, s.col.bg)
	// Vertical rhythm: 18 px margins top and bottom, 33 px between the
	// status chip (18..62), the shoulders, the crosses and the bottom row.
	const (
		sy = 95  // shoulders
		cy = 278 // centre of the crosses and of the middle row
		by = 401 // bottom row
	)
	// Shoulders, as Input: L1 L2 on the left, R2 R1 on the right.
	s.group(30, sy, 110, groupH)
	s.key(40, sy+pad, keyH, "L1", on(hidL1))
	s.key(90, sy+pad, keyH, "L2", on(hidL2))
	s.group(500, sy, 110, groupH)
	s.key(510, sy+pad, keyH, "R2", on(hidR2))
	s.key(560, sy+pad, keyH, "R1", on(hidR1))
	s.drawStatus(v.device, v.state)
	s.cross(110, cy, [4]string{"U", "D", "L", "R"}, [4]bool{p.up, p.down, p.left, p.right})
	s.cross(530, cy, [4]string{"X", "B", "Y", "A"}, [4]bool{on(hidNorth), on(hidSouth), on(hidWest), on(hidEast)})
	if v.stick {
		s.key(90, cy-20, keyH, "LS", false) // centre of the d-pad cross
	}
	// Middle column centred, as Input.
	switch {
	case v.on && v.reconnect:
		s.rowC(cy-30, []string{"X"}, []bool{on(hidNorth)}, "RECONNECT")
	case v.on:
		s.rowC(cy-30, []string{"MENU"}, []bool{v.menu}, "HOME")
	default:
		s.rowC(cy-30, []string{"MENU"}, []bool{v.menu}, "")
	}
	// Bottom row: Input's "SELECT START QUIT", or while MENU is held the
	// combos it starts (same height, same place).
	switch {
	case !v.menu:
		s.rowC(by, []string{"SELECT", "START"}, []bool{on(hidSelect), on(hidStart)}, "QUIT")
	case v.on:
		s.hint(by, [][2]string{{"START", "BT OFF"}, {"SELECT", "STICK"}})
	default:
		s.hint(by, [][2]string{{"START", "BT ON"}})
	}
}

// rowC draws row() centred on the screen.
func (s *screen) rowC(y int, keys []string, lit []bool, label string) {
	s.row((640-rowWidth(keys, label))/2, y, keys, lit, label)
}

// hint: one centred group of "key label" pairs, the combos MENU completes
// while it is held (MENU itself is lit in the middle column).
func (s *screen) hint(y int, pairs [][2]string) {
	w := pad - 4
	for _, p := range pairs {
		w += keyW(p[0]) + pad + textWidth(faceLabel, p[1]) + 16
	}
	x := (640 - w) / 2
	s.group(x, y, w, groupH)
	x += pad
	for _, p := range pairs {
		s.key(x, y+pad, keyW(p[0]), p[0], false)
		x += keyW(p[0]) + pad
		s.cv.textMid(x, y, groupH, faceLabel, p[1], s.col.fg)
		x += textWidth(faceLabel, p[1]) + 16
	}
}

func (s *screen) present() { s.fb.present(s.cv.pixels) }

func savePNG(s *screen, path string) error {
	img := image.NewRGBA(image.Rect(0, 0, s.fb.w, s.fb.h))
	for y := 0; y < s.fb.h; y++ {
		for x := 0; x < s.fb.w; x++ {
			p := s.cv.pixels[y*s.fb.w+x]
			img.SetRGBA(x, y, color.RGBA{uint8(p >> 16), uint8(p >> 8), uint8(p), 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
