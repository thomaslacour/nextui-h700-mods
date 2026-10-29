package main

// Search with a predictive letter strip: the library's names (artists,
// albums, and songs when the server lists them) are indexed once; after
// each letter only the letters that continue at least one name, at the start
// of a word, are offered. One pass over a few thousand names: well under a
// millisecond on the H700.

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

type indexEntry struct {
	norm   string // uppercase, no accents
	label  string
	value  string
	cover  string
	album  *album
	artist *artist
	song   *song
}

type libIndex struct {
	entries []indexEntry
	songs   []song
}

var accents = map[rune]rune{}

func init() {
	groups := map[rune]string{
		'A': "ÀÁÂÃÄÅĀĂĄàáâãäåāăą", 'C': "ÇĆĈĊČçćĉċč", 'D': "ĎĐďđ", 'E': "ÈÉÊËĒĔĖĘĚèéêëēĕėęě",
		'G': "ĜĞĠĢĝğġģ", 'H': "ĤĦĥħ", 'I': "ÌÍÎÏĨĪĬĮİìíîïĩīĭįı", 'J': "Ĵĵ", 'K': "Ķķ", 'L': "ĹĻĽĿŁĺļľŀł",
		'N': "ÑŃŅŇñńņň", 'O': "ÒÓÔÕÖØŌŎŐòóôõöøōŏő", 'R': "ŔŖŘŕŗř", 'S': "ŚŜŞŠśŝşšß", 'T': "ŢŤŦţťŧ",
		'U': "ÙÚÛÜŨŪŬŮŰŲùúûüũūŭůűų", 'W': "Ŵŵ", 'Y': "ÝŶŸýÿŷ", 'Z': "ŹŻŽźżž",
	}
	for base, s := range groups {
		for _, r := range s {
			accents[r] = base
		}
	}
}

func normalize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if base, ok := accents[r]; ok {
			r = base
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

func isWordByte(c byte) bool { return c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' }

// wordAt reports whether q occurs in n at the start of a word.
func wordAt(n, q string) bool {
	for i := strings.Index(n, q); i >= 0; {
		if i == 0 || !isWordByte(n[i-1]) {
			return true
		}
		j := strings.Index(n[i+1:], q)
		if j < 0 {
			break
		}
		i += 1 + j
	}
	return false
}

const space, backspace = '␣', '⌫'

// nextLetters: what may follow q (A-Z, 0-9, ␣), then ⌫ when q is not empty.
func (ix *libIndex) nextLetters(q string) []rune {
	Q := normalize(q)
	seen := map[byte]bool{}
	for _, e := range ix.entries {
		n := e.norm
		if Q == "" {
			for j := 0; j < len(n); j++ {
				if isWordByte(n[j]) && (j == 0 || !isWordByte(n[j-1])) {
					seen[n[j]] = true
				}
			}
			continue
		}
		for i := strings.Index(n, Q); i >= 0; {
			if i == 0 || !isWordByte(n[i-1]) {
				if k := i + len(Q); k < len(n) {
					if isWordByte(n[k]) {
						seen[n[k]] = true
					} else if n[k] == ' ' {
						seen[' '] = true
					}
				}
			}
			j := strings.Index(n[i+1:], Q)
			if j < 0 {
				break
			}
			i += 1 + j
		}
	}
	var out []rune
	for _, c := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" {
		if seen[byte(c)] {
			out = append(out, c)
		}
	}
	if seen[' '] {
		out = append(out, space)
	}
	if q != "" {
		out = append(out, backspace)
	}
	return out
}

func (ix *libIndex) find(q string) []indexEntry {
	Q := normalize(strings.TrimSpace(q))
	if Q == "" {
		return nil
	}
	var out []indexEntry
	for _, e := range ix.entries {
		if wordAt(e.norm, Q) {
			out = append(out, e)
		}
	}
	return out
}

func buildIndex(a *app) (*libIndex, error) {
	ix := &libIndex{}
	a.ensure("artists", "Indexing library…")
	a.ensure("albums", "Indexing library…")
	a.ensure("songs", "Indexing library…")
	artists, albums := a.lib.Artists, a.lib.Albums
	for i := range artists {
		ar := &artists[i]
		ix.entries = append(ix.entries, indexEntry{norm: normalize(ar.Name), label: ar.Name, value: "Artist", artist: ar})
	}
	for i := range albums {
		al := &albums[i]
		ix.entries = append(ix.entries, indexEntry{norm: normalize(al.name() + " " + al.Artist), label: al.name(),
			value: "Album · " + al.Artist, cover: string(al.CoverArt), album: al})
	}
	// Songs: servers that accept an empty query list the whole library.
	if songs := a.lib.Songs; len(songs) > 0 {
		ix.songs = songs
		for i := range ix.songs {
			s := &ix.songs[i]
			ix.entries = append(ix.entries, indexEntry{norm: normalize(s.Title + " " + s.Artist), label: s.Title,
				value: s.Artist, cover: coverID(*s), song: s})
		}
	}
	sort.SliceStable(ix.entries, func(i, j int) bool { return kindRank(ix.entries[i]) < kindRank(ix.entries[j]) })
	return ix, nil
}

func kindRank(e indexEntry) int {
	switch {
	case e.artist != nil:
		return 0
	case e.album != nil:
		return 1
	}
	return 2
}

func (e indexEntry) toRow(all []indexEntry) row {
	r := row{label: e.label, value: e.value, cover: e.cover}
	switch {
	case e.artist != nil:
		ar := e.artist
		r.open = func(a *app) { openArtist(a, string(ar.ID), ar.Name) }
	case e.album != nil:
		al := *e.album
		r.open = func(a *app) { openAlbum(a, al) }
	case e.song != nil:
		var songs []song
		at := 0
		for _, x := range all {
			if x.song != nil {
				if x.song == e.song {
					at = len(songs)
				}
				songs = append(songs, *x.song)
			}
		}
		s := *e.song
		r.open = func(a *app) { a.play(songs, at) }
		r.onY = func(a *app) { a.enqueue([]song{s}) }
	}
	return r
}

type searchScreen struct {
	q     string
	k     int
	radio bool // radio-browser.info: online, so no prediction
}

func newSearch(a *app) *searchScreen {
	if a.index == nil && a.api != nil {
		ix, err := buildIndex(a)
		if err != nil {
			a.fail("Cannot index", err)
			ix = &libIndex{}
		}
		a.index = ix
	}
	return &searchScreen{}
}

func (s *searchScreen) crumb(a *app) string {
	if s.radio {
		return "Radio · Search stations"
	}
	return ""
}

func (s *searchScreen) letters(a *app) []rune {
	if s.radio {
		out := []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
		out = append(out, space)
		if s.q != "" {
			out = append(out, backspace)
		}
		return out
	}
	if a.index == nil {
		return nil
	}
	return a.index.nextLetters(s.q)
}

func (s *searchScreen) draw(a *app) {
	c, col := a.cv, a.col
	var hits []indexEntry
	if a.index != nil && !s.radio {
		hits = a.index.find(s.q)
	}
	const fy = 44
	c.pill(20, fy, 600, 44, 22, col.chip)
	x := 38
	ph := "Albums, artists, songs"
	if s.radio {
		ph = "Station name, then START"
	}
	if s.q == "" {
		c.textMid(x, fy, 44, &face21, ph, col.dim)
	} else {
		x = c.textMid(x, fy, 44, &face21, strings.ToUpper(s.q), col.fg) + 4
		if !s.radio {
			n := fmt.Sprintf("%d results", len(hits))
			c.textMid(620-18-textWidth(&face15, n), fy, 44, &face15, n, col.dim)
		}
	}
	c.rect(x, fy+10, 3, 24, col.fg) // caret
	for i := 0; i < 5 && i < len(hits); i++ {
		e := hits[i]
		y := 96 + i*rowH
		a.drawCover(e.cover, e.label, 20, y+6, 40, 8)
		vw := textWidth(&face17, truncate(&face17, e.value, 200))
		c.textMid(74, y+4, 44, &face24, truncate(&face24, e.label, 640-74-vw-50), col.fg)
		c.textMid(640-36-vw, y+4, 44, &face17, truncate(&face17, e.value, 200), col.dim)
	}
	if s.q != "" && len(hits) == 0 && !s.radio {
		c.textMid(38, 96, 44, &face24, "No results", col.dim)
	}
	L := s.letters(a)
	s.k = max(0, min(s.k, len(L)-1))
	const lw, lg, ly = 40, 4, 480 - 56 - 47
	shift := max(0, min(s.k*(lw+lg)-286, len(L)*(lw+lg)-620))
	for i, r := range L {
		lx := 12 + i*(lw+lg) - shift
		if lx > 640 || lx+lw < 0 {
			continue
		}
		bg, fg := col.card, col.dim
		if i == s.k {
			bg, fg = col.pill, col.pillFg
		}
		c.pill(lx, ly, lw, 44, 12, bg)
		f := &face21
		ch := string(r)
		if _, ok := f.glyphs[r]; !ok { // font without ␣ / ⌫
			ch = map[rune]string{space: "_", backspace: "<"}[r]
		}
		c.textMid(lx+(lw-textWidth(f, ch))/2, ly, 44, f, ch, fg)
	}
	a.drawHints([]hint{{"X", "DELETE"}, {"A", "TYPE"}}, []hint{{"START", "RESULTS"}})
}

func (s *searchScreen) key(a *app, k key) {
	L := s.letters(a)
	switch k {
	case kLeft:
		if len(L) > 0 {
			s.k = (s.k + len(L) - 1) % len(L)
		}
	case kRight:
		if len(L) > 0 {
			s.k = (s.k + 1) % len(L)
		}
	case kA:
		if s.k < len(L) {
			switch r := L[s.k]; r {
			case backspace:
				s.back()
			case space:
				s.q += " "
			default:
				if len(s.q) < 30 {
					s.q += strings.ToLower(string(r))
				}
			}
			s.k = 0
		}
	case kX:
		s.back()
		s.k = 0
	case kStart, kDown:
		if s.radio {
			if strings.TrimSpace(s.q) == "" {
				a.toast("Type a few letters")
				break
			}
			a.busy("Searching stations…")
			rows, err := a.searchRadios(strings.TrimSpace(s.q))
			if err != nil {
				a.fail("Directory", err)
				break
			}
			a.push(&listScreen{title: fmt.Sprintf("Stations for %q", strings.ToUpper(s.q)), rows: rows, empty: "No stations",
				left: []hint{{"Y", "FAVORITE"}}, right: []hint{{"B", "BACK"}, {"A", "PLAY"}}})
			break
		}
		if a.index == nil {
			break
		}
		hits := a.index.find(s.q)
		if len(hits) == 0 {
			a.toast("Type a few letters")
			break
		}
		rows := make([]row, 0, len(hits))
		for _, e := range hits {
			rows = append(rows, e.toRow(hits))
		}
		a.push(&listScreen{title: fmt.Sprintf("Results for %q", strings.ToUpper(s.q)), rows: rows, thumbs: true,
			left: []hint{{"Y", "UP NEXT"}}, right: []hint{{"B", "BACK"}, {"A", "OPEN"}}})
	case kB:
		a.pop()
	}
	a.dirty = true
}

func (s *searchScreen) back() {
	if r := []rune(s.q); len(r) > 0 {
		s.q = string(r[:len(r)-1])
	}
}
