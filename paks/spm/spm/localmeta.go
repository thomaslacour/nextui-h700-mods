package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

type localGame struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	System      string `json:"system"`
	Desc        string `json:"description"`
	Releasedate string `json:"releasedate"`
	Developer   string `json:"developer"`
	Publisher   string `json:"publisher"`
	Genre       string `json:"genre"`
	Image       string `json:"image"`
	Players     string `json:"players"`
	Rating      string `json:"rating"`
}

type gamelist struct {
	XMLName xml.Name `xml:"gameList"`
	Games   []game   `xml:"game"`
	Folders []folder `xml:"folder"`
}

type game struct {
	Path        string `xml:"path"`
	Name        string `xml:"name"`
	Desc        string `xml:"desc"`
	Image       string `xml:"image"`
	Releasedate string `xml:"releasedate"`
	Developer   string `xml:"developer"`
	Publisher   string `xml:"publisher"`
	Genre       string `xml:"genre"`
	ID          string `xml:"id"`
	Players     string `xml:"players"`
	Rating      string `xml:"rating"`
}

type folder struct {
	Path string `xml:"path"`
	Name string `xml:"name"`
}

func (e *Env) gamelistPaths() []string {
	var out []string
	out = append(out, filepath.Join(e.SD, "gamelist.xml"))
	dirs, err := os.ReadDir(e.roms())
	if err != nil {
		return out
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		out = append(out, filepath.Join(e.roms(), name, "gamelist.xml"))
	}
	return out
}

func readGamelist(path string, e *Env) ([]localGame, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("gamelist too large")
	}
	var gl gamelist
	if err := xml.Unmarshal(data, &gl); err != nil {
		return nil, err
	}
	system := ""
	dir := filepath.Dir(path)
	base := filepath.Base(dir)
	if base != filepath.Base(e.SD) && dir != e.roms() {
		system = systemName(base)
	}
	var out []localGame
	for _, g := range gl.Games {
		p := g.Path
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Clean(filepath.Join(filepath.Dir(path), p))
		}
		out = append(out, localGame{
			ID:          g.ID,
			Path:        p,
			Name:        g.Name,
			System:      system,
			Desc:        g.Desc,
			Releasedate: g.Releasedate,
			Developer:   g.Developer,
			Publisher:   g.Publisher,
			Genre:       g.Genre,
			Image:       g.Image,
			Players:     g.Players,
			Rating:      g.Rating,
		})
	}
	return out, nil
}

func (e *Env) localIndex() ([]localGame, error) {
	roms, err := e.scanROMs()
	if err != nil {
		return nil, err
	}
	// The ROM inventory is authoritative; XML can only enrich existing games.
	// Canonicalize root aliases (/mnt/SDCARD -> /mnt/sdcard) for exact joins.
	canonical := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
		absolute, err := filepath.Abs(p)
		if err == nil {
			p = absolute
		}
		return filepath.Clean(p)
	}
	byPath := map[string]int{}
	all := make([]localGame, 0, len(roms))
	for _, rom := range roms {
		byPath[canonical(rom.Path)] = len(all)
		all = append(all, localGame{ID: e.localRel(rom.Path), Path: rom.Path, Name: rom.Name, System: rom.System})
	}
	for _, p := range e.gamelistPaths() {
		games, err := readGamelist(p, e)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "spm: ignoring optional metadata %q: %v\n", p, err)
			continue
		}
		for _, g := range games {
			i, ok := byPath[canonical(g.Path)]
			if !ok {
				continue
			}
			base := all[i]
			g.ID, g.Path, g.System = base.ID, base.Path, base.System
			if g.Name == "" {
				g.Name = base.Name
			}
			all[i] = g
		}
	}
	sort.Slice(all, func(i, j int) bool {
		ni, nj := strings.ToLower(all[i].Name), strings.ToLower(all[j].Name)
		if ni == nj {
			return all[i].Path < all[j].Path
		}
		return ni < nj
	})
	return all, nil
}

func (e *Env) searchLocal(term string) ([]localGame, error) {
	term = strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return nil, fmt.Errorf("search needs a term")
	}
	all, err := e.localIndex()
	if err != nil {
		return nil, err
	}
	var out []localGame
	for _, g := range all {
		name := strings.ToLower(g.Name)
		genre := strings.ToLower(g.Genre)
		dev := strings.ToLower(g.Developer)
		pub := strings.ToLower(g.Publisher)
		desc := strings.ToLower(g.Desc)
		base := ""
		if g.Path != "" {
			base = strings.ToLower(filepath.Base(g.Path))
		}
		text := strings.Join([]string{name, base, genre, dev, pub, desc, strings.ToLower(g.System)}, " ")
		matches := true
		for _, word := range strings.Fields(term) {
			if !strings.Contains(text, word) {
				matches = false
				break
			}
		}
		if matches {
			out = append(out, g)
		}
	}
	return out, nil
}

func (e *Env) localInfo(id string) (*localGame, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("info needs an id, path or title")
	}
	all, err := e.localIndex()
	if err != nil {
		return nil, err
	}
	low := strings.ToLower(id)
	// Prefer stable ROM-relative IDs/paths, then names. Never pick an arbitrary
	// game when the same basename or title exists on several systems.
	for level := 0; level < 3; level++ {
		var matches []localGame
		for _, g := range all {
			match := g.ID == id || g.Path == id
			if level == 1 {
				match = strings.EqualFold(filepath.Base(g.Path), id) || strings.EqualFold(g.Name, id)
			}
			if level == 2 {
				match = strings.Contains(strings.ToLower(g.Name), low)
			}
			if match {
				matches = append(matches, g)
			}
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("ambiguous game: use a ROM-relative ID from spm search")
		}
		if len(matches) == 1 {
			return &matches[0], nil
		}
	}
	return nil, fmt.Errorf("not found in ROM inventory")
}

func (e *Env) updateLocalIndex() (int, error) {
	games, err := e.localIndex()
	return len(games), err
}

func printLocalSearch(hits []localGame) {
	if len(hits) == 0 {
		fmt.Println("no matches in ROM inventory")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSYSTEM\tPATH")
	for _, h := range hits {
		id := h.ID
		if id == "" && h.Path != "" {
			id = filepath.Base(h.Path)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", id, h.Name, h.System, h.Path)
	}
	w.Flush()
}

func printLocalInfo(h *localGame) {
	if h.Name != "" {
		fmt.Printf("name: %s\n", h.Name)
	}
	if h.System != "" {
		fmt.Printf("system: %s\n", h.System)
	}
	if h.ID != "" {
		fmt.Printf("id: %s\n", h.ID)
	}
	if h.Path != "" {
		fmt.Printf("path: %s\n", h.Path)
	}
	if h.Releasedate != "" {
		fmt.Printf("released: %s\n", h.Releasedate)
	}
	if h.Developer != "" {
		fmt.Printf("developer: %s\n", h.Developer)
	}
	if h.Publisher != "" {
		fmt.Printf("publisher: %s\n", h.Publisher)
	}
	if h.Genre != "" {
		fmt.Printf("genre: %s\n", h.Genre)
	}
	if h.Players != "" {
		fmt.Printf("players: %s\n", h.Players)
	}
	if h.Rating != "" {
		fmt.Printf("rating: %s\n", h.Rating)
	}
	if h.Image != "" {
		fmt.Printf("image: %s\n", h.Image)
	}
	if h.Desc != "" {
		fmt.Printf("\n%s\n", h.Desc)
	}
}
