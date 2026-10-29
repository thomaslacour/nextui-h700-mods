package main

// Library cache: lists fetched once, kept in memory and in library.json on
// the SD card, across reboots. It is used whatever its age, so the app opens
// at once; past 6 hours it is reloaded in the background (at start, then
// checked every minute while the app runs) and swapped in when complete.
// Settings > Refresh library reloads it now.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	libraryTTL   = 6 * time.Hour
	libraryRetry = 10 * time.Minute // after a failed background refresh
)

// libRefresh tracks the background reload (main loop only, except the channel).
type libRefresh struct {
	running bool
	tried   time.Time
	gen     int // bumped when the cache is cleared: late results are dropped
	c       chan libResult
}

type libResult struct {
	gen int
	lib *library
}

type savedStation struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type library struct {
	Saved     time.Time      `json:"saved"`
	Albums    []album        `json:"albums,omitempty"`
	Artists   []artist       `json:"artists,omitempty"`
	Playlists []playlist     `json:"playlists,omitempty"`
	Recent    []album        `json:"recent,omitempty"`
	Frequent  []album        `json:"frequent,omitempty"`
	Songs     []song         `json:"songs,omitempty"`
	Radios    []savedStation `json:"radios,omitempty"` // the server's stations
	Have      []string       `json:"have,omitempty"`   // which lists are loaded
}

func (l *library) has(k string) bool {
	for _, h := range l.Have {
		if h == k {
			return true
		}
	}
	return false
}

func (a *app) libraryPath() string { return filepath.Join(a.cfg.dir, "library.json") }

func (a *app) loadLibrary() {
	a.lib = &library{}
	b, err := os.ReadFile(a.libraryPath())
	if err != nil {
		return
	}
	var l library
	if json.Unmarshal(b, &l) == nil {
		for _, list := range [][]album{l.Albums, l.Recent, l.Frequent} {
			fillAlbums(list)
		}
		fillArtists(l.Artists)
		fillSongs(l.Songs)
		a.lib = &l
	}
}

func (a *app) saveLibrary() {
	if a.offline {
		return
	}
	a.lib.Saved = time.Now()
	b, err := json.Marshal(a.lib)
	if err != nil {
		return
	}
	tmp := a.libraryPath() + ".new"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		os.Rename(tmp, a.libraryPath())
	} else {
		log.Printf("library.json: %v", err)
	}
}

// ensure loads a list into the cache once; msg is shown while it loads.
func (a *app) ensure(kind, msg string) {
	if a.lib.has(kind) || a.api == nil || a.offline {
		return
	}
	a.busy(msg)
	if err := fetchKind(a.api, a.lib, kind); err != nil {
		if kind != "songs" && kind != "radios" {
			a.fail("Cannot load", err)
		}
		return
	}
	a.lib.Have = append(a.lib.Have, kind)
	a.saveLibrary()
}

// fetchKind loads one list from the server into l (no UI: also used by the
// background refresh).
func fetchKind(api *client, l *library, kind string) error {
	var err error
	switch kind {
	case "albums":
		l.Albums, err = api.allAlbums()
	case "artists":
		l.Artists, err = api.artists()
	case "playlists":
		l.Playlists, err = api.playlists()
	case "recent":
		l.Recent, err = api.albumList("newest", 20, 0)
	case "frequent":
		l.Frequent, err = api.albumList("frequent", 20, 0)
		if err == nil && len(l.Frequent) == 0 {
			l.Frequent, err = api.albumList("random", 20, 0)
		}
	case "songs":
		_, _, l.Songs, err = api.searchAll(5000)
	case "radios":
		var r *response
		if r, err = api.call("getInternetRadioStations", nil); err == nil && r.Radios != nil {
			l.Radios = nil
			for _, s := range r.Radios.Station {
				if s.StreamURL != "" {
					l.Radios = append(l.Radios, savedStation{s.Name, s.StreamURL})
				}
			}
		}
	}
	return err
}

// copyKind moves one list from src to dst.
func copyKind(dst, src *library, kind string) {
	switch kind {
	case "albums":
		dst.Albums = src.Albums
	case "artists":
		dst.Artists = src.Artists
	case "playlists":
		dst.Playlists = src.Playlists
	case "recent":
		dst.Recent = src.Recent
	case "frequent":
		dst.Frequent = src.Frequent
	case "songs":
		dst.Songs = src.Songs
	case "radios":
		dst.Radios = src.Radios
	}
	if !dst.has(kind) {
		dst.Have = append(dst.Have, kind)
	}
}

// maybeRefresh starts a background reload of the loaded lists when the
// cache is older than libraryTTL. Lists that fail keep their cached copy.
func (a *app) maybeRefresh() {
	r := &a.libR
	if r.running || a.api == nil || a.offline || len(a.lib.Have) == 0 ||
		time.Since(a.lib.Saved) < libraryTTL || time.Since(r.tried) < libraryRetry {
		return
	}
	if r.c == nil {
		r.c = make(chan libResult, 1)
	}
	r.running, r.tried = true, time.Now()
	api, gen, c, kinds := a.api, r.gen, r.c, append([]string(nil), a.lib.Have...)
	go func() {
		fresh := &library{}
		for _, k := range kinds {
			if err := fetchKind(api, fresh, k); err != nil {
				log.Printf("library refresh %s: %v", k, err)
				continue
			}
			fresh.Have = append(fresh.Have, k)
		}
		c <- libResult{gen, fresh}
	}()
}

// libraryRefreshed swaps in a background reload (main loop).
func (a *app) libraryRefreshed(res libResult) {
	a.libR.running = false
	if res.gen != a.libR.gen || len(res.lib.Have) == 0 {
		return
	}
	for _, k := range res.lib.Have {
		copyKind(a.lib, res.lib, k)
	}
	a.saveLibrary()
	if a.index != nil {
		a.index, _ = buildIndex(a)
	}
	a.dirty = true
	log.Printf("library refreshed in the background: %v", res.lib.Have)
}

// clearLibrary empties the cache (new account, manual refresh).
func (a *app) clearLibrary() {
	os.Remove(a.libraryPath())
	a.lib, a.index = &library{}, nil
	a.libR.gen++
	a.libR.running = false
	a.home.loaded = false
}

func (a *app) refreshLibrary() {
	a.clearLibrary()
	a.home.load(a)
	a.toast("Library refreshed")
}
