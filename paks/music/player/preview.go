package main

// Off-device rendering (--preview) and the fake mpd it uses (also used by
// the tests): the same drawing code as on the console, sample data, PNGs.

import (
	"bufio"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fakeMPD answers the few commands the player sends, from memory.
type fakeMPD struct {
	mu      sync.Mutex
	ln      net.Listener
	queue   []queueEntry
	nextID  int
	state   string
	song    int
	elapsed float64
	random  string
	repeat  string
	audio   string
	log     []string
}

// startFakeMPD listens on a random local TCP port (addr()): unix socket
// paths are too short-limited on macOS for test directories.
func startFakeMPD() (*fakeMPD, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &fakeMPD{ln: ln, state: "stop", random: "0", repeat: "0", nextID: 1}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f, nil
}

func (f *fakeMPD) close()       { f.ln.Close() }
func (f *fakeMPD) addr() string { return f.ln.Addr().String() }

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) >= 2 {
		s = s[1 : len(s)-1]
		s = strings.ReplaceAll(s, `\"`, `"`)
		s = strings.ReplaceAll(s, `\\`, `\`)
	}
	return s
}

func splitArgs(line string) []string {
	var out []string
	var cur strings.Builder
	inQ, esc := false, false
	for _, r := range line {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && inQ:
			esc = true
		case r == '"':
			inQ = !inQ
		case r == ' ' && !inQ:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func (f *fakeMPD) serve(c net.Conn) {
	defer c.Close()
	w := bufio.NewWriter(c)
	r := bufio.NewReader(c)
	w.WriteString("OK MPD 0.23.16\n")
	w.Flush()
	var list []string
	inList := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "command_list_begin":
			inList, list = true, nil
			continue
		case line == "command_list_end":
			inList = false
		case inList:
			list = append(list, line)
			continue
		default:
			list = []string{line}
		}
		ok := true
		for _, cmd := range list {
			if err := f.exec(cmd, w); err != nil {
				fmt.Fprintf(w, "ACK [2@0] {%s} %s\n", strings.Fields(cmd)[0], err)
				ok = false
				break
			}
		}
		if ok {
			w.WriteString("OK\n")
		}
		w.Flush()
	}
}

func (f *fakeMPD) exec(line string, w *bufio.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, line)
	a := splitArgs(line)
	arg := func(i int) string {
		if i < len(a) {
			return a[i]
		}
		return ""
	}
	switch a[0] {
	case "status":
		fmt.Fprintf(w, "volume: -1\nrepeat: %s\nrandom: %s\nplaylistlength: %d\nstate: %s\n", f.repeat, f.random, len(f.queue), f.state)
		if f.state != "stop" && f.song < len(f.queue) {
			fmt.Fprintf(w, "song: %d\nsongid: %d\nelapsed: %.3f\nbitrate: 0\n", f.song, f.queue[f.song].id, f.elapsed)
			if f.audio != "" {
				fmt.Fprintf(w, "audio: %s\n", f.audio)
			}
		}
	case "currentsong":
		if f.state != "stop" && f.song < len(f.queue) {
			e := f.queue[f.song]
			fmt.Fprintf(w, "file: %s\nTitle: %s\nArtist: %s\nPos: %d\nId: %d\n", e.file, e.title, e.artist, e.pos, e.id)
		}
	case "playlistinfo":
		for i, e := range f.queue {
			fmt.Fprintf(w, "file: %s\n", e.file)
			if e.title != "" {
				fmt.Fprintf(w, "Title: %s\n", e.title)
			}
			if e.artist != "" {
				fmt.Fprintf(w, "Artist: %s\n", e.artist)
			}
			fmt.Fprintf(w, "Pos: %d\nId: %d\n", i, e.id)
		}
	case "clear":
		f.queue, f.state, f.song = nil, "stop", 0
	case "addid":
		f.queue = append(f.queue, queueEntry{pos: len(f.queue), id: f.nextID, file: arg(1)})
		fmt.Fprintf(w, "Id: %d\n", f.nextID)
		f.nextID++
	case "addtagid":
		id, _ := strconv.Atoi(arg(1))
		for i := range f.queue {
			if f.queue[i].id == id {
				switch arg(2) {
				case "Title":
					f.queue[i].title = arg(3)
				case "Artist":
					f.queue[i].artist = arg(3)
				}
			}
		}
	case "play":
		if len(f.queue) == 0 {
			return fmt.Errorf("empty queue")
		}
		if n, err := strconv.Atoi(arg(1)); err == nil {
			f.song = n
		}
		f.state, f.elapsed = "play", 0
	case "pause":
		if arg(1) == "1" {
			f.state = "pause"
		} else {
			f.state = "play"
		}
	case "next":
		if f.song+1 < len(f.queue) {
			f.song++
			f.elapsed = 0
		} else {
			f.state = "stop"
		}
	case "previous":
		if f.song > 0 {
			f.song--
		}
		f.elapsed = 0
	case "seekcur":
		v, _ := strconv.ParseFloat(arg(1), 64)
		if strings.HasPrefix(arg(1), "+") || strings.HasPrefix(arg(1), "-") {
			f.elapsed += v
		} else {
			f.elapsed = v
		}
	case "random":
		f.random = arg(1)
	case "repeat":
		f.repeat = arg(1)
	case "stop":
		f.state = "stop"
	case "delete":
		n, _ := strconv.Atoi(arg(1))
		if n < len(f.queue) {
			f.queue = append(f.queue[:n], f.queue[n+1:]...)
			for i := range f.queue {
				f.queue[i].pos = i
			}
			if n < f.song {
				f.song--
			}
		}
	default:
		return fmt.Errorf("unknown command")
	}
	return nil
}

func sampleCover(c1, c2, c3 color.RGBA, cx, cy int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, coverPx, coverPx))
	for y := 0; y < coverPx; y++ {
		for x := 0; x < coverPx; x++ {
			t := float64(x+y) / float64(2*coverPx)
			px := color.RGBA{uint8(float64(c1.R)*(1-t) + float64(c3.R)*t), uint8(float64(c1.G)*(1-t) + float64(c3.G)*t), uint8(float64(c1.B)*(1-t) + float64(c3.B)*t), 255}
			if dx, dy := x-cx, y-cy; dx*dx+dy*dy < 52*52 {
				px = c2
			}
			img.SetRGBA(x, y, px)
		}
	}
	return img
}

var sampleSongs = []song{
	{ID: "s1", Title: "Signaler", Artist: "Siriusmo", Album: "Mosaik", AlbumID: "al1", CoverArt: "al1", Duration: 242, Track: 1, Suffix: "m4a", BitRate: 256},
	{ID: "s2", Title: "High Together", Artist: "Siriusmo", Album: "Mosaik", AlbumID: "al1", CoverArt: "al1", Duration: 287, Track: 2, Suffix: "m4a", BitRate: 256},
	{ID: "s3", Title: "Plastic Hips", Artist: "Siriusmo", Album: "Mosaik", AlbumID: "al1", CoverArt: "al1", Duration: 229, Track: 3, Suffix: "m4a", BitRate: 256},
	{ID: "s4", Title: "Red Knob", Artist: "Siriusmo", Album: "Mosaik", AlbumID: "al1", CoverArt: "al1", Duration: 769, Track: 4, Suffix: "m4a", BitRate: 256, Starred: "2026-10-01"},
	{ID: "s5", Title: "Feromonikon", Artist: "Siriusmo", Album: "Mosaik", AlbumID: "al1", CoverArt: "al1", Duration: 261, Track: 5, Suffix: "m4a", BitRate: 256},
	{ID: "s6", Title: "One More Time", Artist: "Daft Punk", Album: "Discovery", AlbumID: "al2", CoverArt: "al2", Duration: 320, Track: 1, Suffix: "flac", BitRate: 1411},
}

var sampleAlbums = []album{
	{ID: "al1", Name: "Mosaik", Artist: "Siriusmo", Year: 2011, CoverArt: "al1"},
	{ID: "al2", Name: "Discovery", Artist: "Daft Punk", Year: 2001, CoverArt: "al2"},
	{ID: "al3", Name: "Moon Safari", Artist: "Air", Year: 1998, CoverArt: "al3"},
	{ID: "al4", Name: "Cross", Artist: "Justice", Year: 2007, CoverArt: "al4"},
	{ID: "al5", Name: "L'homme à tête de chou", Artist: "Serge Gainsbourg", Year: 1976, CoverArt: "al5"},
	{ID: "al6", Name: "Wolfgang Amadeus Phoenix", Artist: "Phoenix", Year: 2009, CoverArt: "al6"},
	{ID: "al7", Name: "Politics", Artist: "Sébastien Tellier", Year: 2004, CoverArt: "al7"},
}

func savePNG(fb *framebuffer, pixels []uint32, path string) error {
	img := image.NewRGBA(image.Rect(0, 0, fb.w, fb.h))
	for y := 0; y < fb.h; y++ {
		for x := 0; x < fb.w; x++ {
			p := pixels[y*fb.w+x]
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

// previewApp: an app on an in-memory screen, signed in, with a fake mpd,
// sample covers and a sample library index.
func previewApp(dir string) (*app, *fakeMPD, error) {
	f, err := startFakeMPD()
	if err != nil {
		return nil, nil, err
	}
	sock := f.addr()
	cfg := &config{dir: dir, URL: "https://music.example.com/rest", User: "me", Token: "x", Salt: "y",
		settings: map[string]string{}, settingsPath: filepath.Join(dir, "settings.txt"),
		queuePath: filepath.Join(dir, "queue.json"), mpdSocket: sock, proxyAddr: "127.0.0.1:46301"}
	a := newApp(cfg, nil, previewFramebuffer())
	a.offline = true
	a.status = statusState{charge: 82, wifi: 3, vpn: vpnOn, clock: "13:40"}
	pal := [][3]color.RGBA{
		{{210, 75, 42, 255}, {243, 211, 107, 255}, {90, 26, 16, 255}}, {{29, 42, 107, 255}, {232, 192, 74, 255}, {11, 15, 36, 255}},
		{{159, 180, 192, 255}, {244, 241, 230, 255}, {77, 96, 107, 255}}, {{20, 20, 20, 255}, {242, 242, 242, 255}, {58, 15, 15, 255}},
		{{107, 74, 43, 255}, {227, 199, 155, 255}, {42, 26, 12, 255}}, {{227, 211, 180, 255}, {192, 57, 43, 255}, {122, 91, 58, 255}},
		{{19, 41, 75, 255}, {155, 183, 212, 255}, {5, 12, 24, 255}},
	}
	for i, al := range sampleAlbums {
		img := sampleCover(pal[i][0], pal[i][1], pal[i][2], 70+i*15, 80+i*10)
		a.covers[string(al.CoverArt)] = img
		a.avg[string(al.CoverArt)] = average(img)
		a.asked[string(al.CoverArt)] = true
	}
	a.lib = &library{Recent: sampleAlbums, Frequent: append(append([]album{}, sampleAlbums[3:]...), sampleAlbums[:3]...),
		Albums: sampleAlbums, Have: []string{"recent", "frequent", "albums", "artists", "playlists", "songs"}}
	a.home.loaded = true
	for _, al := range sampleAlbums {
		a.lib.Artists = append(a.lib.Artists, artist{ID: flexString("ar-" + al.Artist), Name: al.Artist, AlbumCount: 1})
	}
	a.home.explore = a.lib.Artists[1:]
	a.favorites = []station{{"FIP", "https://icecast.radiofrance.fr/fip-hifi.aac"}, {"Radio Nova", "https://novazz.ice.infomaniak.ch/novazz-128.mp3"}}
	ix := &libIndex{songs: sampleSongs}
	for i := range sampleAlbums {
		al := &sampleAlbums[i]
		ix.entries = append(ix.entries, indexEntry{norm: normalize(al.name() + " " + al.Artist), label: al.name(), value: "Album · " + al.Artist, cover: string(al.CoverArt), album: al})
	}
	for i := range ix.songs {
		s := &ix.songs[i]
		ix.entries = append(ix.entries, indexEntry{norm: normalize(s.Title + " " + s.Artist), label: s.Title, value: s.Artist, cover: coverID(*s), song: s})
	}
	a.index = ix
	return a, f, nil
}

func renderPreviews(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	a, f, err := previewApp(dir)
	if err != nil {
		return err
	}
	defer f.close()
	shot := func(name string) error {
		a.refresh()
		a.drawScreen()
		return savePNG(a.fb, a.cv.pixels, filepath.Join(dir, name+".png"))
	}
	if err := a.mpd.replace(a.tracks(sampleSongs[:5]), 3); err != nil {
		return err
	}
	for _, s := range sampleSongs {
		a.meta[string(s.ID)] = s
	}
	f.elapsed = 151
	f.audio = "44100:f:2"
	steps := []struct {
		name string
		do   func()
	}{
		{"01-listen-now", func() { a.home.sel, a.home.it = 0, [4]int{} }},
		{"02-listen-now-shelf", func() { a.home.sel, a.home.it = 3, [4]int{0, 2, 1, 1} }},
		{"03-albums", func() {
			a.tab = 1
			a.stack = []screen{&listScreen{thumbs: true, rows: albumRows(sampleAlbums), sel: 1, right: []hint{{"Y", "UP NEXT"}, {"A", "OPEN"}}, left: []hint{{"L2", "TABS"}}}}
		}},
		{"04-album", func() {
			full := sampleAlbums[0]
			full.Song = sampleSongs[:5]
			a.push(&albumScreen{al: &full, sel: 5})
		}},
		{"05-now-playing", func() { a.push(&nowPlaying{}) }},
		{"06-paused-shuffle", func() { f.state = "pause"; f.random = "1" }},
		{"07-up-next", func() { f.state = "play"; a.push(queueScreen(a)) }},
		{"08-search", func() {
			a.tab = 5
			a.stack = []screen{&searchScreen{q: "si"}}
		}},
		{"09-settings", func() { a.tab = 6; a.stack = []screen{settingsScreen()}; a.top().(*listScreen).sel = 0 }},
		{"09b-editor", func() {
			a.push(&editor{title: "Settings · Server URL", value: "https://music.example.com/rest", x: 3, y: 3, save: func(*app, string) {}})
		}},
		{"10-volume", func() {
			a.pop()
			a.volume, a.volUntil = 60, time.Now().Add(time.Minute)
		}},
		{"11-light-home", func() {
			a.volUntil = time.Time{}
			a.setTheme(true)
			a.tab, a.stack = 0, []screen{a.home}
		}},
		{"12-light-now-playing", func() { a.push(&nowPlaying{}) }},
		{"13-toast", func() {
			a.stack = a.stack[:1]
			a.setTheme(false)
			a.toast("Added to Up Next")
			a.toastStart = time.Now().Add(-time.Second)
		}},
		{"13b-toast-sliding", func() { a.fail("Cannot star", errNoArt); a.toastStart = time.Now().Add(-slide / 2) }},
	}
	for _, s := range steps {
		s.do()
		if err := shot(s.name); err != nil {
			return err
		}
	}
	a.toastUntil = time.Time{}
	a.tab = 4
	a.stack = []screen{&listScreen{rows: []row{{label: "Search stations", value: "radio-browser.info"}, {label: "FIP", value: "Recent"},
		{label: "Radio Nova", value: "novazz.ice.infomaniak.ch"}}, left: []hint{{"Y", "FAVORITE"}}, right: []hint{{"A", "PLAY"}}}}
	if err := shot("14-radio"); err != nil {
		return err
	}
	a.cfgOK = false
	a.tab, a.stack = 0, []screen{a.home}
	return shot("15-signed-out")
}
