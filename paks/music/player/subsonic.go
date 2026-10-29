package main

// Minimal Subsonic API client (1.16.1, JSON). Token auth only: the password
// itself never reaches the console. Works with Navidrome, smolsonic and any
// server that follows the Subsonic API.

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const apiVersion = "1.16.1"
const clientName = "sp-music"

// flexString accepts a JSON string or number (servers differ on ids).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	*f = flexString(bytes.Trim(b, " "))
	if *f == "null" {
		*f = ""
	}
	return nil
}

// flexInt accepts a JSON number or a numeric string.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `" `)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil // tolerate odd values rather than failing a whole list
	}
	*f = flexInt(v)
	return nil
}

type song struct {
	ID          flexString `json:"id"`
	Title       string     `json:"title"`
	Artist      string     `json:"artist"`
	Album       string     `json:"album"`
	AlbumID     flexString `json:"albumId"`
	CoverArt    flexString `json:"coverArt"`
	Duration    flexInt    `json:"duration"`
	Track       flexInt    `json:"track"`
	Suffix      string     `json:"suffix"`
	ContentType string     `json:"contentType"`
	BitRate     flexInt    `json:"bitRate"`
	Starred     string     `json:"starred,omitempty"`
}

type album struct {
	ID        flexString `json:"id"`
	Name      string     `json:"name"`
	Title     string     `json:"title"` // getAlbumList (non-ID3) style
	Artist    string     `json:"artist"`
	CoverArt  flexString `json:"coverArt"`
	SongCount flexInt    `json:"songCount"`
	Year      flexInt    `json:"year"`
	Starred   string     `json:"starred"`
	Song      []song     `json:"song"`
}

func (a album) name() string {
	if a.Name != "" {
		return a.Name
	}
	return a.Title
}

type artist struct {
	ID         flexString `json:"id"`
	Name       string     `json:"name"`
	AlbumCount flexInt    `json:"albumCount"`
	Album      []album    `json:"album"`
}

type playlist struct {
	ID        flexString `json:"id"`
	CoverArt  flexString `json:"coverArt"`
	Name      string     `json:"name"`
	SongCount flexInt    `json:"songCount"`
	Entry     []song     `json:"entry"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string {
	switch e.Code {
	case 40:
		return "wrong user name or password"
	case 41:
		return "token sign-in not supported by the server"
	case 70:
		return "not found"
	}
	return fmt.Sprintf("server error %d: %s", e.Code, e.Message)
}

type response struct {
	Status        string    `json:"status"`
	Version       string    `json:"version"`
	Type          string    `json:"type"`
	ServerVersion string    `json:"serverVersion"`
	Error         *apiError `json:"error"`
	Artists       *struct {
		Index []struct {
			Artist []artist `json:"artist"`
		} `json:"index"`
	} `json:"artists"`
	Artist    *artist `json:"artist"`
	Album     *album  `json:"album"`
	AlbumList *struct {
		Album []album `json:"album"`
	} `json:"albumList2"`
	Playlists *struct {
		Playlist []playlist `json:"playlist"`
	} `json:"playlists"`
	Playlist *playlist `json:"playlist"`
	Starred  *struct {
		Song  []song  `json:"song"`
		Album []album `json:"album"`
	} `json:"starred2"`
	RandomSongs *struct {
		Song []song `json:"song"`
	} `json:"randomSongs"`
	Radios *struct {
		Station []struct {
			ID        flexString `json:"id"`
			Name      string     `json:"name"`
			StreamURL string     `json:"streamUrl"`
		} `json:"internetRadioStation"`
	} `json:"internetRadioStations"`
	Search *struct {
		Artist []artist `json:"artist"`
		Album  []album  `json:"album"`
		Song   []song   `json:"song"`
	} `json:"searchResult3"`
}

type client struct {
	base              *url.URL
	user, token, salt string
	http              *http.Client
	transport         *http.Transport
}

// newTransport: system roots + the optional extra CA, and an optional fixed
// IP for the server host (the TLS name stays the host from the URL).
func newTransport(cfg *config, host string) (*http.Transport, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if cfg.CA != "" {
		pem, err := os.ReadFile(cfg.CA)
		if err != nil {
			return nil, fmt.Errorf("cannot read CA file: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA file has no certificate")
		}
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	t := &http.Transport{
		TLSClientConfig:       &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConnsPerHost:   4,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if cfg.IP != "" {
				if h, p, err := net.SplitHostPort(addr); err == nil && strings.EqualFold(h, host) {
					addr = net.JoinHostPort(cfg.IP, p)
				}
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	return t, nil
}

func newClient(cfg *config) (*client, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad server url %q", cfg.URL)
	}
	if !strings.HasSuffix(u.Path, "/rest") {
		u.Path = strings.TrimRight(u.Path, "/") + "/rest"
	}
	t, err := newTransport(cfg, u.Hostname())
	if err != nil {
		return nil, err
	}
	c := &client{base: u, user: cfg.User, token: cfg.Token, salt: cfg.Salt, transport: t,
		http: &http.Client{Transport: t, Timeout: 20 * time.Second}}
	if c.token == "" { // password in config: derive a token once per run
		c.salt = randomSalt()
		c.token = makeToken(cfg.Password, c.salt)
	}
	return c, nil
}

func randomSalt() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

func makeToken(password, salt string) string {
	sum := md5.Sum([]byte(password + salt))
	return hex.EncodeToString(sum[:])
}

// endpoint returns the full URL of a method with auth (never log it).
func (c *client) endpoint(method string, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	q.Set("u", c.user)
	q.Set("t", c.token)
	q.Set("s", c.salt)
	q.Set("v", apiVersion)
	q.Set("c", clientName)
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + method
	u.RawQuery = q.Encode()
	return u.String()
}

// cleanErr drops the URL (and its token) from transport errors.
func cleanErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return errors.New("server did not answer in time")
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("cannot find %s (DNS)", dnsErr.Name)
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return errors.New("server certificate not trusted (check ca.pem)")
	}
	if strings.Contains(err.Error(), "connect: network is unreachable") {
		return errors.New("no network (Wi-Fi off?)")
	}
	if strings.Contains(err.Error(), "connection refused") {
		return errors.New("server refused the connection")
	}
	return err
}

func (c *client) call(method string, q url.Values) (*response, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("f", "json")
	resp, err := c.http.Get(c.endpoint(method, q))
	if err != nil {
		return nil, cleanErr(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, cleanErr(err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("server answered HTTP %d", resp.StatusCode)
	}
	var env struct {
		R response `json:"subsonic-response"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("unexpected answer from server: %v", err)
	}
	if env.R.Error != nil {
		return nil, env.R.Error
	}
	fillUnknown(&env.R)
	if env.R.Status != "ok" {
		return nil, fmt.Errorf("server status %q", env.R.Status)
	}
	return &env.R, nil
}

// Empty names read as commercial players show them (Apple Music, Spotify):
// "Unknown Album", "Unknown Artist", "Untitled".
const unknownAlbum, unknownArtist, untitled = "Unknown Album", "Unknown Artist", "Untitled"

func orDefault(s *string, def string) {
	if strings.TrimSpace(*s) == "" {
		*s = def
	}
}

func fillSongs(songs []song) {
	for i := range songs {
		orDefault(&songs[i].Title, untitled)
		orDefault(&songs[i].Artist, unknownArtist)
		orDefault(&songs[i].Album, unknownAlbum)
	}
}

func fillAlbums(albums []album) {
	for i := range albums {
		if strings.TrimSpace(albums[i].name()) == "" {
			albums[i].Name = unknownAlbum
		}
		orDefault(&albums[i].Artist, unknownArtist)
		fillSongs(albums[i].Song)
	}
}

func fillArtists(artists []artist) {
	for i := range artists {
		orDefault(&artists[i].Name, unknownArtist)
		fillAlbums(artists[i].Album)
	}
}

func fillUnknown(r *response) {
	if r.Artists != nil {
		for i := range r.Artists.Index {
			fillArtists(r.Artists.Index[i].Artist)
		}
	}
	if r.Artist != nil {
		orDefault(&r.Artist.Name, unknownArtist)
		fillAlbums(r.Artist.Album)
	}
	if r.Album != nil {
		a := []album{*r.Album}
		fillAlbums(a)
		*r.Album = a[0]
	}
	if r.AlbumList != nil {
		fillAlbums(r.AlbumList.Album)
	}
	if r.Playlists != nil {
		for i := range r.Playlists.Playlist {
			orDefault(&r.Playlists.Playlist[i].Name, "Untitled Playlist")
		}
	}
	if r.Playlist != nil {
		orDefault(&r.Playlist.Name, "Untitled Playlist")
		fillSongs(r.Playlist.Entry)
	}
	if r.Starred != nil {
		fillSongs(r.Starred.Song)
		fillAlbums(r.Starred.Album)
	}
	if r.RandomSongs != nil {
		fillSongs(r.RandomSongs.Song)
	}
	if r.Search != nil {
		fillArtists(r.Search.Artist)
		fillAlbums(r.Search.Album)
		fillSongs(r.Search.Song)
	}
}

func (c *client) ping() (*response, error) { return c.call("ping", nil) }

func (c *client) artists() ([]artist, error) {
	r, err := c.call("getArtists", nil)
	if err != nil {
		return nil, err
	}
	var out []artist
	if r.Artists != nil {
		for _, ix := range r.Artists.Index {
			out = append(out, ix.Artist...)
		}
	}
	return out, nil
}

func (c *client) artist(id string) (*artist, error) {
	r, err := c.call("getArtist", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	if r.Artist == nil {
		return nil, errors.New("artist not found")
	}
	return r.Artist, nil
}

// albumList: type is newest, alphabeticalByName, random, frequent, recent, starred.
func (c *client) albumList(kind string, size, offset int) ([]album, error) {
	r, err := c.call("getAlbumList2", url.Values{"type": {kind}, "size": {strconv.Itoa(size)}, "offset": {strconv.Itoa(offset)}})
	if err != nil {
		return nil, err
	}
	if r.AlbumList == nil {
		return nil, nil
	}
	return r.AlbumList.Album, nil
}

// allAlbums pages through alphabeticalByName (500 per call, 5000 max).
func (c *client) allAlbums() ([]album, error) {
	var out []album
	for off := 0; off < 5000; off += 500 {
		page, err := c.albumList("alphabeticalByName", 500, off)
		if err != nil {
			return out, err
		}
		out = append(out, page...)
		if len(page) < 500 {
			break
		}
	}
	return out, nil
}

func (c *client) album(id string) (*album, error) {
	r, err := c.call("getAlbum", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	if r.Album == nil {
		return nil, errors.New("album not found")
	}
	return r.Album, nil
}

func (c *client) playlists() ([]playlist, error) {
	r, err := c.call("getPlaylists", nil)
	if err != nil {
		return nil, err
	}
	if r.Playlists == nil {
		return nil, nil
	}
	return r.Playlists.Playlist, nil
}

func (c *client) playlist(id string) (*playlist, error) {
	r, err := c.call("getPlaylist", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	if r.Playlist == nil {
		return nil, errors.New("playlist not found")
	}
	return r.Playlist, nil
}

func (c *client) starred() ([]song, []album, error) {
	r, err := c.call("getStarred2", nil)
	if err != nil {
		return nil, nil, err
	}
	if r.Starred == nil {
		return nil, nil, nil
	}
	return r.Starred.Song, r.Starred.Album, nil
}

func (c *client) randomSongs(n int) ([]song, error) {
	r, err := c.call("getRandomSongs", url.Values{"size": {strconv.Itoa(n)}})
	if err != nil {
		return nil, err
	}
	if r.RandomSongs == nil {
		return nil, nil
	}
	return r.RandomSongs.Song, nil
}

func (c *client) search(query string) ([]artist, []album, []song, error) {
	r, err := c.call("search3", url.Values{"query": {query}, "artistCount": {"20"}, "albumCount": {"30"}, "songCount": {"60"}})
	if err != nil {
		return nil, nil, nil, err
	}
	if r.Search == nil {
		return nil, nil, nil, nil
	}
	return r.Search.Artist, r.Search.Album, r.Search.Song, nil
}

// searchAll lists the whole library with an empty search3 query (Navidrome
// and most servers accept it); used to build the search index.
func (c *client) searchAll(songs int) ([]artist, []album, []song, error) {
	r, err := c.call("search3", url.Values{"query": {""}, "artistCount": {"0"}, "albumCount": {"0"}, "songCount": {strconv.Itoa(songs)}})
	if err != nil {
		return nil, nil, nil, err
	}
	if r.Search == nil {
		return nil, nil, nil, nil
	}
	return r.Search.Artist, r.Search.Album, r.Search.Song, nil
}

func (c *client) star(id string, on bool) error {
	m := "star"
	if !on {
		m = "unstar"
	}
	_, err := c.call(m, url.Values{"id": {id}})
	return err
}

func (c *client) scrobble(id string, submission bool) error {
	_, err := c.call("scrobble", url.Values{"id": {id}, "submission": {strconv.FormatBool(submission)},
		"time": {strconv.FormatInt(time.Now().UnixMilli(), 10)}})
	return err
}

func (c *client) coverArt(id string, size int) ([]byte, error) {
	resp, err := c.http.Get(c.endpoint("getCoverArt", url.Values{"id": {id}, "size": {strconv.Itoa(size)}}))
	if err != nil {
		return nil, cleanErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return nil, fmt.Errorf("no cover (HTTP %d)", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}
