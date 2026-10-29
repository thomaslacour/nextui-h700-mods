package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeServer answers a few Subsonic methods and records the last query.
func fakeServer(t *testing.T) (*httptest.Server, *url.Values) {
	var last url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = r.URL.Query()
		if last.Get("t") != makeToken("secret", last.Get("s")) || last.Get("u") != "me" {
			io.WriteString(w, `{"subsonic-response":{"status":"failed","version":"1.16.1","error":{"code":40,"message":"Wrong username or password"}}}`)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/rest/") {
		case "ping":
			io.WriteString(w, `{"subsonic-response":{"status":"ok","version":"1.16.1","type":"smolsonic"}}`)
		case "getAlbum":
			// Numeric ids and string durations: must still decode.
			io.WriteString(w, `{"subsonic-response":{"status":"ok","album":{"id":12,"name":"Discovery","artist":"Daft Punk","year":2001,
				"song":[{"id":101,"title":"One More Time","duration":"320","track":1,"suffix":"flac"},{"id":"102","title":"Aerodynamic","duration":207}]}}}`)
		case "getArtists":
			io.WriteString(w, `{"subsonic-response":{"status":"ok","artists":{"index":[{"name":"A","artist":[{"id":"ar1","name":"Air","albumCount":2}]},{"name":"D","artist":[{"id":"ar2","name":"Daft Punk"}]}]}}}`)
		case "stream":
			if last.Get("id") == "bad" {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"subsonic-response":{"status":"failed","error":{"code":0,"message":"could not read file"}}}`)
				return
			}
			w.Header().Set("Content-Type", "audio/flac")
			if rg := r.Header.Get("Range"); rg != "" {
				w.Header().Set("Content-Range", "bytes 4-7/8")
				w.WriteHeader(http.StatusPartialContent)
				io.WriteString(w, "DATA")
				return
			}
			io.WriteString(w, "fLaCDATA")
		default:
			io.WriteString(w, `{"subsonic-response":{"status":"failed","error":{"code":70,"message":"nope"}}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

func testConfig(t *testing.T, url string) *config {
	dir := t.TempDir()
	return &config{dir: dir, URL: url + "/rest", User: "me", Password: "secret",
		settings: map[string]string{}, settingsPath: filepath.Join(dir, "settings.txt"),
		queuePath: filepath.Join(dir, "queue.json"), proxyAddr: "127.0.0.1:46301"}
}

func TestClient(t *testing.T) {
	srv, last := fakeServer(t)
	c, err := newClient(testConfig(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.ping()
	if err != nil || r.Type != "smolsonic" {
		t.Fatalf("ping: %v %+v", err, r)
	}
	if last.Get("v") != apiVersion || last.Get("f") != "json" || last.Get("p") != "" {
		t.Fatalf("query: %v", *last)
	}
	al, err := c.album("12")
	if err != nil || al.ID != "12" || len(al.Song) != 2 || al.Song[0].ID != "101" || al.Song[0].Duration != 320 {
		t.Fatalf("album: %v %+v", err, al)
	}
	ars, err := c.artists()
	if err != nil || len(ars) != 2 || ars[1].Name != "Daft Punk" {
		t.Fatalf("artists: %v %+v", err, ars)
	}
	if _, err := c.playlist("x"); err == nil || err.Error() != "not found" {
		t.Fatalf("api error: %v", err)
	}
	bad := testConfig(t, srv.URL)
	bad.Password = "wrong"
	c2, _ := newClient(bad)
	if _, err := c2.ping(); err == nil || !strings.Contains(err.Error(), "wrong user name") {
		t.Fatalf("bad password: %v", err)
	}
}

func TestErrorsHideToken(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1")
	c, _ := newClient(cfg)
	_, err := c.ping()
	if err == nil || strings.Contains(err.Error(), c.token) || strings.Contains(err.Error(), "t=") {
		t.Fatalf("error leaks the URL: %v", err)
	}
}

func TestRelay(t *testing.T) {
	srv, last := fakeServer(t)
	cfg := testConfig(t, srv.URL)
	c, _ := newClient(cfg)
	relay := httptest.NewServer(newRelay(cfg, c))
	defer relay.Close()

	resp, err := http.Get(relay.URL + "/stream/101.flac")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "fLaCDATA" || resp.Header.Get("Content-Type") != "audio/flac" {
		t.Fatalf("stream: %q %v", body, resp.Header)
	}
	if last.Get("id") != "101" || last.Get("format") != "" || last.Get("t") == "" {
		t.Fatalf("upstream query: %v", *last)
	}

	req, _ := http.NewRequest("GET", relay.URL+"/stream/101.flac", nil)
	req.Header.Set("Range", "bytes=4-")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || string(body) != "DATA" {
		t.Fatalf("range: %d %q", resp.StatusCode, body)
	}

	// Stored format by default (mpd-full decodes it); quality setting: MP3.
	http.Get(relay.URL + "/stream/7.m4a")
	if last.Get("format") != "" || last.Get("maxBitRate") != "" || last.Get("id") != "7" {
		t.Fatalf("original: %v", *last)
	}
	if err := cfg.setSetting("quality", "128"); err != nil {
		t.Fatal(err)
	}
	http.Get(relay.URL + "/stream/8.flac")
	if last.Get("maxBitRate") != "128" || last.Get("format") != "mp3" {
		t.Fatalf("quality: %v", *last)
	}
	// The server sends an error instead of audio: 502, reason kept for the player.
	t.Setenv("TMPDIR", t.TempDir())
	r, err := http.Get(relay.URL + "/stream/bad.mp3")
	if err != nil || r.StatusCode != 502 {
		t.Fatalf("server error: %v %v", err, r)
	}
	if b, _ := os.ReadFile(streamErrorFile()); string(b) != "bad\tcould not read file\n" {
		t.Fatalf("error file: %q", b)
	}
	if r, _ := http.Get(relay.URL + "/other"); r.StatusCode != 404 {
		t.Fatalf("other path: %d", r.StatusCode)
	}
}

func TestRelayURL(t *testing.T) {
	cfg := testConfig(t, "http://x")
	cases := map[string]string{"flac": "/stream/a%2Fb.flac", "M4A": "/stream/a%2Fb.m4a", "": "/stream/a%2Fb.bin"}
	for suffix, want := range cases {
		got := relayURL(cfg, song{ID: "a/b", Suffix: suffix})
		if !strings.HasSuffix(got, want) {
			t.Errorf("%s: %s", suffix, got)
		}
		u, _ := url.Parse(got)
		if id, ok := streamID(u.Path); !ok || id != "a/b" {
			t.Errorf("streamID(%s) = %q", u.Path, id)
		}
	}
}

func TestConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MUSIC_DIR", dir)
	if _, err := loadConfig(); err != errNoConfig {
		t.Fatalf("missing config: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "config.txt"), []byte("# c\nurl = https://music.example.com/rest/\nuser = me\ntoken = abc # note\nsalt = s1\nca = ca.pem\n"), 0o644)
	cfg, err := loadConfig()
	if err != nil || cfg.URL != "https://music.example.com/rest" || cfg.Token != "abc" || cfg.CA != filepath.Join(dir, "ca.pem") {
		t.Fatalf("config: %v %+v", err, cfg)
	}
	if _, err := newClient(cfg); err == nil || !strings.Contains(err.Error(), "CA") {
		t.Fatalf("missing CA must fail: %v", err)
	}
	cfg.setSetting("after_exit", "stop")
	kv, _ := readKV(filepath.Join(dir, "settings.txt"))
	if kv["after_exit"] != "stop" {
		t.Fatalf("settings: %v", kv)
	}
}

func TestPlayerWithFakeMPD(t *testing.T) {
	a, f, err := previewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	a.play(sampleSongs[:3], 1)
	if a.curID != "s2" || a.st["state"] != "play" || a.cur.Title != "High Together" {
		t.Fatalf("play: %q %v %+v", a.curID, a.st, a.cur)
	}
	if _, ok := a.top().(*nowPlaying); !ok {
		t.Fatal("now playing not shown")
	}
	q, _ := a.mpd.queue()
	if len(q) != 3 || q[0].title != "Signaler" || !strings.HasSuffix(q[0].file, "/stream/s1.m4a") {
		t.Fatalf("queue: %+v", q)
	}
	a.enqueue(sampleSongs[5:6])
	if q, _ = a.mpd.queue(); len(q) != 4 || q[3].title != "One More Time" {
		t.Fatalf("enqueue: %+v", q)
	}
	a.handle(kA)
	if a.st["state"] != "pause" {
		t.Fatalf("pause: %v", a.st)
	}
	a.handle(kR1)
	if a.curID != "s3" {
		t.Fatalf("next: %q", a.curID)
	}
	a.handle(kL2)
	if a.st["random"] != "1" {
		t.Fatalf("shuffle: %v", a.st)
	}
	// Screen off: only playback keys work, MENU does nothing.
	a.handle(kSelect)
	a.handle(kMenu)
	a.handle(kR1)
	if !a.off || a.curID != "s6" || len(a.stack) != 2 {
		t.Fatalf("screen off: off=%v cur=%q stack=%d", a.off, a.curID, len(a.stack))
	}
	a.handle(kPower)
	a.handle(kMenu)
	a.release(kMenu)
	if a.off || len(a.stack) != 1 || a.tab != 5 {
		t.Fatalf("wake + MENU: off=%v stack=%d tab=%d (want Search)", a.off, len(a.stack), a.tab)
	}
	a.setTab(0)
	a.handle(kB)
	if a.quit {
		t.Fatal("B on Listen Now must not quit")
	}
	a.handle(kMenu)
	a.handle(kStart)
	a.release(kMenu)
	if !a.quit || a.tab != 0 {
		t.Fatal("MENU + START must quit (and not open the MENU screen)")
	}
	// queue.json lets a new player show the details of mpd's songs.
	b := newApp(a.cfg, nil, previewFramebuffer())
	b.refresh()
	if b.cur.Title != "One More Time" || b.cur.Suffix != "flac" {
		t.Fatalf("reload meta: %+v", b.cur)
	}
}

func TestPreviews(t *testing.T) {
	dir := t.TempDir()
	if err := renderPreviews(dir); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	if len(files) != 17 {
		t.Fatalf("previews: %v", files)
	}
}

func TestWrapAndKeys(t *testing.T) {
	lines := wrap(&face30, "Harder, Better, Faster, Stronger (Alive 2007 version, extended)", 300, 2)
	if len(lines) != 2 || !strings.HasSuffix(lines[1], "...") {
		t.Fatalf("wrap: %q", lines)
	}
	if ev, ok := translateKey(304, 1); !ok || ev.k != kA || !ev.pressed {
		t.Fatal("A")
	}
	if _, ok := translateKey(311, 2); ok {
		t.Fatal("autorepeat must be ignored")
	}
	if ev, _ := translateKey(312, 0); ev.k != kMenu || ev.pressed {
		t.Fatal("MENU release")
	}
}

func TestFillUnknown(t *testing.T) {
	r := &response{Album: &album{Song: []song{{ID: "1"}}}, Search: &struct {
		Artist []artist `json:"artist"`
		Album  []album  `json:"album"`
		Song   []song   `json:"song"`
	}{Artist: []artist{{}}, Song: []song{{Title: "  "}}}}
	fillUnknown(r)
	if r.Album.Name != unknownAlbum || r.Album.Artist != unknownArtist || r.Album.Song[0].Title != untitled ||
		r.Search.Artist[0].Name != unknownArtist || r.Search.Song[0].Album != unknownAlbum || r.Search.Song[0].Title != untitled {
		t.Fatalf("%+v %+v", r.Album, r.Search)
	}
}

func TestLocalStarFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/rest/") {
		case "star", "unstar":
			io.WriteString(w, `{"subsonic-response":{"status":"failed","error":{"code":0,"message":"database error"}}}`)
		case "getStarred2":
			io.WriteString(w, `{"subsonic-response":{"status":"ok","starred2":{"song":[]}}}`)
		}
	}))
	defer srv.Close()
	a, f, err := previewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	a.cfg.URL, a.cfg.Password = srv.URL+"/rest", "secret"
	a.api, _ = newClient(a.cfg)
	a.offline = false
	a.play(sampleSongs[:2], 0)
	a.toggleStar()
	if a.cur.Starred == "" || !strings.Contains(a.toastText, "console") {
		t.Fatalf("star: %q %q", a.cur.Starred, a.toastText)
	}
	songs, err := a.starredSongs()
	if err != nil || len(songs) != 1 || songs[0].ID != "s1" {
		t.Fatalf("starred: %v %+v", err, songs)
	}
	a.play(sampleSongs[:2], 0) // a new queue keeps the local star
	if a.cur.Starred == "" {
		t.Fatal("local star lost on replay")
	}
	a.toggleStar()
	if songs, _ := a.starredSongs(); len(songs) != 0 {
		t.Fatalf("unstar: %+v", songs)
	}
}

func TestEllipsisOnBaseline(t *testing.T) {
	if textWidth(&face18, "Loading…") != textWidth(&face18, "Loading...") {
		t.Fatal("… must be drawn as three periods")
	}
}

func TestExternalPad(t *testing.T) {
	p := newPadState(-1) // no device: default ranges, D-input layout
	cases := []struct {
		e    rawEvent
		want []keyEvent
	}{
		{rawEvent{1, 305, 1}, []keyEvent{{kA, true}}}, // bottom button
		{rawEvent{1, 304, 1}, []keyEvent{{kB, true}}}, // right button
		{rawEvent{1, 315, 1}, []keyEvent{{kStart, true}}},
		{rawEvent{1, 316, 1}, []keyEvent{{kMenu, true}}},
		{rawEvent{3, 17, -1}, []keyEvent{{kUp, true}}},
		{rawEvent{3, 17, 0}, []keyEvent{{kUp, false}}},
		{rawEvent{3, absX, 30000}, []keyEvent{{kRight, true}}},
		{rawEvent{3, absX, 29000}, nil}, // still right
		{rawEvent{3, absX, 0}, []keyEvent{{kRight, false}}},
		{rawEvent{3, absGas, 30000}, []keyEvent{{kR2, true}}},
		{rawEvent{3, absGas, -30000}, []keyEvent{{kR2, false}}},
	}
	for i, c := range cases {
		got := p.translate(c.e)
		if len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) {
			t.Errorf("%d %+v: got %+v want %+v", i, c.e, got, c.want)
		}
	}
	if !reJoystick.MatchString("h: handlers=event3 js1 ") || reJoystick.MatchString("h: handlers=kbd event2 ") {
		t.Fatal("joystick detection")
	}
}

// A stale library.json is kept (survives a reboot), then reloaded in the
// background: the artists come back from the server, failed lists keep
// their cached copy, and a result from before a cache clear is dropped.
func TestLibraryBackgroundRefresh(t *testing.T) {
	srv, _ := fakeServer(t)
	cfg := testConfig(t, srv.URL)
	c, _ := newClient(cfg)
	a := &app{cfg: cfg, api: c, home: &homeScreen{}}
	old := library{Saved: time.Now().Add(-7 * time.Hour), Have: []string{"artists", "playlists"},
		Artists: []artist{{ID: "x", Name: "Old"}}, Playlists: []playlist{{ID: "p", Name: "Kept"}}}
	b, _ := json.Marshal(old)
	os.WriteFile(a.libraryPath(), b, 0o644)

	a.loadLibrary()
	if len(a.lib.Artists) != 1 || a.lib.Artists[0].Name != "Old" {
		t.Fatalf("stale cache not loaded: %+v", a.lib)
	}
	a.maybeRefresh()
	if !a.libR.running {
		t.Fatal("no refresh started")
	}
	a.maybeRefresh() // a second call must not start another one
	a.libraryRefreshed(<-a.libR.c)
	if len(a.lib.Artists) != 2 || a.lib.Playlists[0].Name != "Kept" || time.Since(a.lib.Saved) > time.Minute {
		t.Fatalf("after refresh: %+v", a.lib)
	}
	var saved library
	b, _ = os.ReadFile(a.libraryPath())
	if json.Unmarshal(b, &saved) != nil || len(saved.Artists) != 2 {
		t.Fatal("refresh not saved")
	}

	a.lib.Saved = time.Now().Add(-7 * time.Hour)
	a.libR.tried = time.Time{}
	a.maybeRefresh()
	a.clearLibrary()
	a.libraryRefreshed(<-a.libR.c)
	if len(a.lib.Artists) != 0 {
		t.Fatal("result from before the clear was applied")
	}
}
