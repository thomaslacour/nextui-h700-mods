package main

// Status row of the launcher, ported from nextui/patches/20-status-dial.patch
// (same vector icons on a 14-unit grid, same order, sizes, gaps and
// colors), plus a music note shown while music plays. Keep both in step:
// the launcher shows the same note (SS_drawMusic in the patch).

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// layer is a small straight-alpha RGBA surface, as the patch's ARGB layer.
type layer struct {
	w, h    int
	r, g, b []float32
	a       []float32
	cutting bool
}

func newLayer(w, h int) *layer {
	n := w * h
	return &layer{w: w, h: h, r: make([]float32, n), g: make([]float32, n), b: make([]float32, n), a: make([]float32, n)}
}

type rgba struct{ r, g, b, a float32 } // 0-255 each

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func (l *layer) over(x, y int, c rgba, cov float32) {
	if x < 0 || y < 0 || x >= l.w || y >= l.h || cov <= 0 {
		return
	}
	i := y*l.w + x
	if l.cutting {
		l.a[i] *= 1 - clamp01(cov)
		return
	}
	sa := clamp01(cov * c.a / 255)
	if sa <= 0 {
		return
	}
	da := l.a[i]
	oa := sa + da*(1-sa)
	k := da * (1 - sa)
	l.r[i] = (c.r*sa + l.r[i]*k) / oa
	l.g[i] = (c.g*sa + l.g[i]*k) / oa
	l.b[i] = (c.b*sa + l.b[i]*k) / oa
	l.a[i] = oa
}

func hypot(x, y float32) float32 { return float32(math.Hypot(float64(x), float64(y))) }

func (l *layer) disc(cx, cy, rad float32, c rgba) {
	for y := int(cy - rad - 1); y <= int(cy+rad+1); y++ {
		for x := int(cx - rad - 1); x <= int(cx+rad+1); x++ {
			l.over(x, y, c, clamp01(rad+0.5-hypot(float32(x)+0.5-cx, float32(y)+0.5-cy)))
		}
	}
}

// arc of a ring from `from` to `to` (fractions of a turn, 0 = noon, clockwise).
func (l *layer) arc(cx, cy, rad, width, from, to float32, c rgba) {
	circ := 2 * math.Pi * rad
	for y := int(cy - rad - width); y <= int(cy+rad+width); y++ {
		for x := int(cx - rad - width); x <= int(cx+rad+width); x++ {
			dx, dy := float32(x)+0.5-cx, float32(y)+0.5-cy
			cov := clamp01(width/2 + 0.5 - float32(math.Abs(float64(hypot(dx, dy)-rad))))
			if cov <= 0 {
				continue
			}
			t := float32(math.Atan2(float64(dx), float64(-dy)) / (2 * math.Pi))
			if t < 0 {
				t++
			}
			if to-from < 1 {
				cov *= clamp01((to-t)*circ+0.5) * clamp01((t-from)*circ+0.5)
			}
			l.over(x, y, c, cov)
		}
	}
}

func (l *layer) line(x1, y1, x2, y2, width float32, c rgba) {
	vx, vy := x2-x1, y2-y1
	len2 := vx*vx + vy*vy
	for y := int(min(y1, y2)) - int(width) - 1; y <= int(max(y1, y2))+int(width)+1; y++ {
		for x := int(min(x1, x2)) - int(width) - 1; x <= int(max(x1, x2))+int(width)+1; x++ {
			px, py := float32(x)+0.5-x1, float32(y)+0.5-y1
			t := clamp01((px*vx + py*vy) / len2)
			l.over(x, y, c, clamp01(width/2+0.5-hypot(px-t*vx, py-t*vy)))
		}
	}
}

func inPolygon(p []float32, x, y float32) bool {
	in := false
	n := len(p) / 2
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		if (p[2*i+1] > y) != (p[2*j+1] > y) && x < (p[2*j]-p[2*i])*(y-p[2*i+1])/(p[2*j+1]-p[2*i+1])+p[2*i] {
			in = !in
		}
	}
	return in
}

func (l *layer) poly(p []float32, c rgba) {
	x0, x1, y0, y1 := p[0], p[0], p[1], p[1]
	for i := 1; i < len(p)/2; i++ {
		x0, x1 = min(x0, p[2*i]), max(x1, p[2*i])
		y0, y1 = min(y0, p[2*i+1]), max(y1, p[2*i+1])
	}
	for py := int(y0) - 1; py <= int(y1)+1; py++ {
		for px := int(x0) - 1; px <= int(x1)+1; px++ {
			hits := 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					if inPolygon(p, float32(px)+(float32(sx)+0.5)/4, float32(py)+(float32(sy)+0.5)/4) {
						hits++
					}
				}
			}
			if hits > 0 {
				l.over(px, py, c, float32(hits)/16)
			}
		}
	}
}

func (l *layer) rrect(x0, y0, x1, y1, rad float32, c rgba) {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	hx, hy := (x1-x0)/2-rad, (y1-y0)/2-rad
	for y := int(y0) - 1; y <= int(y1)+1; y++ {
		for x := int(x0) - 1; x <= int(x1)+1; x++ {
			qx := float32(math.Abs(float64(float32(x)+0.5-cx))) - hx
			qy := float32(math.Abs(float64(float32(y)+0.5-cy))) - hy
			d := hypot(max(qx, 0), max(qy, 0)) + min(max(qx, qy), 0) - rad
			l.over(x, y, c, clamp01(0.5-d))
		}
	}
}

// pen: glyph on a 14-unit grid, origin top left (SS_Pen).
type pen struct {
	l        *layer
	x, y, k  float32
	ink, dim rgba
}

func (p *pen) X(v float32) float32 { return p.x + v*p.k }
func (p *pen) Y(v float32) float32 { return p.y + v*p.k }
func (p *pen) K(v float32) float32 { return v * p.k }
func (p *pen) line(x1, y1, x2, y2, w float32, c rgba) {
	p.l.line(p.X(x1), p.Y(y1), p.X(x2), p.Y(y2), p.K(w), c)
}
func (p *pen) disc(x, y, r float32, c rgba) { p.l.disc(p.X(x), p.Y(y), p.K(r), c) }
func (p *pen) rect(x0, y0, x1, y1, r float32, c rgba) {
	p.l.rrect(p.X(x0), p.Y(y0), p.X(x1), p.Y(y1), p.K(r), c)
}
func (p *pen) arcTop(x, y, r, w, half float32, c rgba) {
	p.l.arc(p.X(x), p.Y(y), p.K(r), p.K(w), 1-half, 1, c)
	p.l.arc(p.X(x), p.Y(y), p.K(r), p.K(w), 0, half, c)
}
func (p *pen) slash(x1, y1, x2, y2 float32) {
	p.l.cutting = true
	p.line(x1, y1, x2, y2, 3.6, rgba{})
	p.l.cutting = false
	p.line(x1, y1, x2, y2, 1.6, p.ink)
}

var (
	ssGreen = rgba{0x4c, 0xd9, 0x64, 255}
	ssRed   = rgba{0xeb, 0x57, 0x57, 255}
	ssBlue  = rgba{0x5a, 0xaa, 0xff, 255}
	ssAmber = rgba{0xf5, 0xb8, 0x3d, 255}
)

// statusState is what the icons show (read at most every 5 s, local only).
type statusState struct {
	charge          int
	charging        bool
	wifi            int // 0 off, 1-3 bars
	vpn             int // 0 off, 1 on, 2 failed
	bt, pad, update bool
	alert, music    bool
	clock           string
}

const (
	vpnOff = iota
	vpnOn
	vpnFail
)

func drawBattery(p *pen, s statusState) {
	fill := p.ink
	if s.charging {
		fill = ssGreen
	} else if s.charge < 20 {
		fill = ssRed
	}
	p.rect(0.4, 2.2, 21.6, 11.8, 2.4, p.dim)
	p.rect(0.4, 2.2, 0.4+max(4.8, 21.2*clamp01(float32(s.charge)/100)), 11.8, 2.4, fill)
}

func drawWifi(p *pen, s statusState) {
	radii := []float32{4.0, 7.6, 11.2}
	for i, r := range radii {
		c := p.dim
		if i < s.wifi {
			c = p.ink
		}
		p.arcTop(8, 12.6, r, 1.9, 0.125, c)
	}
	c := p.dim
	if s.wifi > 0 {
		c = p.ink
	}
	p.disc(8, 12.6, 1.4, c)
	if s.wifi == 0 {
		p.slash(2, 1, 14, 13.5)
	}
}

func drawVpn(p *pen, s statusState) {
	c := p.ink
	if s.vpn == vpnFail {
		c = p.dim
	}
	p.arcTop(5.5, 4.4, 3.3, 1.7, 0.25, c)
	p.line(2.2, 4.4, 2.2, 6.6, 1.7, c)
	p.line(8.8, 4.4, 8.8, 6.6, 1.7, c)
	p.rect(0.6, 6.0, 10.4, 14.0, 1.6, c)
	p.l.cutting = true
	p.disc(5.5, 9.8, 1.0, rgba{})
	p.l.cutting = false
	if s.vpn == vpnFail {
		p.slash(0.2, 1.0, 10.8, 13.6)
	}
}

func drawBluetooth(p *pen, s statusState) {
	p.line(4.5, 0.8, 4.5, 13.2, 1.6, p.ink)
	p.line(4.5, 0.8, 8.0, 4.0, 1.6, p.ink)
	p.line(8.0, 4.0, 1.0, 10.0, 1.6, p.ink)
	p.line(4.5, 13.2, 8.0, 10.0, 1.6, p.ink)
	p.line(8.0, 10.0, 1.0, 4.0, 1.6, p.ink)
}

func drawPad(p *pen, s statusState) {
	p.rect(0.5, 2.5, 18.5, 10.0, 3.6, p.ink)
	p.disc(4.2, 9.8, 3.4, p.ink)
	p.disc(14.8, 9.8, 3.4, p.ink)
	p.l.cutting = true
	p.rect(3.0, 6.0, 7.4, 7.3, 0.3, rgba{})
	p.rect(4.55, 4.4, 5.85, 8.9, 0.3, rgba{})
	p.disc(13.1, 5.3, 0.85, rgba{})
	p.disc(15.2, 7.5, 0.85, rgba{})
	p.l.cutting = false
}

func drawUpdate(p *pen, s statusState) {
	p.disc(7, 7, 7, ssBlue)
	p.l.cutting = true
	p.line(7.0, 3.6, 7.0, 10.0, 1.7, rgba{})
	p.line(4.2, 7.4, 7.0, 10.2, 1.7, rgba{})
	p.line(7.0, 10.2, 9.8, 7.4, 1.7, rgba{})
	p.l.cutting = false
}

func drawAlert(p *pen, s statusState) {
	p.l.poly([]float32{p.X(7.5), p.Y(0.3), p.X(15), p.Y(13.7), p.X(0), p.Y(13.7)}, ssAmber)
	p.l.cutting = true
	p.line(7.5, 4.8, 7.5, 9.0, 1.7, rgba{})
	p.disc(7.5, 11.2, 0.95, rgba{})
	p.l.cutting = false
}

// drawMusic: a beamed pair of eighth notes, same stroke weight (patch:
// SS_drawMusic, shown while the Music pak plays).
func drawMusic(p *pen, s statusState) {
	p.disc(3.0, 11.0, 2.6, p.ink)
	p.disc(11.0, 9.6, 2.6, p.ink)
	p.line(5.0, 11.0, 5.0, 2.6, 1.6, p.ink)
	p.line(13.0, 9.6, 13.0, 1.2, 1.6, p.ink)
	p.l.poly([]float32{p.X(4.2), p.Y(1.9), p.X(13.8), p.Y(0.4), p.X(13.8), p.Y(3.6), p.X(4.2), p.Y(5.1)}, p.ink)
}

type ssIcon struct {
	w     float32
	shown func(s statusState) bool
	draw  func(p *pen, s statusState)
}

// Right to left, as SS_ICONS; the note goes last (leftmost).
var ssIcons = []ssIcon{
	{22, func(statusState) bool { return true }, drawBattery},
	{16, func(statusState) bool { return true }, drawWifi},
	{11, func(s statusState) bool { return s.vpn != vpnOff }, drawVpn},
	{9, func(s statusState) bool { return s.bt }, drawBluetooth},
	{19, func(s statusState) bool { return s.pad }, drawPad},
	{14, func(s statusState) bool { return s.update }, drawUpdate},
	{15, func(s statusState) bool { return s.alert }, drawAlert},
	{14, func(s statusState) bool { return s.music }, drawMusic},
}

const (
	ssGlyph = 14 // SCALE1(7)
	ssGap   = ssGlyph * 9 / 14
	ssPadX  = 10 // SCALE1(5)
	ssPadY  = 6  // SCALE1(3)
	ssEdge  = 4  // SCALE1(2)
)

// statusRow renders the row (and clock) into a layer, as SS_iconRow.
func statusRow(s statusState, ink rgba, clockW int) (*layer, int) {
	k := float32(ssGlyph) / 14
	var units float32
	n := 0
	for _, ic := range ssIcons {
		if ic.shown(s) {
			units += ic.w
			n++
		}
	}
	w := int(math.Ceil(float64(units*k))) + (n-1)*ssGap + 2*ssPadX
	if s.clock != "" {
		w += ssGap + clockW
	}
	h := ssGlyph + 2*ssPadY
	l := newLayer(w, h)
	dim := ink
	dim.a *= 0.3
	p := &pen{l: l, x: float32(w - ssPadX), y: ssPadY, k: k, ink: ink, dim: dim}
	for _, ic := range ssIcons {
		if !ic.shown(s) {
			continue
		}
		p.x -= ic.w * k
		ic.draw(p, s)
		p.x -= ssGap
	}
	return l, w
}

func (c *canvas) blitLayer(l *layer, x, y int) {
	for yy := 0; yy < l.h; yy++ {
		for xx := 0; xx < l.w; xx++ {
			i := yy*l.w + xx
			if a := l.a[i]; a > 0 {
				c.plot(x+xx, y+yy, c.rgb(uint8(l.r[i]+0.5), uint8(l.g[i]+0.5), uint8(l.b[i]+0.5)), uint8(a*255+0.5))
			}
		}
	}
}

// readStatus mirrors SS_refreshStatus with the same local sources.
func readStatus(sd string, musicPlaying bool) statusState {
	s := statusState{music: musicPlaying, charge: -1}
	for _, dir := range []string{"/sys/class/power_supply/axp2202-battery", "/sys/class/power_supply/battery"} {
		if v := readSys(dir + "/capacity"); v != "" {
			s.charge = atoiOr(v, 0)
			s.charging = readSys(dir+"/status") == "Charging"
			break
		}
	}
	if entries, err := os.ReadDir("/sys/class/power_supply"); err == nil {
		for _, e := range entries {
			t := readSys("/sys/class/power_supply/" + e.Name() + "/type")
			if (t == "USB" || t == "Mains") && readSys("/sys/class/power_supply/"+e.Name()+"/online") == "1" {
				s.charging = true
			}
		}
	}
	if readSys("/sys/class/net/wlan0/operstate") == "up" {
		s.wifi = 3
		for _, line := range strings.Split(readSys("/proc/net/wireless"), "\n") {
			f := strings.Fields(line)
			if len(f) > 2 && strings.HasPrefix(f[0], "wlan0") {
				q := atoiOr(strings.TrimSuffix(f[2], "."), 70)
				switch {
				case q < 30:
					s.wifi = 1
				case q < 50:
					s.wifi = 2
				}
			}
		}
	}
	switch {
	case ifaceHasIPv4("tailscale0"):
		s.vpn = vpnOn
	case processRunning("tailscaled"):
		s.vpn = vpnFail
	case fileHas(filepath.Join(sd, ".userdata/h700/auto.sh"), "Tailscale.pak-on-boot", false):
		s.vpn = vpnFail
	}
	if js, _ := filepath.Glob("/dev/input/js*"); len(js) > 1 {
		s.pad = true
	}
	s.bt = strings.Contains(readSys("/proc/bus/input/devices"), "Bus=0005")
	st := filepath.Join(sd, ".userdata/h700/update-check/state")
	s.update = fileHas(st, "baseos_update=1", true) || fileHas(st, "nextui_update=1", true)
	rep := filepath.Join(sd, ".userdata/h700/save-sync/reports/07-last-check.txt")
	for _, f := range []string{"FAIL CONFLICT_BOTH_CHANGED", "FAIL INITIAL_MISMATCH_MANUAL_REVIEW", "FAIL FIRST_UPLOAD_MANUAL_ONLY",
		"FAIL REMOTE_DELETED_MANUAL_REVIEW", "FAIL STEAM_AUTH_REQUIRED"} {
		if fileHas(rep, f, false) {
			s.alert = true
		}
	}
	kv, _ := readKV(filepath.Join(sd, ".userdata/shared/minuisettings.txt"))
	if kv["showclock"] == "1" {
		if kv["clock24h"] == "0" {
			s.clock = time.Now().Format("3:04 PM")
		} else {
			s.clock = time.Now().Format("15:04")
		}
	}
	return s
}

func atoiOr(s string, def int) int {
	n := 0
	if s == "" {
		return def
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func fileHas(path, needle string, atLineStart bool) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if atLineStart && strings.HasPrefix(line, needle) || !atLineStart && strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

func processRunning(name string) bool {
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		if readSys("/proc/"+e.Name()+"/comm") == name {
			return true
		}
	}
	return false
}
