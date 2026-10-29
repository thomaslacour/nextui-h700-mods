package main

// Radio: the server's internet radio stations (Subsonic
// getInternetRadioStations) plus radios.txt on the SD card, one station per
// line "Name | https://stream-url". mpd plays the stream directly; the song
// title comes from the stream (ICY metadata).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type station struct{ name, url string }

const defaultRadios = `# Radio stations for the Music pak: one per line, "Name | stream URL".
FIP | https://icecast.radiofrance.fr/fip-hifi.aac
France Inter | https://icecast.radiofrance.fr/franceinter-hifi.aac
France Culture | https://icecast.radiofrance.fr/franceculture-hifi.aac
Radio Nova | https://novazz.ice.infomaniak.ch/novazz-128.mp3
TSF Jazz | https://tsfjazz.ice.infomaniak.ch/tsfjazz-high.mp3
SomaFM Groove Salad | https://ice2.somafm.com/groovesalad-128-mp3
Radio Paradise | https://stream.radioparadise.com/aac-320
KEXP 90.3 | https://kexp-mp3-128.streamguys1.com/kexp128.mp3
`

func (a *app) radiosPath() string { return filepath.Join(a.cfg.dir, "radios.txt") }

func (a *app) stations() []station {
	var out []station
	if a.api != nil {
		a.ensure("radios", "Loading stations…")
		for _, s := range a.lib.Radios {
			out = append(out, station{s.Name, s.URL})
		}
	}
	if _, err := os.Stat(a.radiosPath()); os.IsNotExist(err) && !a.offline {
		os.WriteFile(a.radiosPath(), []byte(defaultRadios), 0o644)
	}
	f, err := os.Open(a.radiosPath())
	if err != nil {
		f2 := strings.NewReader(defaultRadios)
		return append(out, parseRadios(bufio.NewScanner(f2))...)
	}
	defer f.Close()
	return append(out, parseRadios(bufio.NewScanner(f))...)
}

func parseRadios(sc *bufio.Scanner) []station {
	var out []station
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, u, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		name, u = strings.TrimSpace(name), strings.TrimSpace(u)
		if name != "" && (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
			out = append(out, station{name, u})
		}
	}
	return out
}

// radioTab: online search, the last stations played, then favorites.
func radioTab(a *app) screen {
	l := &listScreen{empty: "No stations", left: []hint{{"Y", "FAVORITE"}}, right: []hint{{"A", "PLAY"}}}
	l.refresh = func(a *app) []row {
		rows := []row{{label: "Search stations", value: "radio-browser.info", open: func(a *app) { a.push(&searchScreen{radio: true}) }}}
		for _, s := range a.recentRadios() {
			s := s
			rows = append(rows, row{label: s.name, value: "Recent", open: func(a *app) { a.playRadio(s) }, onY: func(a *app) { a.addFavorite(s) }})
		}
		for _, s := range a.favorites {
			s := s
			rows = append(rows, row{label: s.name, value: hostOf(s.url), open: func(a *app) { a.playRadio(s) }})
		}
		return rows
	}
	if a.favorites == nil {
		a.favorites = a.stations()
	}
	return l
}

func (a *app) recentPath() string { return filepath.Join(a.cfg.dir, "radios-recent.txt") }

func (a *app) recentRadios() []station {
	f, err := os.Open(a.recentPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseRadios(bufio.NewScanner(f))
}

// rememberRadio puts s first in the recent list (8 kept, no duplicates).
func (a *app) rememberRadio(s station) {
	list := []station{s}
	for _, r := range a.recentRadios() {
		if r.url != s.url && len(list) < 8 {
			list = append(list, r)
		}
	}
	var b strings.Builder
	b.WriteString("# Last radio stations played (Music pak).\n")
	for _, r := range list {
		b.WriteString(r.name + " | " + r.url + "\n")
	}
	os.WriteFile(a.recentPath(), []byte(b.String()), 0o644)
}

func (a *app) addFavorite(s station) {
	for _, f := range a.favorites {
		if f.url == s.url {
			a.toast("Already in favorites")
			return
		}
	}
	f, err := os.OpenFile(a.radiosPath(), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		a.fail("Cannot save", err)
		return
	}
	f.WriteString(strings.ReplaceAll(s.name, "|", "/") + " | " + s.url + "\n")
	f.Close()
	a.favorites = append(a.favorites, s)
	a.toast("Added to favorites")
}

// searchRadios asks the radio-browser.info directory (no account needed).
func (a *app) searchRadios(q string) ([]row, error) {
	if rows, ok := a.radioHits[strings.ToLower(q)]; ok {
		return rows, nil
	}
	rows, err := a.searchRadiosOnline(q)
	if err == nil {
		if a.radioHits == nil {
			a.radioHits = map[string][]row{}
		}
		a.radioHits[strings.ToLower(q)] = rows
	}
	return rows, err
}

func (a *app) searchRadiosOnline(q string) ([]row, error) {
	u := "https://all.api.radio-browser.info/json/stations/search?" + url.Values{
		"name": {q}, "limit": {"60"}, "hidebroken": {"true"}, "order": {"clickcount"}, "reverse": {"true"}}.Encode()
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "sp-music/1.0 (RG35XX SP)")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, cleanErr(err)
	}
	defer resp.Body.Close()
	var list []struct {
		Name    string `json:"name"`
		URL     string `json:"url_resolved"`
		Country string `json:"countrycode"`
		Codec   string `json:"codec"`
		Bitrate int    `json:"bitrate"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("directory answer: %v", err)
	}
	var rows []row
	for _, r := range list {
		if r.URL == "" || strings.TrimSpace(r.Name) == "" {
			continue
		}
		s := station{strings.TrimSpace(r.Name), r.URL}
		v := strings.TrimSpace(r.Country + " · " + r.Codec)
		if r.Bitrate > 0 {
			v += " " + strconv.Itoa(r.Bitrate)
		}
		rows = append(rows, row{label: s.name, value: strings.Trim(v, " ·"), open: func(a *app) { a.playRadio(s) },
			onY: func(a *app) { a.addFavorite(s) }})
	}
	return rows, nil
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(u, "/:"); i >= 0 {
		u = u[:i]
	}
	return u
}

func (a *app) playRadio(s station) {
	a.busy("Tuning in…")
	if err := a.mpd.replace([]track{{url: s.url, title: s.name, album: s.name}}, 0); err != nil {
		a.fail("Cannot play", err)
		return
	}
	a.meta = map[string]song{s.url: {ID: flexString(s.url), Title: s.name, Album: s.name, Suffix: "radio"}}
	a.saveMeta()
	a.rememberRadio(s)
	a.refresh()
	a.push(&nowPlaying{})
}

func isRadio(s song) bool { return s.Suffix == "radio" }

// parseICY turns a stream title into song and artist. ICY titles are
// loose: "Artist - Song" by convention, often Latin-1 instead of UTF-8,
// sometimes the raw "StreamTitle='...';", quoted, with HTML entities, empty,
// a URL, or just the station's name. Anything unusable gives the station
// name and "Live radio".
func parseICY(raw, station string) (title, artist string) {
	t := cleanICY(raw)
	if t == "" || strings.EqualFold(t, station) || looksGeneric(t) {
		return station, "Live radio"
	}
	for _, sep := range []string{" - ", " – ", " — ", " ~ ", " | ", " / "} {
		a, b, ok := strings.Cut(t, sep)
		a, b = strings.TrimSpace(a), strings.TrimSpace(b)
		if !ok || a == "" || b == "" {
			continue
		}
		if looksGeneric(a) && looksGeneric(b) {
			return station, "Live radio"
		}
		if looksGeneric(b) {
			return a, station
		}
		return b, a
	}
	return t, station
}

var icyEntities = strings.NewReplacer("&amp;", "&", "&quot;", "\"", "&#39;", "'", "&apos;", "'", "&lt;", "<", "&gt;", ">")

func cleanICY(raw string) string {
	t := raw
	if !utf8.ValidString(t) { // Latin-1, the ICY default
		r := make([]rune, 0, len(t))
		for i := 0; i < len(t); i++ {
			r = append(r, rune(t[i]))
		}
		t = string(r)
	}
	if i := strings.Index(t, "StreamTitle="); i >= 0 {
		t = t[i+len("StreamTitle="):]
		if j := strings.Index(t, "';"); j >= 0 {
			t = t[:j]
		}
	}
	t = icyEntities.Replace(t)
	t = strings.Map(func(r rune) rune {
		if r < 32 || r == 0x7f || r == utf8.RuneError {
			return ' '
		}
		return r
	}, t)
	t = strings.Join(strings.Fields(t), " ")
	t = strings.Trim(t, " '\"`;")
	// A dangling separator ("Artist - " while the title is unknown).
	t = strings.TrimSpace(strings.Trim(t, "-–—~|/ "))
	if r := []rune(t); len(r) > 160 {
		t = string(r[:160])
	}
	return t
}

// looksGeneric: placeholders some stations send between songs.
func looksGeneric(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	if l == "" || strings.HasPrefix(l, "http") || strings.HasPrefix(l, "www.") {
		return true
	}
	switch l {
	case "-", "--", "unknown", "untitled", "n/a", "na", "null", "none", "live", "on air", "advert", "advertisement", "pub", "jingle":
		return true
	}
	hasLetter := false
	for _, r := range l {
		if unicode.IsLetter(r) {
			hasLetter = true
			break
		}
	}
	return !hasLetter
}
