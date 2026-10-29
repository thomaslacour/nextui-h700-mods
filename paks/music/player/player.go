package main

// Playback glue between the screens, mpd and the server: queue songs, poll
// mpd, remember song details (queue.json), scrobble, star.

import (
	"encoding/json"
	"errors"
	"log"
	"math/rand"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func (a *app) loadMeta() {
	b, err := os.ReadFile(a.cfg.queuePath)
	if err != nil {
		return
	}
	var m map[string]song
	if json.Unmarshal(b, &m) == nil && m != nil {
		a.meta = m
	}
}

func (a *app) saveMeta() {
	b, err := json.Marshal(a.meta)
	if err != nil {
		return
	}
	tmp := a.cfg.queuePath + ".new"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		os.Rename(tmp, a.cfg.queuePath)
	} else {
		log.Printf("queue.json: %v", err)
	}
}

func (a *app) tracks(songs []song) []track {
	out := make([]track, 0, len(songs))
	for _, s := range songs {
		out = append(out, track{url: relayURL(a.cfg, s), title: s.Title, artist: s.Artist, album: s.Album})
	}
	return out
}

func shuffled(songs []song) []song {
	out := append([]song(nil), songs...)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// play replaces the queue with songs and starts at index start.
func (a *app) play(songs []song, start int) {
	if len(songs) == 0 {
		a.toast("Nothing to play")
		return
	}
	a.busy("Starting…")
	if err := a.mpd.replace(a.tracks(songs), start); err != nil {
		a.fail("Cannot play", err)
		return
	}
	a.meta = map[string]song{}
	local := map[flexString]bool{}
	for _, x := range a.localStars() {
		local[x.ID] = true
	}
	for _, s := range songs {
		if local[s.ID] {
			s.Starred = "1"
		}
		a.meta[string(s.ID)] = s
	}
	a.saveMeta()
	a.refresh()
	if _, ok := a.top().(*nowPlaying); !ok {
		a.push(&nowPlaying{})
	}
}

func (a *app) enqueue(songs []song) {
	if len(songs) == 0 {
		return
	}
	if err := a.mpd.add(a.tracks(songs)); err != nil {
		a.fail("Cannot add", err)
		return
	}
	for _, s := range songs {
		a.meta[string(s.ID)] = s
	}
	a.saveMeta()
	if len(songs) == 1 {
		a.toast("Added to queue")
	} else {
		a.toast("%d songs added to queue", len(songs))
	}
	a.refresh()
}

// streamProblem shows, once, a song the server could not send (written by
// the relay), and names it.
func (a *app) streamProblem() {
	b, err := os.ReadFile(streamErrorFile())
	if err != nil {
		return
	}
	os.Remove(streamErrorFile())
	id, msg, _ := strings.Cut(strings.TrimSpace(string(b)), "\t")
	name := "a song"
	if s, ok := a.meta[id]; ok {
		name = s.Title
	}
	a.fail("Skipped "+name, errors.New("server: "+msg))
}

// refresh polls mpd: state, current song, and scrobbles on song change.
func (a *app) refresh() {
	a.streamProblem()
	st, err := a.mpd.status()
	a.mpdOK = err == nil
	if err != nil {
		a.st = map[string]string{}
		a.curID = ""
		return
	}
	a.st = st
	id := ""
	if st["state"] != "stop" || st["song"] != "" {
		if cs, err := a.mpd.current(); err == nil {
			file := cs["file"]
			if u, err := url.Parse(file); err == nil && strings.HasPrefix(u.Path, "/stream/") && strings.HasSuffix(u.Host, a.cfg.proxyAddr[strings.LastIndex(a.cfg.proxyAddr, ":"):]) {
				id, _ = streamID(u.Path)
			} else if file != "" {
				id = file // a radio stream
			}
			if s, ok := a.meta[id]; ok {
				a.cur = s
				if isRadio(s) {
					a.cur.Title, a.cur.Artist = parseICY(cs["Title"], s.Title)
					a.cur.Album = s.Title
				}
			} else {
				a.cur = song{ID: flexString(id), Title: cs["Title"], Artist: cs["Artist"], Album: cs["Album"]}
			}
		}
	}
	a.curID = id
	a.trackScrobble(id)
}

func (a *app) elapsed() float64 {
	v, _ := strconv.ParseFloat(a.st["elapsed"], 64)
	return v
}

func (a *app) duration() float64 {
	if v, err := strconv.ParseFloat(a.st["duration"], 64); err == nil && v > 0 {
		return v
	}
	return float64(a.cur.Duration)
}

// trackScrobble: "now playing" when a song starts, a real scrobble once
// half of it (or 4 minutes) has played. Only while the player is open.
func (a *app) trackScrobble(id string) {
	if a.api == nil || a.offline {
		return
	}
	s := &a.scrob
	if id != s.id {
		s.id, s.elapsed, s.sent, s.dur = id, 0, false, a.duration()
		if id != "" && a.st["state"] == "play" {
			go func() {
				if err := a.api.scrobble(id, false); err != nil {
					log.Printf("now playing: %v", err)
				}
			}()
		}
		return
	}
	s.elapsed = a.elapsed()
	if !s.sent && id != "" && s.dur > 30 && (s.elapsed >= s.dur/2 || s.elapsed >= 240) {
		s.sent = true
		go func() {
			if err := a.api.scrobble(id, true); err != nil {
				log.Printf("scrobble: %v", err)
			}
		}()
	}
}

func (a *app) togglePause() {
	var err error
	switch a.st["state"] {
	case "play":
		err = a.mpd.simple("pause 1")
	case "pause":
		err = a.mpd.simple("pause 0")
	default:
		err = a.mpd.simple("play")
	}
	if err != nil {
		a.fail("Player", err)
	}
	a.refresh()
	a.dirty = true
}

func (a *app) skip(dir int) {
	if !a.mpdOK || a.st["playlistlength"] == "" || a.st["playlistlength"] == "0" {
		return
	}
	cmd := "next"
	if dir < 0 {
		cmd = "previous"
		if a.elapsed() > 3 {
			cmd = "seekcur 0"
		}
	}
	if err := a.mpd.simple(cmd); err != nil {
		a.fail("Player", err)
	}
	a.refresh()
	if _, ok := a.top().(*nowPlaying); !ok && a.cur.Title != "" {
		a.toast("%s", a.cur.Title)
	}
	a.dirty = true
}

func (a *app) seek(by int) {
	if a.st["state"] == "stop" {
		return
	}
	arg := "+" + strconv.Itoa(by)
	if by < 0 {
		arg = strconv.Itoa(by)
	}
	if err := a.mpd.simple("seekcur " + quoteArg(arg)); err != nil {
		a.fail("Seek", err)
	}
	a.refresh()
}

func (a *app) toggleFlag(name string) {
	v := "1"
	if a.st[name] == "1" {
		v = "0"
	}
	if err := a.mpd.simple(name + " " + v); err != nil {
		a.fail("Player", err)
	}
	a.refresh()
}

func (a *app) toggleStar() {
	if a.api == nil || a.curID == "" {
		return
	}
	if isRadio(a.cur) {
		return
	}
	on := a.cur.Starred == ""
	where := ""
	if err := a.api.star(a.curID, on); err != nil {
		var ae *apiError
		if !errors.As(err, &ae) { // network: nothing saved anywhere
			a.fail("Cannot star", err)
			return
		}
		// The server refuses (smolsonic: "database error"): keep it here.
		log.Printf("star on server: %v; kept on the console", err)
		where = " on the console"
	}
	s := a.cur
	if on {
		s.Starred = "1"
		a.toast("Starred%s", where)
	} else {
		s.Starred = ""
		a.toast("Removed from starred%s", where)
	}
	a.setLocalStar(s, on)
	a.meta[a.curID] = s
	a.cur = s
	a.saveMeta()
}

// Local stars: kept in starred.json when the server cannot store them, and
// shown with the server's in Playlists > Starred.
func (a *app) starPath() string { return a.cfg.dir + "/starred.json" }

func (a *app) localStars() []song {
	b, err := os.ReadFile(a.starPath())
	if err != nil {
		return nil
	}
	var out []song
	json.Unmarshal(b, &out)
	return out
}

func (a *app) setLocalStar(s song, on bool) {
	var out []song
	for _, x := range a.localStars() {
		if x.ID != s.ID {
			out = append(out, x)
		}
	}
	if on {
		out = append([]song{s}, out...)
	}
	if b, err := json.Marshal(out); err == nil {
		os.WriteFile(a.starPath()+".new", b, 0o644)
		os.Rename(a.starPath()+".new", a.starPath())
	}
}

// starredSongs: the server's, then the console's, without duplicates.
func (a *app) starredSongs() ([]song, error) {
	songs, _, err := a.api.starred()
	seen := map[flexString]bool{}
	for _, s := range songs {
		seen[s.ID] = true
	}
	for _, s := range a.localStars() {
		if !seen[s.ID] {
			s.Starred = "1"
			songs = append(songs, s)
		}
	}
	if err != nil && len(songs) == 0 {
		return nil, err
	}
	return songs, nil
}
