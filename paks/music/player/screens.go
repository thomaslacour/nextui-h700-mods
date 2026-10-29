package main

// Screens (v3 design): Listen Now, tab lists, album, Now Playing, Up Next,
// settings. Search lives in search.go.

import (
	"fmt"
	"math/rand"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------- lists

const listTop, rowH, visRows = 44, 46, 8

type row struct {
	label, value string
	cover        string // cover id: thumbnail shown when the list has thumbs
	now          bool
	open         func(a *app)
	onY          func(a *app)
	set          *settingRow
}

type settingRow struct {
	key, def string
	values   []string
	labels   []string
	changed  func(a *app, v string)
}

func (s *settingRow) index(a *app) int {
	v := a.cfg.setting(s.key, s.def)
	for i, x := range s.values {
		if x == v {
			return i
		}
	}
	return 0
}

type listScreen struct {
	title    string // crumb; "" on a tab root (tabs shown instead)
	rows     []row
	sel, off int
	wide     bool
	thumbs   bool
	left     []hint
	right    []hint
	refresh  func(a *app) []row
	empty    string
	selFirst func(a *app) int // initial selection (Up Next: the current song)
}

func (l *listScreen) crumb(a *app) string { return l.title }

func (l *listScreen) clamp() {
	l.sel = max(0, min(l.sel, len(l.rows)-1))
	if l.sel < l.off {
		l.off = l.sel
	}
	if l.sel >= l.off+visRows {
		l.off = l.sel - visRows + 1
	}
	l.off = max(0, l.off)
}

func (l *listScreen) draw(a *app) {
	if l.refresh != nil {
		l.rows = l.refresh(a)
	}
	if l.selFirst != nil {
		l.sel, l.selFirst = l.selFirst(a), nil
	}
	l.clamp()
	c, col := a.cv, a.col
	if len(l.rows) == 0 {
		msg := l.empty
		if msg == "" {
			msg = "Nothing here"
		}
		c.textMid(34, listTop, rowH, &face24, msg, col.dim)
	}
	for i := 0; i < visRows && l.off+i < len(l.rows); i++ {
		r := l.rows[l.off+i]
		y := listTop + i*rowH
		sel := l.off+i == l.sel
		value := r.value
		if r.set != nil {
			value = r.set.labels[r.set.index(a)]
			if sel {
				value = "‹ " + value + " ›"
			}
		}
		x := 12
		if l.thumbs {
			a.drawCover(r.cover, r.label, x+8, y+5, 36, 8)
			x += 8 + 36 + 12
		}
		right := 640 - 18 - 18
		vw := 0
		if value != "" {
			value = truncate(&face17, value, 220)
			vw = textWidth(&face17, value) + 14
		}
		pad := 18
		if l.thumbs {
			pad = 14
		}
		label := r.label
		if r.now {
			label = "▶ " + label
		}
		label = truncate(&face24, label, right-vw-x-2*pad)
		lw := textWidth(&face24, label)
		fg, vfg := col.fg, col.dim
		if sel {
			if l.wide {
				c.pill(12, y+3, 640-12-18, 40, 20, col.pill)
				vfg = col.pillFg
			} else {
				c.pill(x, y+3, lw+2*pad, 40, 20, col.pill)
			}
			fg = col.pillFg
		}
		c.textMid(x+pad, y+3, 40, &face24, label, fg)
		if value != "" {
			c.textMid(right-vw+14, y+3, 40, &face17, value, vfg)
		}
	}
	if len(l.rows) > visRows {
		const H = visRows*rowH - 8
		bh := max(20, H*visRows/len(l.rows))
		by := (H - bh) * l.off / (len(l.rows) - visRows)
		c.pill(630, listTop+4, 4, H, 2, col.track)
		c.pill(630, listTop+4+by, 4, bh, 2, col.dim)
	}
	left, right := l.left, l.right
	if left == nil && l.title == "" {
		left = []hint{{"L2", "TABS"}}
	}
	if right == nil {
		right = []hint{{"B", "BACK"}, {"A", "OPEN"}}
	}
	a.drawHints(left, right)
}

func (l *listScreen) key(a *app, k key) {
	if len(l.rows) == 0 {
		if k == kB {
			a.pop()
		}
		return
	}
	r := &l.rows[min(l.sel, len(l.rows)-1)]
	switch k {
	case kUp:
		l.sel = (l.sel - 1 + len(l.rows)) % len(l.rows)
	case kDown:
		l.sel = (l.sel + 1) % len(l.rows)
	case kLeft, kRight:
		if r.set != nil {
			n := len(r.set.values)
			i := r.set.index(a)
			if k == kRight {
				i = (i + 1) % n
			} else {
				i = (i + n - 1) % n
			}
			v := r.set.values[i]
			if err := a.cfg.setSetting(r.set.key, v); err != nil {
				a.fail("Cannot save setting", err)
			}
			if r.set.changed != nil {
				r.set.changed(a, v)
			}
		} else if k == kLeft {
			l.sel = max(0, l.sel-visRows)
		} else {
			l.sel = min(len(l.rows)-1, l.sel+visRows)
		}
	case kA:
		if r.open != nil {
			r.open(a)
		} else if r.set != nil {
			l.key(a, kRight)
		}
	case kY:
		if r.onY != nil {
			r.onY(a)
		}
	case kB:
		a.pop()
	}
	a.dirty = true
}

func countLabel(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// ---------------------------------------------------------------- tabs

func albumRows(albums []album) []row {
	rows := make([]row, 0, len(albums))
	for _, al := range albums {
		al := al
		rows = append(rows, row{label: al.name(), value: al.Artist, cover: string(al.CoverArt),
			open: func(a *app) { openAlbum(a, al) },
			onY: func(a *app) {
				full, err := a.api.album(string(al.ID))
				if err != nil {
					a.fail("Cannot load", err)
					return
				}
				a.enqueue(full.Song)
			}})
	}
	return rows
}

func albumsTab(a *app) screen {
	l := &listScreen{thumbs: true, empty: "No albums", right: []hint{{"Y", "UP NEXT"}, {"A", "OPEN"}}, left: []hint{{"L2", "TABS"}}}
	a.ensure("albums", "Loading albums…")
	l.rows = albumRows(a.lib.Albums)
	return l
}

func artistsTab(a *app) screen {
	l := &listScreen{empty: "No artists"}
	a.ensure("artists", "Loading artists…")
	for _, ar := range a.lib.Artists {
		ar := ar
		l.rows = append(l.rows, row{label: ar.Name, value: countLabel(int(ar.AlbumCount), "album", "albums"),
			open: func(a *app) { openArtist(a, string(ar.ID), ar.Name) }})
	}
	return l
}

func openArtist(a *app, id, name string) {
	a.busy("Loading…")
	ar, err := a.api.artist(id)
	if err != nil {
		a.fail("Cannot load", err)
		return
	}
	a.push(&listScreen{title: name, rows: albumRows(ar.Album), thumbs: true, empty: "No albums",
		right: []hint{{"Y", "UP NEXT"}, {"A", "OPEN"}}, left: []hint{{"B", "BACK"}}})
}

func playlistsTab(a *app) screen {
	l := &listScreen{thumbs: true, empty: "No playlists"}
	a.ensure("playlists", "Loading playlists…")
	for _, p := range a.lib.Playlists {
		p := p
		l.rows = append(l.rows, row{label: p.Name, value: countLabel(int(p.SongCount), "song", "songs"), cover: string(p.CoverArt),
			open: func(a *app) {
				a.busy("Loading…")
				full, err := a.api.playlist(string(p.ID))
				if err != nil {
					a.fail("Cannot load", err)
					return
				}
				a.push(songList(p.Name, full.Entry))
			}})
	}
	// Starred songs, as a playlist (Subsonic keeps them apart).
	l.rows = append(l.rows, row{label: "Starred", value: "★", open: func(a *app) {
		a.busy("Loading…")
		songs, err := a.starredSongs()
		if err != nil {
			a.fail("Cannot load", err)
			return
		}
		a.push(songList("Starred", songs))
	}})
	return l
}

// songList: Play All, Shuffle, then the songs. A plays from that song.
func songList(title string, songs []song) *listScreen {
	rows := []row{
		{label: "Play All", value: countLabel(len(songs), "song", "songs"), open: func(a *app) { a.play(songs, 0) }},
		{label: "Shuffle", open: func(a *app) { a.play(shuffled(songs), 0) }},
	}
	for i, s := range songs {
		i, s := i, s
		rows = append(rows, row{label: s.Title, value: s.Artist, cover: coverID(s),
			open: func(a *app) { a.play(songs, i) },
			onY:  func(a *app) { a.enqueue([]song{s}) }})
	}
	if len(songs) == 0 {
		rows = nil
	}
	return &listScreen{title: title, rows: rows, empty: "No songs", left: []hint{{"Y", "UP NEXT"}},
		right: []hint{{"B", "BACK"}, {"A", "PLAY"}}}
}

// ---------------------------------------------------------------- listen now

// homeScreen: shelves of tiles, scrolled to keep the selected one in view:
// Recently Added, Most Played, Radio (recent then favorites), Artists to
// explore (a random dozen, shown with one of their albums).
type homeScreen struct {
	sel     int
	it      [4]int
	loaded  bool
	explore []artist
}

type tile struct {
	title, sub string
	cover      string // cover id, or "" for a drawn tile
	label      string // drawn tile: initials
	open, onY  func(a *app)
}

func (h *homeScreen) load(a *app) {
	if h.loaded || a.api == nil {
		return
	}
	a.ensure("recent", "Loading…")
	a.ensure("frequent", "Loading…")
	a.ensure("artists", "Loading…")
	a.ensure("albums", "Loading…")
	if a.favorites == nil {
		a.favorites = a.stations()
	}
	h.explore = nil
	for _, i := range rand.Perm(len(a.lib.Artists)) {
		if len(h.explore) == 12 {
			break
		}
		h.explore = append(h.explore, a.lib.Artists[i])
	}
	h.loaded = true
}

func (h *homeScreen) crumb(a *app) string { return "" }

func albumTiles(albums []album) []tile {
	out := make([]tile, 0, len(albums))
	for _, al := range albums {
		al := al
		out = append(out, tile{title: al.name(), sub: al.Artist, cover: string(al.CoverArt),
			open: func(a *app) { openAlbum(a, al) },
			onY:  func(a *app) { albumRows([]album{al})[0].onY(a) }})
	}
	return out
}

func initials(name string) string {
	var out []rune
	for _, w := range splitWords(name) {
		r := []rune(w)
		if len(r) > 0 && len(out) < 2 {
			out = append(out, r[0])
		}
	}
	return strings.ToUpper(string(out))
}

func (h *homeScreen) shelves(a *app) ([4]string, [4][]tile) {
	var radios []tile
	seen := map[string]bool{}
	for _, s := range append(a.recentRadios(), a.favorites...) {
		if seen[s.url] || len(radios) == 12 {
			continue
		}
		seen[s.url] = true
		s := s
		radios = append(radios, tile{title: s.name, sub: "Radio", label: initials(s.name),
			open: func(a *app) { a.playRadio(s) }, onY: func(a *app) { a.addFavorite(s) }})
	}
	firstAlbum := map[string]album{}
	for _, al := range a.lib.Albums {
		if _, ok := firstAlbum[al.Artist]; !ok {
			firstAlbum[al.Artist] = al
		}
	}
	var artists []tile
	for _, ar := range h.explore {
		ar := ar
		t := tile{title: ar.Name, sub: countLabel(int(ar.AlbumCount), "album", "albums"), label: initials(ar.Name),
			open: func(a *app) { openArtist(a, string(ar.ID), ar.Name) }}
		if al, ok := firstAlbum[ar.Name]; ok {
			t.cover = string(al.CoverArt)
		}
		artists = append(artists, t)
	}
	return [4]string{"Recently Added", "Most Played", "Radio", "Artists to explore"},
		[4][]tile{albumTiles(a.lib.Recent), albumTiles(a.lib.Frequent), radios, artists}
}

// tileColor: a stable, muted color per name (drawn tiles).
func tileColor(c *canvas, name string) uint32 {
	var h uint32 = 2166136261
	for _, b := range []byte(name) {
		h = (h ^ uint32(b)) * 16777619
	}
	hues := [][3]uint8{{0x8e, 0x3b, 0x46}, {0x2f, 0x5d, 0x8a}, {0x3d, 0x7a, 0x5a}, {0x7a, 0x5c, 0x2e}, {0x5b, 0x3f, 0x8c}, {0x2e, 0x6f, 0x73}, {0x8a, 0x4b, 0x2f}}
	p := hues[h%uint32(len(hues))]
	return c.rgb(p[0], p[1], p[2])
}

const shelfH, tileS, tileGap = 186, 100, 14

func (h *homeScreen) draw(a *app) {
	c, col := a.cv, a.col
	if !a.cfgOK {
		c.text(34, 90, &face24, "Not signed in", col.fg)
		c.text(34, 130, &face17, "Settings > Server, User, Password", col.dim)
		a.drawHints([]hint{{"L2", "TABS"}}, []hint{{"MENU+START", "QUIT"}})
		return
	}
	titles, lists := h.shelves(a)
	// Two shelves fit: scroll so the selected one is in view.
	scroll := max(0, h.sel-1) * shelfH
	for si := 0; si < 4; si++ {
		y := 44 + si*shelfH - scroll
		if y > 430 || y+shelfH < 40 {
			continue
		}
		c.text(20, y, &face24, titles[si], col.fg)
		ty := y + 38
		tiles := lists[si]
		idx := h.it[si]
		shift := max(0, min(idx*(tileS+tileGap)-(tileS+tileGap), len(tiles)*(tileS+tileGap)-600))
		for i, t := range tiles {
			tx := 20 + i*(tileS+tileGap) - shift
			if tx > 640 || tx+tileS < 0 {
				continue
			}
			sel := h.sel == si && i == idx
			if sel {
				c.ring(tx-3, ty-3, tileS+6, tileS+6, 13, 3, col.pill, col.bg)
			}
			a.drawCover(t.cover, t.title, tx, ty, tileS, 10)
			if sel {
				name := truncate(&face15, t.title, tileS-12)
				c.pill(tx, ty+tileS+6, textWidth(&face15, name)+14, 24, 12, col.pill)
				c.textMid(tx+7, ty+tileS+6, 24, &face15, name, col.pillFg)
			} else {
				c.textMid(tx+2, ty+tileS+6, 24, &face15, truncate(&face15, t.title, tileS-4), col.fg)
			}
			c.text(tx+2, ty+tileS+31, &face12, truncate(&face12, t.sub, tileS-4), col.dim)
		}
		if len(tiles) == 0 {
			c.text(20, ty+30, &face17, "Nothing yet", col.dim)
		}
	}
	// Hide what scrolled under the title row or the button hints.
	c.rect(0, 0, 640, 40, col.bg)
	c.rect(0, 422, 640, 58, col.bg)
	a.drawHints([]hint{{"L2", "TABS"}}, []hint{{"START", "NOW PLAYING"}, {"A", "OPEN"}})
}

func (h *homeScreen) key(a *app, k key) {
	_, lists := h.shelves(a)
	cur := lists[h.sel]
	i := h.it[h.sel]
	switch k {
	case kUp:
		h.sel = max(0, h.sel-1)
	case kDown:
		h.sel = min(3, h.sel+1)
	case kLeft:
		h.it[h.sel] = max(0, i-1)
	case kRight:
		h.it[h.sel] = max(0, min(len(cur)-1, i+1))
	case kA:
		if i < len(cur) && cur[i].open != nil {
			cur[i].open(a)
		}
	case kY:
		if i < len(cur) && cur[i].onY != nil {
			cur[i].onY(a)
		}
	case kB:
		a.pop()
	}
	a.dirty = true
}

// ---------------------------------------------------------------- album

type albumScreen struct {
	al       *album
	sel, off int // 0 Play, 1 Shuffle, 2.. songs
}

func openAlbum(a *app, al album) {
	a.busy("Loading…")
	full, err := a.api.album(string(al.ID))
	if err != nil {
		a.fail("Cannot load", err)
		return
	}
	a.push(&albumScreen{al: full})
}

func (s *albumScreen) crumb(a *app) string {
	t := s.al.Artist
	if s.al.Year > 0 {
		t += " · " + strconv.Itoa(int(s.al.Year))
	}
	return t
}

func (s *albumScreen) draw(a *app) {
	c, col := a.cv, a.col
	al := s.al
	a.drawCover(string(al.CoverArt), al.name(), 20, 47, 104, 10)
	tx, tw := 140, 640-140-20
	c.text(tx, 48, &face24, truncate(&face24, al.name(), tw), col.fg)
	c.text(tx, 80, &face17, truncate(&face17, fmt.Sprintf("%s · %s", al.Artist, countLabel(len(al.Song), "song", "songs")), tw), col.dim)
	bx := tx
	for i, label := range []string{"Play", "Shuffle"} {
		bw := textWidth(&face15, label) + 28 + 18
		bg, fg := col.chip, col.fg
		if s.sel == i {
			bg, fg = col.pill, col.pillFg
		}
		c.pill(bx, 112, bw, 30, 15, bg)
		if i == 0 {
			c.triangle(bx+14, 121, 10, 12, fg)
		} else if _, ok := face15.glyphs['⇄']; ok {
			c.textMid(bx+11, 112, 30, &face15, "⇄", fg)
		}
		c.textMid(bx+32, 112, 30, &face15, label, fg)
		bx += bw + 8
	}
	const top, vis = 160, 5
	ts := max(0, s.sel-2)
	if ts < s.off {
		s.off = ts
	}
	if ts >= s.off+vis {
		s.off = ts - vis + 1
	}
	for i := 0; i < vis && s.off+i < len(al.Song); i++ {
		n := s.off + i
		so := al.Song[n]
		y := top + i*rowH
		num := n + 1
		if so.Track > 0 {
			num = int(so.Track)
		}
		v := fmtTime(float64(so.Duration))
		vw := textWidth(&face17, v)
		label := truncate(&face24, strconv.Itoa(num)+". "+so.Title, 640-36-vw-30-36)
		fg := col.fg
		if s.sel == n+2 {
			c.pill(12, y+3, textWidth(&face24, label)+36, 40, 20, col.pill)
			fg = col.pillFg
		}
		c.textMid(30, y+3, 40, &face24, label, fg)
		c.textMid(640-36-vw, y+3, 40, &face17, v, col.dim)
	}
	label := "PLAY"
	if s.sel < 2 {
		label = "START"
	}
	a.drawHints([]hint{{"Y", "UP NEXT"}}, []hint{{"B", "BACK"}, {"A", label}})
}

func (s *albumScreen) key(a *app, k key) {
	songs := s.al.Song
	n := len(songs) + 2
	switch k {
	case kUp:
		if s.sel == 1 {
			s.sel = 0
		} else {
			s.sel = max(0, s.sel-1)
		}
	case kDown:
		if s.sel == 0 {
			s.sel = min(2, n-1)
		} else {
			s.sel = min(n-1, s.sel+1)
		}
	case kLeft:
		if s.sel == 1 {
			s.sel = 0
		}
	case kRight:
		if s.sel == 0 {
			s.sel = 1
		}
	case kA:
		switch s.sel {
		case 0:
			a.play(songs, 0)
		case 1:
			a.play(shuffled(songs), 0)
		default:
			a.play(songs, s.sel-2)
		}
	case kY:
		if s.sel >= 2 {
			a.enqueue([]song{songs[s.sel-2]})
		} else {
			a.enqueue(songs)
		}
	case kB:
		a.pop()
	}
	a.dirty = true
}

// ---------------------------------------------------------------- settings

func settingsScreen() *listScreen {
	l := &listScreen{wide: true, left: []hint{{"←→", "CHANGE"}}}
	l.refresh = func(a *app) []row {
		rows := []row{
			{label: "Theme", set: &settingRow{key: "theme", def: "dark", values: []string{"dark", "light"}, labels: []string{"Dark", "Light"},
				changed: func(a *app, v string) { a.setTheme(v == "light") }}},
			{label: "Quality", set: &settingRow{key: "quality", def: "original",
				values:  []string{"original", "320", "192", "128"},
				labels:  []string{"Original", "MP3 320 kbps", "MP3 192 kbps", "MP3 128 kbps"},
				changed: func(a *app, v string) { a.toast("Applies from the next song") }}},
			{label: "Online covers", set: &settingRow{key: "online_covers", def: "on",
				values: []string{"on", "off"}, labels: []string{"MusicBrainz", "Off"}}},
			{label: "MENU button", set: &settingRow{key: "menu_button", def: "search",
				values: []string{"search", "settings", "home"}, labels: []string{"Search", "Settings", "Listen Now"}}},
			{label: "After exit", set: &settingRow{key: "after_exit", def: "keep",
				values: []string{"keep", "stop"}, labels: []string{"Keep playing", "Stop music"}}},
		}
		rows = append(rows, accountRows(a)...)
		if a.cfgOK {
			rows = append(rows,
				row{label: "Test connection", open: testServer},
				row{label: "Refresh library", value: libAge(a), open: func(a *app) { a.refreshLibrary() }})
		}
		return rows
	}
	return l
}

func libAge(a *app) string {
	if a.lib.Saved.IsZero() {
		return ""
	}
	m := int(time.Since(a.lib.Saved).Minutes())
	if m < 60 {
		return fmt.Sprintf("%d min ago", m)
	}
	return fmt.Sprintf("%d h ago", m/60)
}

func testServer(a *app) {
	a.busy("Connecting…")
	r, err := a.api.ping()
	if err != nil {
		a.fail("Server", err)
		return
	}
	name := r.Type
	if name == "" {
		name = "Subsonic"
	}
	a.toast("Connected · %s %s", name, r.Version)
}

// ---------------------------------------------------------------- up next

func queueScreen(a *app) *listScreen {
	l := &listScreen{thumbs: true, right: []hint{{"B", "BACK"}, {"A", "PLAY"}}, left: []hint{{"Y", "REMOVE"}},
		empty: "Up Next is empty"}
	l.selFirst = func(a *app) int { n, _ := strconv.Atoi(a.st["song"]); return n }
	l.refresh = func(a *app) []row {
		q, err := a.mpd.queue()
		if err != nil {
			return nil
		}
		curPos, _ := strconv.Atoi(a.st["song"])
		l.title = "Up Next · " + countLabel(len(q), "song", "songs")
		rows := make([]row, 0, len(q))
		for _, e := range q {
			e := e
			r := row{label: e.title, value: e.artist, now: e.pos == curPos && a.curID != ""}
			if u, err := url.Parse(e.file); err == nil {
				if id, ok := streamID(u.Path); ok {
					if s, ok := a.meta[id]; ok {
						r.label, r.value, r.cover = s.Title, s.Artist, coverID(s)
					}
				}
			}
			if r.label == "" {
				r.label = "Unknown song"
			}
			r.open = func(a *app) {
				if err := a.mpd.simple("play " + strconv.Itoa(e.pos)); err != nil {
					a.fail("Player", err)
				}
				a.refresh()
				a.pop()
			}
			r.onY = func(a *app) {
				if e.pos == curPos {
					a.toast("Playing now: R1 skips it")
					return
				}
				if err := a.mpd.simple("delete " + strconv.Itoa(e.pos)); err != nil {
					a.fail("Player", err)
				}
			}
			rows = append(rows, r)
		}
		return rows
	}
	l.title = "Up Next"
	return l
}

// ---------------------------------------------------------------- now playing

type nowPlaying struct{}

func (n *nowPlaying) crumb(a *app) string { return "Now Playing" }

func (n *nowPlaying) draw(a *app) {
	c, col := a.cv, a.col
	if a.curID == "" {
		msg := "Nothing playing"
		if !a.mpdOK {
			msg = "Player not running"
		}
		c.text(40, 150, &face30, msg, col.fg)
		c.text(40, 200, &face17, "Pick an album, a playlist or Shuffle.", col.dim)
		a.drawHints(nil, []hint{{"B", "BACK"}})
		return
	}
	s := a.cur
	id := coverID(s)
	if t, ok := a.tintOf(id); ok {
		c.tint(0, 0, 640, 480, 0, t, .55, .75, .35)
	}
	a.drawCover(id, s.Album, 34, 66, 230, 16)
	if a.st["state"] == "pause" {
		dark := c.rgb(0, 0, 0)
		for y := 0; y < 230; y++ {
			for x := 0; x < 230; x++ {
				c.plot(34+x, 66+y, dark, uint8(int(roundAlpha(x, y, 230, 230, 16))*2/5))
			}
		}
		c.pill(34+95, 66+80, 18, 70, 6, col.fg)
		c.pill(34+117, 66+80, 18, 70, 6, col.fg)
	}
	x, w := 290, 640-290-30
	pos, _ := strconv.Atoi(a.st["song"])
	c.text(x, 74, &face15, fmt.Sprintf("%d / %s", pos+1, a.st["playlistlength"]), col.dim)
	y := 96
	for _, line := range wrap(&face30, s.Title, w, 2) {
		c.text(x, y, &face30, line, col.fg)
		y += 34
	}
	y += 6
	c.text(x, y, &face21, truncate(&face21, s.Artist, w), col.fg)
	y += 28
	c.text(x, y, &face18, truncate(&face18, s.Album, w), col.dim)
	y += 36
	fx := x
	for _, f := range []struct {
		label string
		on    bool
	}{{"SHUFFLE", a.st["random"] == "1"}, {"REPEAT", a.st["repeat"] == "1"}, {starLabel(s), s.Starred != ""}} {
		if isRadio(s) {
			break
		}
		fw := textWidth(&face13, f.label) + 24
		bg, fg := col.chip, col.dim
		if f.on {
			bg, fg = col.pill, col.pillFg
		}
		c.pill(fx, y, fw, 28, 14, bg)
		c.textMid(fx+12, y, 28, &face13, f.label, fg)
		fx += fw + 8
	}
	y += 40
	c.text(x, y, &face13, truncate(&face13, qualityLine(a, s), w), col.dim)

	el, du := a.elapsed(), a.duration()
	const bx, bw, by = 34, 572, 330
	if isRadio(s) {
		c.pill(bx, by, bw, 6, 3, col.track)
		c.text(bx, by+16, &face17, fmtTime(el), col.dim)
		lw := textWidth(&face13, "LIVE") + 36
		c.pill(bx+bw-lw, by+14, lw, 26, 13, col.pill)
		c.pill(bx+bw-lw+12, by+14+9, 8, 8, 4, c.rgb(0xeb, 0x57, 0x57)) // red dot
		c.textMid(bx+bw-lw+24, by+14, 26, &face13, "LIVE", col.pillFg)
		c.textMid(320-textWidth(&face13, "RADIO")/2, by+16, 22, &face13, "RADIO", col.fg)
		label := "STOP"
		if a.st["state"] != "play" {
			label = "PLAY"
		}
		a.drawHints([]hint{{"SELECT", "SCREEN OFF"}}, []hint{{"B", "BACK"}, {"A", label}})
		return
	}
	c.pill(bx, by, bw, 6, 3, col.track)
	kx := bx
	if du > 0 {
		kx = bx + int(float64(bw)*min(el/du, 1))
		c.pill(bx, by, max(6, kx-bx), 6, 3, col.fill)
	}
	c.pill(kx-9, by-6, 18, 18, 9, col.fill)
	c.text(bx, by+16, &face17, fmtTime(el), col.dim)
	if du > 0 {
		r := "-" + fmtTime(du-el)
		c.text(bx+bw-textWidth(&face17, r), by+16, &face17, r, col.dim)
	}
	c.textMid(320-textWidth(&face13, "NAVIDROME")/2, by+16, 22, &face13, "NAVIDROME", col.fg)
	label := "PAUSE"
	if a.st["state"] != "play" {
		label = "PLAY"
	}
	a.drawHints([]hint{{"L1", "PREV"}, {"R1", "NEXT"}}, []hint{{"X", "UP NEXT"}, {"A", label}})
}

func starLabel(s song) string {
	if s.Starred != "" {
		return "★ STARRED"
	}
	return "☆ STAR"
}

func qualityLine(a *app, s song) string {
	if q := a.cfg.setting("quality", "original"); q != "original" {
		return "MP3 · " + q + " kbps (if the server converts)"
	}
	var parts []string
	if s.Suffix != "" {
		parts = append(parts, strings.ToUpper(s.Suffix))
	}
	if s.BitRate > 0 {
		parts = append(parts, strconv.Itoa(int(s.BitRate))+" kbps")
	}
	if f := a.st["audio"]; f != "" {
		if rate, _, ok := strings.Cut(f, ":"); ok {
			if r, err := strconv.Atoi(rate); err == nil && r > 0 {
				parts = append(parts, fmt.Sprintf("%.1f kHz", float64(r)/1000))
			}
		}
	}
	if len(parts) == 0 {
		return "Streaming"
	}
	return strings.Join(parts, " · ")
}

func (n *nowPlaying) key(a *app, k key) {
	switch k {
	case kA:
		a.togglePause()
	case kB:
		a.pop()
	case kLeft:
		a.seek(-10)
	case kRight:
		a.seek(10)
	case kX:
		a.push(queueScreen(a))
	case kY:
		a.toggleStar()
	case kL2:
		a.toggleFlag("random")
	case kR2:
		a.toggleFlag("repeat")
	}
	a.dirty = true
}
