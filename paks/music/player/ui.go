package main

// Main loop and screen chrome (v3 design): tabs top left, NextUI status pill
// top right, NextUI button groups at the bottom, toasts, volume indicator.

import (
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	pollEach = time.Second
	coverPx  = 250 // asked from the server, scaled down locally
)

type hint struct{ btn, label string }

type app struct {
	cfg   *config
	cfgOK bool
	api   *client
	mpd   *mpd
	fb    *framebuffer
	cv    *canvas
	col   palette
	ic    icons
	light bool

	tab   int
	stack []screen

	toastText  string
	toastStart time.Time
	toastUntil time.Time
	toastErr   bool
	busyMsg    string
	statusX    int      // left edge of the status row (title space ends there)
	bandBG     []uint32 // the screen under the title band, without title
	volUntil   time.Time
	volume     int // 0-100, read after a volume key
	volC       chan int
	off, quit  bool
	menuHeld   bool // MENU is down (quit if START comes)
	menuCombo  bool
	dirty      bool

	meta  map[string]song
	st    map[string]string
	cur   song
	curID string
	mpdOK bool
	scrob struct {
		id           string
		elapsed, dur float64
		sent         bool
	}

	covers map[string]*image.RGBA // coverPx, by cover id
	scaled map[string]*image.RGBA // "id@size"
	avg    map[string]color.RGBA
	asked  map[string]bool
	coverQ chan string
	coverC chan coverResult

	status    statusState
	lib       *library
	libR      libRefresh
	favorites []station
	radioHits map[string][]row // directory searches, this session

	home    *homeScreen
	index   *libIndex
	offline bool // previews and tests: never call the server
}

type coverResult struct {
	id  string
	img *image.RGBA
}

type screen interface {
	crumb(a *app) string // "" = root screen (tabs shown)
	draw(a *app)
	key(a *app, k key)
}

var tabNames = []string{"Listen Now", "Albums", "Artists", "Playlists", "Radio", "Search", "Settings"}

func newApp(cfg *config, cfgErr error, fb *framebuffer) *app {
	a := &app{cfg: cfg, fb: fb, cv: newCanvas(fb), mpd: newMPD(cfg.mpdSocket),
		meta: map[string]song{}, st: map[string]string{}, covers: map[string]*image.RGBA{},
		scaled: map[string]*image.RGBA{}, avg: map[string]color.RGBA{}, asked: map[string]bool{},
		coverQ: make(chan string, 64), coverC: make(chan coverResult, 16), volC: make(chan int, 4)}
	a.light = cfg.setting("theme", "dark") == "light"
	a.col = makePalette(a.cv, a.light)
	a.ic = loadIcons()
	if cfgErr == nil {
		if c, err := newClient(cfg); err == nil {
			a.api, a.cfgOK = c, true
		} else {
			log.Printf("client: %v", err)
		}
	} else if cfgErr != errNoConfig {
		log.Printf("config: %v", cfgErr)
	}
	a.loadMeta()
	a.loadLibrary()
	a.home = &homeScreen{}
	a.stack = []screen{a.home}
	return a
}

func (a *app) setTheme(light bool) {
	a.light = light
	a.col = makePalette(a.cv, light)
	a.dirty = true
}

// ---------------------------------------------------------------- navigation

func (a *app) top() screen { return a.stack[len(a.stack)-1] }
func (a *app) push(s screen) {
	a.stack = append(a.stack, s)
	a.dirty = true
}
func (a *app) pop() {
	if len(a.stack) > 1 {
		a.stack = a.stack[:len(a.stack)-1]
	} else if a.tab != 0 {
		a.setTab(0)
	} else {
		a.toast("MENU + START to quit")
	}
	a.dirty = true
}

func (a *app) setTab(i int) {
	n := len(tabNames)
	a.tab = (i%n + n) % n
	a.dirty = true
	if !a.cfgOK && a.tab != 6 {
		a.tab = 0
		a.stack = []screen{a.home}
		return
	}
	switch a.tab {
	case 0:
		a.stack = []screen{a.home}
	case 1:
		a.stack = []screen{albumsTab(a)}
	case 2:
		a.stack = []screen{artistsTab(a)}
	case 3:
		a.stack = []screen{playlistsTab(a)}
	case 4:
		a.stack = []screen{radioTab(a)}
	case 5:
		a.stack = []screen{newSearch(a)}
	case 6:
		a.stack = []screen{settingsScreen()}
	}
}

// Notifications take the title's place, top left: they slide up in (the
// title goes up and out), stay, then slide down out (the title comes back).
func (a *app) toast(format string, args ...any) {
	a.notify(fmt.Sprintf(format, args...), 2500*time.Millisecond, false)
}

func (a *app) fail(what string, err error) {
	log.Printf("%s: %v", what, err)
	a.notify(what+": "+err.Error(), 4*time.Second, true)
}

func (a *app) notify(msg string, d time.Duration, isErr bool) {
	now := time.Now()
	if now.Before(a.toastUntil) { // replacing one already shown: no re-entry
		a.toastStart = now.Add(-slide)
	} else {
		a.toastStart = now
	}
	a.toastText, a.toastErr, a.toastUntil = msg, isErr, now.Add(d)
	a.dirty = true
}

const slide = 180 * time.Millisecond

// busy shows a message at once, in the title's place (before a slow call).
func (a *app) busy(msg string) {
	if a.off {
		return
	}
	a.busyMsg = msg
	a.drawScreen()
	a.present()
	a.busyMsg = ""
}

// titleShift: 0 = title shown, 1 = notification shown, eased, in between
// while sliding.
func (a *app) titleShift(now time.Time) float64 {
	if a.busyMsg != "" {
		return 1
	}
	if !now.Before(a.toastUntil) {
		return 0
	}
	in := float64(now.Sub(a.toastStart)) / float64(slide)
	out := float64(a.toastUntil.Sub(now)) / float64(slide)
	v := min(max(min(in, out), 0), 1)
	return 1 - (1-v)*(1-v)*(1-v)
}

func (a *app) sliding(now time.Time) bool {
	return now.Before(a.toastStart.Add(slide)) || (now.Before(a.toastUntil) && now.After(a.toastUntil.Add(-slide)))
}

func (a *app) present() { a.fb.present(a.cv.pixels) }

func fmtTime(sec float64) string {
	s := int(sec + 0.5)
	if s < 0 {
		s = 0
	}
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func readSys(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

func (a *app) readDevice() {
	if a.offline {
		return
	}
	sd := os.Getenv("SDCARD_PATH")
	if sd == "" {
		sd = "/mnt/SDCARD"
	}
	a.status = readStatus(sd, false)
}

var reLineout = regexp.MustCompile(`: values=(\d+)`)

// readVolume reports NextUI's volume as a percentage of "lineout volume"
// (what keymon sets from the keys), after keymon has applied it.
func (a *app) readVolume() {
	go func() {
		time.Sleep(80 * time.Millisecond)
		out, err := exec.Command("amixer", "-c", "0", "cget", "name=lineout volume").Output()
		if err != nil {
			return
		}
		if m := reLineout.FindSubmatch(out); m != nil {
			v, _ := strconv.Atoi(string(m[1]))
			a.volC <- v * 100 / 31
		}
	}()
}

// ---------------------------------------------------------------- chrome

const chromeY, chromeH = 12, 36

// drawStatus: the launcher's own status row (statusicons.go, ported from
// nextui/patches/20-status-dial.patch), with the clock; while a volume key
// is down, a volume bar in the same place.
func (a *app) drawStatus() {
	c, col := a.cv, a.col
	if time.Now().Before(a.volUntil) {
		const vb = 110
		w := 10 + 24 + 10 + vb + 12
		x := 640 - ssEdge - w
		a.statusX = x
		c.pill(x, 2, w, 30, 15, col.chip)
		m := a.ic.volume
		if a.volume == 0 {
			m = a.ic.mute
		}
		c.mask(m, x+10, 2+(30-m.h)/2, col.icon)
		bx := x + 10 + 24 + 10
		c.pill(bx, 13, vb, 8, 4, col.track)
		if a.volume > 0 {
			c.pill(bx, 13, max(8, vb*a.volume/100), 8, 4, col.icon)
		}
		return
	}
	s := a.status
	s.music = a.st["state"] == "play"
	ink := rgbaOf(a, col.fg)
	clockW := 0
	if s.clock != "" {
		clockW = textWidth(&face18, s.clock)
	}
	row, w := statusRow(s, ink, clockW)
	x := 640 - ssEdge - w
	a.statusX = x
	c.blitLayer(row, x, ssEdge)
	if s.clock != "" {
		c.textMid(x+ssPadX, ssEdge, row.h, &face18, s.clock, col.fg)
	}
}

func rgbaOf(a *app, v uint32) rgba {
	f := a.fb
	ch := func(p channel) float32 {
		if p.length == 0 || p.length > 8 {
			return 0
		}
		m := uint32(1)<<p.length - 1
		return float32((v>>p.off)&m) * 255 / float32(m)
	}
	return rgba{ch(f.chanR), ch(f.chanG), ch(f.chanB), 255}
}

// drawTitle: the current screen's name, top left, on the status row's axis;
// a notification slides in over it (titleShift).
func (a *app) drawTitle(s string, root bool) {
	c := a.cv
	const band = ssGlyph + 2*ssPadY
	limit := a.statusX - 18 - 12
	// Restore what the screen drew there (Now Playing's tint, etc.).
	if rows := ssEdge + band + 2; len(a.bandBG) == rows*c.w {
		for y := 0; y < rows; y++ {
			copy(c.pixels[y*c.w:y*c.w+a.statusX], a.bandBG[y*c.w:y*c.w+a.statusX])
		}
	}
	col := a.col.dim
	if root {
		col = a.col.fg
	}
	k := a.titleShift(time.Now())
	off := int(float64(band)*k + 0.5)
	c.clipY0, c.clipY1 = ssEdge, ssEdge+band
	if off < band {
		c.textMid(18, ssEdge-off, band, &face18, truncate(&face18, s, limit), col)
	}
	if off > 0 {
		msg, mcol := a.toastText, a.col.fg
		if a.busyMsg != "" {
			msg = a.busyMsg
		} else if a.toastErr {
			mcol = c.rgb(0xeb, 0x57, 0x57)
		}
		c.textMid(18, ssEdge+band-off, band, &face18, truncate(&face18, msg, limit), mcol)
	}
	c.clipY0, c.clipY1 = 0, 0
}

// animateTitle redraws only the title band (fast frames while sliding).
func (a *app) animateTitle() {
	s := a.top()
	if cr := s.crumb(a); cr == "" {
		a.drawTitle(tabNames[a.tab], true)
	} else {
		a.drawTitle(cr, false)
	}
	a.fb.presentRows(a.cv.pixels, 0, ssEdge+ssGlyph+2*ssPadY+2)
}

// drawHints: NextUI's GFX_blitButtonGroup — one pill per group; a button is
// a color1 circle with its letter in color3, or a color1 pill with a small
// label when it is a word; the hint is in color6.
func (a *app) drawHints(left, right []hint) {
	c, col := a.cv, a.col
	const y = 480 - 10 - chromeH
	btnW := func(b string) int {
		if len([]rune(b)) == 1 {
			return 28
		}
		return textWidth(&face12, b) + 18
	}
	itemW := func(h hint) int { return btnW(h.btn) + 6 + textWidth(&face15, h.label) + 6 }
	groupW := func(hs []hint) int {
		w := 8 + (len(hs)-1)*6
		for _, h := range hs {
			w += itemW(h)
		}
		return w
	}
	draw := func(x int, hs []hint) {
		c.pill(x, y, groupW(hs), chromeH, 18, col.chip)
		x += 4
		for _, h := range hs {
			bw := btnW(h.btn)
			c.pill(x, y+4, bw, 28, 14, col.btn)
			if len([]rune(h.btn)) == 1 {
				c.textMid(x+(bw-textWidth(&face17, h.btn))/2, y+4, 28, &face17, h.btn, col.btnFg)
			} else {
				c.textMid(x+9, y+4, 28, &face12, h.btn, col.btnFg)
			}
			c.textMid(x+bw+6, y, chromeH, &face15, h.label, col.hint)
			x += itemW(h) + 6
		}
	}
	if len(left) > 0 {
		draw(12, left)
	}
	if len(right) > 0 {
		draw(640-12-groupW(right), right)
	}
}

func (a *app) drawScreen() {
	a.cv.rect(0, 0, 640, 480, a.col.bg)
	s := a.top()
	s.draw(a)
	rows := ssEdge + ssGlyph + 2*ssPadY + 2
	a.bandBG = append(a.bandBG[:0], a.cv.pixels[:rows*a.cv.w]...)
	a.drawStatus()
	if cr := s.crumb(a); cr == "" {
		a.drawTitle(tabNames[a.tab], true)
	} else {
		a.drawTitle(cr, false)
	}
}

func (a *app) draw() {
	a.drawScreen()
	a.present()
	a.dirty = false
}

// ---------------------------------------------------------------- loop

func (a *app) handle(k key) {
	if k == kVolUp || k == kVolDown {
		a.volUntil = time.Now().Add(1200 * time.Millisecond)
		a.readVolume()
		a.dirty = true
		return
	}
	if k == kPower || k == kSelect {
		a.setOff(!a.off)
		return
	}
	if a.off {
		switch k {
		case kL1:
			a.skip(-1)
		case kR1:
			a.skip(1)
		case kA:
			a.togglePause()
		}
		return
	}
	switch k {
	case kMenu: // acted on at release: MENU alone, or MENU + START
		a.menuHeld, a.menuCombo = true, false
		return
	case kStart:
		if a.menuHeld { // MENU + START: quit, as everywhere in NextUI
			a.menuCombo, a.quit = true, true
			return
		}
		if _, ok := a.top().(*nowPlaying); !ok {
			if _, isSearch := a.top().(*searchScreen); !isSearch {
				a.push(&nowPlaying{})
				return
			}
		}
	case kL1, kR1:
		if _, isSearch := a.top().(*searchScreen); !isSearch {
			a.skip(map[key]int{kL1: -1, kR1: 1}[k])
			return
		}
	case kL2, kR2:
		if len(a.stack) == 1 {
			a.setTab(a.tab + map[key]int{kL2: -1, kR2: 1}[k])
			return
		}
	}
	a.top().key(a, k)
}

func (a *app) release(k key) {
	if k == kMenu && a.menuHeld {
		a.menuHeld = false
		if !a.menuCombo && !a.off {
			a.menuAction()
		}
	}
}

// menuAction: MENU alone opens the screen chosen in Settings > MENU button.
func (a *app) menuAction() {
	switch a.cfg.setting("menu_button", "search") {
	case "settings":
		a.setTab(6)
	case "home":
		a.setTab(0)
	default:
		a.setTab(5)
	}
}

func (a *app) setOff(off bool) {
	if off == a.off {
		return
	}
	a.off = off
	if off {
		a.cv.rect(0, 0, 640, 480, 0)
		a.present()
	}
	if err := a.fb.blank(off); err != nil {
		log.Printf("blank: %v", err)
	}
	a.dirty = !off
	if !off {
		a.readDevice()
		a.refresh()
	}
}

func (a *app) run() error {
	keys := make(chan keyEvent, 64)
	if err := listenInput(keys); err != nil {
		return err
	}
	go a.coverWorker()
	go a.coverWorker()
	a.readDevice()
	a.refresh()
	a.home.load(a)
	a.draw()
	a.maybeRefresh()
	rep := newRepeater()
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	lastPoll, lastDev, lastLib := time.Now(), time.Now(), time.Now()
	shown := false
	for !a.quit {
		select {
		case ev := <-keys:
			rep.update(ev, time.Now())
			if ev.pressed {
				a.handle(ev.k)
			} else {
				a.release(ev.k)
			}
		case now := <-tick.C:
			for _, k := range rep.due(now) {
				if !a.off {
					a.handle(k)
				}
			}
			if now.Sub(lastPoll) >= pollEach {
				lastPoll = now
				a.refresh()
				a.dirty = true // progress bars, status note
			}
			if now.Sub(lastDev) >= 5*time.Second {
				lastDev = now
				a.readDevice()
			}
			if now.Sub(lastLib) >= time.Minute {
				lastLib = now
				a.maybeRefresh()
			}
			busy := now.Before(a.toastUntil) || now.Before(a.volUntil)
			if busy != shown {
				shown = busy
				a.dirty = true
			}
			if !a.dirty && !a.off && a.sliding(now) {
				a.animateTitle()
			}
		case res := <-a.libR.c:
			a.libraryRefreshed(res)
		case v := <-a.volC:
			a.volume = v
			a.dirty = true
		case r := <-a.coverC:
			if r.img == nil { // no cover: the placeholder stays
				break
			}
			if len(a.covers) > 80 {
				a.covers, a.scaled = map[string]*image.RGBA{}, map[string]*image.RGBA{}
			}
			a.covers[r.id] = r.img
			a.avg[r.id] = average(r.img)
			a.dirty = true
		}
		if a.dirty && !a.off && !a.quit {
			a.draw()
		}
	}
	if a.off {
		a.fb.blank(false)
	}
	return nil
}

// ---------------------------------------------------------------- covers

// coverAt returns the cover id scaled to size (area average), or nil while
// it loads; the first call queues the download.
func (a *app) coverAt(id string, size int) *image.RGBA {
	if id == "" {
		return nil
	}
	k := id + "@" + strconv.Itoa(size)
	if img, ok := a.scaled[k]; ok {
		return img
	}
	if src, ok := a.covers[id]; ok {
		img := src
		if src.Bounds().Dx() != size || src.Bounds().Dy() != size {
			img = boxScale(src, size, size)
		}
		a.scaled[k] = img
		return img
	}
	if !a.asked[id] && a.api != nil && !a.offline {
		select {
		case a.coverQ <- id:
			a.asked[id] = true
		default:
		}
	}
	return nil
}

func (a *app) coverWorker() {
	for id := range a.coverQ {
		img, err := a.fetchCover(id)
		if err != nil {
			log.Printf("cover %s: %v", id, err)
		}
		a.coverC <- coverResult{id, img}
	}
}

// drawCover draws a cover; without one (loading, or none anywhere) a tile
// in a color of its own with the name's initials, as for radios.
func (a *app) drawCover(id, name string, x, y, size, r int) {
	if img := a.coverAt(id, size); img != nil {
		a.cv.cover(img, x, y, r)
		return
	}
	a.cv.pill(x, y, size, size, r, tileColor(a.cv, name))
	f := &face30
	switch {
	case size < 44:
		f = &face15
	case size < 80:
		f = &face21
	}
	ini := initials(name)
	if ini == "" || name == unknownAlbum || name == unknownArtist || name == untitled {
		ini = "♪" // nothing to spell: a note, as commercial players do
	}
	a.cv.textMid(x+(size-textWidth(f, ini))/2, y, size, f, ini, a.cv.rgb(255, 255, 255))
}

func (a *app) tintOf(id string) (uint32, bool) {
	c, ok := a.avg[id]
	if !ok {
		return 0, false
	}
	return a.cv.rgb(c.R, c.G, c.B), true
}

func coverID(s song) string {
	if s.CoverArt != "" {
		return string(s.CoverArt)
	}
	return string(s.AlbumID)
}
