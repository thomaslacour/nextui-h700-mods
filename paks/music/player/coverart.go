package main

// Covers. getCoverArt first; servers without it (smolsonic answers 404) get
// the picture embedded in the song itself, read with HTTP ranges: ID3v2 APIC
// (MP3), FLAC PICTURE, MP4 covr (M4A/ALAC); last, the free MusicBrainz +
// Cover Art Archive databases (artist + album name, no account). Results are
// cached on the SD card, scaled to coverPx, so each cover is fetched once.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func ifaceHasIPv4(name string) bool {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return false
	}
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			return true
		}
	}
	return false
}

var errNoArt = errors.New("no embedded picture")

func (c *client) streamRange(songID string, off, n int64) ([]byte, int64, error) {
	req, _ := http.NewRequest("GET", c.endpoint("stream", url.Values{"id": {songID}}), nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+n-1))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, cleanErr(err)
	}
	defer resp.Body.Close()
	total := resp.ContentLength
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndex(cr, "/"); i >= 0 {
			fmt.Sscan(cr[i+1:], &total)
		}
	}
	if resp.StatusCode != 206 && resp.StatusCode != 200 {
		return nil, 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode == 200 && off > 0 { // ranges ignored: skip ahead
		if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
			return nil, total, err
		}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, n))
	return b, total, err
}

// embeddedArt returns the raw picture bytes of a song.
func (c *client) embeddedArt(songID string) ([]byte, error) {
	head, total, err := c.streamRange(songID, 0, 64<<10)
	if err != nil {
		return nil, err
	}
	switch {
	case len(head) >= 10 && string(head[:3]) == "ID3":
		size := int64(head[6]&0x7f)<<21 | int64(head[7]&0x7f)<<14 | int64(head[8]&0x7f)<<7 | int64(head[9]&0x7f)
		if size+10 > int64(len(head)) {
			if head, _, err = c.streamRange(songID, 0, min(size+10, 16<<20)); err != nil {
				return nil, err
			}
		}
		return id3Picture(head)
	case len(head) >= 4 && string(head[:4]) == "fLaC":
		return c.flacPicture(songID, head)
	case len(head) >= 8 && string(head[4:8]) == "ftyp":
		return c.mp4Picture(songID, total)
	}
	return nil, errNoArt
}

func id3Picture(b []byte) ([]byte, error) {
	ver := b[3]
	size := int(b[6]&0x7f)<<21 | int(b[7]&0x7f)<<14 | int(b[8]&0x7f)<<7 | int(b[9]&0x7f)
	end := min(10+size, len(b))
	pos := 10
	if b[5]&0x40 != 0 && pos+4 <= end { // extended header
		pos += int(binary.BigEndian.Uint32(b[pos:]))
	}
	for pos+10 <= end {
		var id string
		var fs int
		if ver == 2 {
			id, fs = string(b[pos:pos+3]), int(b[pos+3])<<16|int(b[pos+4])<<8|int(b[pos+5])
			pos += 6
		} else {
			id = string(b[pos : pos+4])
			raw := b[pos+4 : pos+8]
			if ver >= 4 {
				fs = int(raw[0]&0x7f)<<21 | int(raw[1]&0x7f)<<14 | int(raw[2]&0x7f)<<7 | int(raw[3]&0x7f)
			} else {
				fs = int(binary.BigEndian.Uint32(raw))
			}
			pos += 10
		}
		if id[0] == 0 || fs <= 0 || pos+fs > end {
			break
		}
		f := b[pos : pos+fs]
		pos += fs
		if id != "APIC" && id != "PIC" || len(f) < 4 {
			continue
		}
		enc := f[0]
		i := 1
		if id == "PIC" {
			i += 3
		} else {
			j := bytes.IndexByte(f[i:], 0)
			if j < 0 {
				continue
			}
			i += j + 1
		}
		i++ // picture type
		if i >= len(f) {
			continue
		}
		if enc == 1 || enc == 2 { // UTF-16 description: ends with 00 00
			for i+1 < len(f) && !(f[i] == 0 && f[i+1] == 0) {
				i += 2
			}
			i += 2
		} else {
			j := bytes.IndexByte(f[i:], 0)
			if j < 0 {
				continue
			}
			i += j + 1
		}
		if i < len(f) {
			return f[i:], nil
		}
	}
	return nil, errNoArt
}

// flacPictureData reads a PICTURE block body, every length checked.
func flacPictureData(blk []byte) ([]byte, error) {
	p := 4 // picture type
	u32 := func() (int, bool) {
		if p < 0 || p+4 > len(blk) {
			return 0, false
		}
		v := int(binary.BigEndian.Uint32(blk[p:]))
		p += 4
		return v, v >= 0 && v <= len(blk)
	}
	ml, ok := u32()
	if !ok {
		return nil, errNoArt
	}
	p += ml
	dl, ok := u32()
	if !ok {
		return nil, errNoArt
	}
	p += dl + 16
	sz, ok := u32()
	if !ok || sz == 0 || p+sz > len(blk) {
		return nil, errNoArt
	}
	return blk[p : p+sz], nil
}

func (c *client) flacPicture(songID string, head []byte) ([]byte, error) {
	pos := int64(4)
	buf, base := head, int64(0)
	get := func(off, n int64) ([]byte, error) {
		if off-base >= 0 && off-base+n <= int64(len(buf)) {
			return buf[off-base : off-base+n], nil
		}
		b, _, err := c.streamRange(songID, off, max(n, 64<<10))
		if err != nil {
			return nil, err
		}
		if int64(len(b)) < n {
			return nil, errNoArt
		}
		buf, base = b, off
		return b[:n], nil
	}
	for i := 0; i < 64; i++ {
		h, err := get(pos, 4)
		if err != nil {
			return nil, err
		}
		last, typ := h[0]&0x80 != 0, h[0]&0x7f
		n := int64(h[1])<<16 | int64(h[2])<<8 | int64(h[3])
		if typ == 6 {
			if n > 16<<20 {
				return nil, errNoArt
			}
			blk, err := get(pos+4, n)
			if err != nil {
				return nil, err
			}
			return flacPictureData(blk)
		}
		if last {
			break
		}
		pos += 4 + n
	}
	return nil, errNoArt
}

// mp4Picture walks the top-level atoms with small range reads, fetches
// moov (start or end of the file) and digs moov/udta/meta/ilst/covr/data.
func (c *client) mp4Picture(songID string, total int64) ([]byte, error) {
	var off int64
	for i := 0; i < 32 && (total <= 0 || off < total); i++ {
		h, _, err := c.streamRange(songID, off, 16)
		if err != nil || len(h) < 8 {
			return nil, errNoArt
		}
		size := int64(binary.BigEndian.Uint32(h))
		typ := string(h[4:8])
		hl := int64(8)
		if size == 1 && len(h) >= 16 {
			size, hl = int64(binary.BigEndian.Uint64(h[8:])), 16
		}
		if size < hl {
			return nil, errNoArt
		}
		if typ == "moov" {
			if size > 32<<20 {
				return nil, errNoArt
			}
			moov, _, err := c.streamRange(songID, off+hl, size-hl)
			if err != nil {
				return nil, err
			}
			return mp4Covr(moov)
		}
		off += size
	}
	return nil, errNoArt
}

func mp4Child(b []byte, name string) []byte {
	for p := 0; p+8 <= len(b); {
		size := int(binary.BigEndian.Uint32(b[p:]))
		if size < 8 || p+size > len(b) {
			return nil
		}
		if string(b[p+4:p+8]) == name {
			return b[p+8 : p+size]
		}
		p += size
	}
	return nil
}

func mp4Covr(moov []byte) ([]byte, error) {
	udta := mp4Child(moov, "udta")
	meta := mp4Child(udta, "meta")
	if len(meta) < 4 {
		return nil, errNoArt
	}
	covr := mp4Child(mp4Child(meta[4:], "ilst"), "covr")
	data := mp4Child(covr, "data")
	if len(data) <= 8 {
		return nil, errNoArt
	}
	return data[8:], nil
}

// ---------------------------------------------------------------- cache

func (a *app) coverDir() string { return filepath.Join(a.cfg.dir, "covers") }

func coverFile(dir, id string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
	return filepath.Join(dir, safe)
}

// fetchCover: disk cache, then getCoverArt, then the embedded picture of
// the album's first song (or of the song itself). nil, nil = no cover.
func (a *app) fetchCover(id string) (img *image.RGBA, err error) {
	defer func() { // a broken file must never crash the player
		if r := recover(); r != nil {
			img, err = nil, fmt.Errorf("unreadable picture: %v", r)
		}
	}()
	return a.fetchCoverSafe(id)
}

func (a *app) fetchCoverSafe(id string) (*image.RGBA, error) {
	base := coverFile(a.coverDir(), id)
	if f, err := os.Open(base + ".jpg"); err == nil {
		img, err := jpeg.Decode(f)
		f.Close()
		if err == nil {
			return boxScale(img, coverPx, coverPx), nil
		}
	}
	if _, err := os.Stat(base + ".none"); err == nil {
		return nil, nil
	}
	raw, err := a.api.coverArt(id, coverPx)
	if err != nil {
		songID, artistName, albumName := id, "", ""
		if strings.HasPrefix(id, "so-") {
			if s, ok := a.meta[id]; ok {
				artistName, albumName = s.Artist, s.Album
			}
		} else if al, aerr := a.api.album(id); aerr == nil {
			artistName, albumName = al.Artist, al.name()
			if len(al.Song) > 0 {
				songID = string(al.Song[0].ID)
			}
		}
		raw, err = a.api.embeddedArt(songID)
		if err == errNoArt && albumName != "" && albumName != unknownAlbum && a.cfg.setting("online_covers", "on") == "on" {
			raw, err = onlineCover(artistName, albumName)
		}
	}
	if err != nil || len(raw) == 0 {
		if err == errNoArt || err == nil {
			os.MkdirAll(a.coverDir(), 0o755)
			os.WriteFile(base+".none", nil, 0o644)
			return nil, nil
		}
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		os.MkdirAll(a.coverDir(), 0o755)
		os.WriteFile(base+".none", nil, 0o644)
		return nil, nil
	}
	small := boxScale(img, coverPx, coverPx)
	os.MkdirAll(a.coverDir(), 0o755)
	if f, err := os.Create(base + ".jpg.new"); err == nil {
		jpeg.Encode(f, small, &jpeg.Options{Quality: 88})
		f.Close()
		os.Rename(base+".jpg.new", base+".jpg")
	}
	return small, nil
}

// ---------------------------------------------------------------- online

const mbAgent = "sp-music/1.0 (RG35XX SP Navidrome player)"

var mbLast = make(chan time.Time, 1)

func init() { mbLast <- time.Time{} }

// mbWait keeps MusicBrainz calls at most one per second (their rule).
func mbWait() {
	last := <-mbLast
	if d := time.Second - time.Since(last); d > 0 {
		time.Sleep(d)
	}
	mbLast <- time.Now()
}

var webClient = &http.Client{Timeout: 15 * time.Second}

func mbQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// onlineCover: release group by album + artist on MusicBrainz, then its
// front cover (250 px) on the Cover Art Archive. errNoArt when not found.
func onlineCover(artistName, albumName string) ([]byte, error) {
	q := "releasegroup:" + mbQuote(albumName)
	if artistName != "" {
		q += " AND artist:" + mbQuote(artistName)
	}
	mbWait()
	req, _ := http.NewRequest("GET", "https://musicbrainz.org/ws/2/release-group/?"+url.Values{"query": {q}, "fmt": {"json"}, "limit": {"3"}}.Encode(), nil)
	req.Header.Set("User-Agent", mbAgent)
	resp, err := webClient.Do(req)
	if err != nil {
		return nil, cleanErr(err)
	}
	var found struct {
		Groups []struct {
			ID    string `json:"id"`
			Score int    `json:"score"`
		} `json:"release-groups"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&found)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	for _, g := range found.Groups {
		if g.Score < 90 {
			continue
		}
		req, _ := http.NewRequest("GET", "https://coverartarchive.org/release-group/"+g.ID+"/front-250", nil)
		req.Header.Set("User-Agent", mbAgent)
		resp, err := webClient.Do(req)
		if err != nil {
			return nil, cleanErr(err)
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == 200 && len(b) > 0 {
			return b, nil
		}
	}
	return nil, errNoArt
}
