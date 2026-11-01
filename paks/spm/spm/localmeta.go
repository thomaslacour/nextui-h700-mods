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
	seen := map[string]bool{}
	var all []localGame
	for _, p := range e.gamelistPaths() {
		games, err := readGamelist(p, e)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			continue
		}
		for _, g := range games {
			key := strings.ToLower(g.Path)
			if key == "" {
				key = strings.ToLower(g.Name + "|" + g.System)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, g)
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
		base := ""
		if g.Path != "" {
			base = strings.ToLower(filepath.Base(g.Path))
		}
		if strings.Contains(name, term) || strings.Contains(base, term) || strings.Contains(genre, term) || strings.Contains(dev, term) || strings.Contains(pub, term) {
			out = append(out, g)
		}
	}
	return out, nil
}

func (e *Env) localInfo(id string) (*localGame, error) {
	id = strings.TrimSpace(id)
	all, err := e.localIndex()
	if err != nil {
		return nil, err
	}
	low := strings.ToLower(id)
	for _, g := range all {
		if g.ID != "" && strings.ToLower(g.ID) == low {
			cp := g
			return &cp, nil
		}
		if g.Path != "" && strings.EqualFold(g.Path, id) {
			cp := g
			return &cp, nil
		}
		if g.Path != "" && strings.EqualFold(filepath.Base(g.Path), id) {
			cp := g
			return &cp, nil
		}
	}
	for _, g := range all {
		if strings.Contains(strings.ToLower(g.Name), low) {
			cp := g
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("not found in local gamelist.xml")
}

func (e *Env) updateLocalIndex() {
	_, _ = e.localIndex()
}

func printLocalSearch(hits []localGame) {
	if len(hits) == 0 {
		fmt.Println("no matches in local gamelist.xml")
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

type localCache struct {
	dir string
}

func newLocalCache(dir string) *localCache { return &localCache{dir: dir} }

type localCacheStatus struct {
	Entries int
	Bytes   int64
}

func (c *localCache) status() localCacheStatus {
	st, err := os.Stat(c.dir)
	if err != nil || !st.IsDir() {
		return localCacheStatus{}
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return localCacheStatus{}
	}
	var count int
	var total int64
	for _, e := range entries {
		if e.Type().IsRegular() {
			count++
			if info, err := e.Info(); err == nil {
				total += info.Size()
			}
		}
	}
	return localCacheStatus{Entries: count, Bytes: total}
}

func (c *localCache) clean(all bool) int {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0
	}
	var removed int
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		if all || strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".cache") {
			if os.Remove(filepath.Join(c.dir, name)) == nil {
				removed++
			}
		}
	}
	return removed
}
