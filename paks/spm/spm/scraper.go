package main

// scraper.go — ScreenScraper client (https://www.screenscraper.fr) for
// `spm search` and `spm info`. Read-only: it only GETs public metadata, never
// writes to the card. Credentials come from the environment or a config file
// and are never logged.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const softname = "spm"

// Credentials identify the developer (devid/devpassword) and the user
// (ssid/sspassword). ScreenScraper requires all four for authenticated data.
type Credentials struct {
	DevID       string `json:"devid"`
	DevPassword string `json:"devpassword"`
	SSID        string `json:"ssid"`
	SSPassword  string `json:"sspassword"`
}

func (c Credentials) complete() bool {
	return c.DevID != "" && c.DevPassword != "" && c.SSID != "" && c.SSPassword != ""
}

// screenScraperCreds loads credentials: environment first (SS_DEVID,
// SS_DEVPASSWORD, SS_ID, SS_PASSWORD), then SD/.userdata/h700/spm/screenscraper.json.
func (e *Env) screenScraperCreds() (Credentials, error) {
	c := Credentials{
		DevID:       os.Getenv("SS_DEVID"),
		DevPassword: os.Getenv("SS_DEVPASSWORD"),
		SSID:        os.Getenv("SS_ID"),
		SSPassword:  os.Getenv("SS_PASSWORD"),
	}
	if c.complete() {
		return c, nil
	}
	path := filepath.Join(e.SD, ".userdata", "h700", "spm", "screenscraper.json")
	if data, err := os.ReadFile(path); err == nil {
		var disk Credentials
		if json.Unmarshal(data, &disk) == nil {
			if c.DevID == "" {
				c.DevID = disk.DevID
			}
			if c.DevPassword == "" {
				c.DevPassword = disk.DevPassword
			}
			if c.SSID == "" {
				c.SSID = disk.SSID
			}
			if c.SSPassword == "" {
				c.SSPassword = disk.SSPassword
			}
		}
		if c.complete() {
			return c, nil
		}
	}
	return Credentials{}, errors.New("ScreenScraper credentials missing: set SS_DEVID/SS_DEVPASSWORD/SS_ID/SS_PASSWORD or " + path)
}

type scraper struct {
	base    string // "https://api.screenscraper.fr/api2/"
	creds   Credentials
	client  *http.Client
	cache   *cache
	refresh bool // bypass the cache and refetch
}

func newScraper(base string, creds Credentials) *scraper {
	if base == "" {
		base = "https://api.screenscraper.fr/api2/"
	}
	return &scraper{base: base, creds: creds, client: &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// cached returns a fresh cached response, or fetches and stores it. The key is
// the non-secret query; credentials never reach the cache file.
func (s *scraper) cached(key string, fetch func() (json.RawMessage, error)) (json.RawMessage, error) {
	if s.cache != nil && !s.refresh {
		if raw, ok := s.cache.read(key); ok {
			return raw, nil
		}
	}
	raw, err := fetch()
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		_ = s.cache.write(key, raw)
	}
	return raw, nil
}

// refreshAll refreshes stale metadata; --refresh explicitly refreshes all.
func (s *scraper) refreshAll() (updated, failed int) {
	if s.cache == nil {
		return 0, 0
	}
	for _, key := range s.cache.queries() {
		if !s.refresh {
			if _, fresh := s.cache.read(key); fresh {
				continue
			}
		}
		endpoint, params, ok := splitQuery(key)
		if !ok {
			continue
		}
		raw, err := s.get(endpoint, url.Values(params))
		if err != nil {
			failed++
			continue
		}
		if s.cache.write(key, raw) != nil {
			failed++
			continue
		}
		updated++
	}
	return updated, failed
}

// get calls one endpoint with the credentials merged in and returns the
// "response" object. ScreenScraper reports application errors both as HTTP
// statuses and as a header.error with HTTP 200.
func (s *scraper) get(endpoint string, params url.Values) (json.RawMessage, error) {
	if endpoint != "jeuRecherche.php" && endpoint != "jeuInfos.php" {
		return nil, errors.New("screenscraper: unsupported metadata endpoint")
	}
	for key := range params {
		if key != "game" && key != "gameid" && key != "systemeid" {
			return nil, errors.New("screenscraper: unsupported metadata parameter")
		}
	}
	if !s.creds.complete() {
		return nil, errors.New("ScreenScraper credentials missing: set SS_DEVID/SS_DEVPASSWORD/SS_ID/SS_PASSWORD or screenscraper.json")
	}
	query := make(url.Values, len(params)+6)
	for key, values := range params {
		query[key] = append([]string(nil), values...)
	}
	params = query
	params.Set("devid", s.creds.DevID)
	params.Set("devpassword", s.creds.DevPassword)
	params.Set("ssid", s.creds.SSID)
	params.Set("sspassword", s.creds.SSPassword)
	params.Set("softname", softname)
	params.Set("output", "json")

	resp, err := s.client.Get(s.base + endpoint + "?" + params.Encode())
	if err != nil {
		// http.Client errors normally include the entire credential-bearing URL.
		return nil, errors.New("screenscraper: network request failed (connection, timeout or TLS)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("screenscraper: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, errors.New("screenscraper: response read failed")
	}
	if len(body) > 4<<20 {
		return nil, errors.New("screenscraper: response too large")
	}
	var envelope struct {
		Header struct {
			Error string `json:"error"`
		} `json:"header"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("screenscraper: invalid reply")
	}
	if envelope.Header.Error != "" {
		return nil, errors.New("screenscraper: API rejected request; check developer access, account and quota")
	}
	if len(envelope.Response) == 0 || string(envelope.Response) == "null" {
		return nil, errors.New("screenscraper: empty response")
	}
	return envelope.Response, nil
}

type ssName struct {
	Region string `json:"region"`
	Text   string `json:"text"`
}
type ssLocalized struct {
	Lang string `json:"langue"`
	Text string `json:"text"`
}

// pickName prefers the ScreenScraper canonical region, then the broad ones.
func pickName(noms []ssName) string {
	for _, prefer := range []string{"ss", "wor", "us", "eu", "jp"} {
		for _, n := range noms {
			if n.Region == prefer && n.Text != "" {
				return n.Text
			}
		}
	}
	for _, n := range noms {
		if n.Text != "" {
			return n.Text
		}
	}
	return ""
}

func pickYear(dates []ssName) string {
	for _, d := range dates {
		if len(d.Text) >= 4 {
			return d.Text[:4]
		}
	}
	return ""
}

// SearchHit is one match of `spm search`.
type SearchHit struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	System   string `json:"system"`
	Year     string `json:"year"`
	Synopsis string `json:"synopsis,omitempty"`
}

// search finds games by name (jeuRecherche.php), cached.
func (s *scraper) search(term string) ([]SearchHit, error) {
	params := url.Values{"game": {term}}
	raw, err := s.cached("jeuRecherche.php?"+params.Encode(), func() (json.RawMessage, error) {
		return s.get("jeuRecherche.php", params)
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Jeux []struct {
			ID       json.Number           `json:"id"`
			Noms     []ssName              `json:"noms"`
			Systeme  struct{ Text string } `json:"systeme"`
			Dates    []ssName              `json:"dates"`
			Synopsis []ssLocalized         `json:"synopsis"`
		} `json:"jeux"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("screenscraper: unparsable search result")
	}
	hits := make([]SearchHit, 0, len(resp.Jeux))
	for _, j := range resp.Jeux {
		desc := ""
		for _, s := range j.Synopsis {
			if s.Lang == "en" || s.Lang == "fr" || desc == "" {
				desc = s.Text
			}
		}
		hits = append(hits, SearchHit{
			ID: j.ID.String(), Name: pickName(j.Noms), System: j.Systeme.Text,
			Year: pickYear(j.Dates), Synopsis: desc})
	}
	return hits, nil
}

// GameInfo is the full record of `spm info <id>`.
type GameInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	System      string   `json:"system"`
	Year        string   `json:"year"`
	Publisher   string   `json:"publisher"`
	Developer   string   `json:"developer"`
	Players     string   `json:"players"`
	Rating      string   `json:"rating"`
	Genres      []string `json:"genres"`
	Description string   `json:"description"`
}

// info fetches one full game record (jeuInfos.php?gameid=), cached.
func (s *scraper) info(id string) (*GameInfo, error) {
	params := url.Values{"gameid": {id}}
	raw, err := s.cached("jeuInfos.php?"+params.Encode(), func() (json.RawMessage, error) {
		return s.get("jeuInfos.php", params)
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Jeu struct {
			ID          json.Number           `json:"id"`
			Noms        []ssName              `json:"noms"`
			Systeme     struct{ Text string } `json:"systeme"`
			Dates       []ssName              `json:"dates"`
			Editeur     struct{ Text string } `json:"editeur"`
			Developpeur struct{ Text string } `json:"developpeur"`
			Joueurs     struct{ Text string } `json:"joueurs"`
			Note        struct{ Text string } `json:"note"`
			Synopsis    []ssLocalized         `json:"synopsis"`
			Genres      []struct {
				Noms []ssLocalized `json:"noms"`
			} `json:"genres"`
		} `json:"jeu"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("screenscraper: unparsable game record")
	}
	desc := ""
	for _, s := range resp.Jeu.Synopsis {
		if s.Lang == "fr" || (s.Lang == "en" && desc == "") {
			desc = s.Text
		}
	}
	var genres []string
	for _, g := range resp.Jeu.Genres {
		for _, n := range g.Noms {
			if n.Lang == "fr" || n.Lang == "en" {
				genres = append(genres, n.Text)
				break
			}
		}
	}
	g := resp.Jeu
	return &GameInfo{
		ID: g.ID.String(), Name: pickName(g.Noms), System: g.Systeme.Text,
		Year: pickYear(g.Dates), Publisher: g.Editeur.Text, Developer: g.Developpeur.Text,
		Players: g.Joueurs.Text, Rating: g.Note.Text, Genres: genres, Description: desc}, nil
}

// description folds long synopses to one line per search hit.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}
