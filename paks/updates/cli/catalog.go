package main

// Catalogues: the Pak Store's storefront.json (LoveRetro/nextui-pak-store,
// gh-pages branch), plus our own ones listed in <home>/sources (one URL per
// line, same format). Each is cached on the SD card; an entry of our own
// catalogues wins over a Pak Store entry with the same id.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultStorefront = "https://raw.githubusercontent.com/LoveRetro/nextui-pak-store/refs/heads/gh-pages/storefront.json"

type script struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
}

type catPak struct {
	ID              string            `json:"id"`
	StorefrontName  string            `json:"storefront_name"`
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Type            string            `json:"type"`
	Description     string            `json:"description"`
	Author          string            `json:"author"`
	RepoURL         string            `json:"repo_url"`
	PrevRepoURLs    []string          `json:"previous_repo_urls"`
	ReleaseFilename string            `json:"release_filename"`
	Changelog       map[string]string `json:"changelog"`
	PreviousNames   []string          `json:"previous_names"`
	Scripts         struct {
		PostInstall   script `json:"post_install"`
		PostUpdate    script `json:"post_update"`
		PostUninstall script `json:"post_uninstall"`
	} `json:"scripts"`
	UpdateIgnore []string `json:"update_ignore"`
	Platforms    []string `json:"platforms"`
	Categories   []string `json:"categories"`
	LargePak     bool     `json:"large_pak"`
	Disabled     bool     `json:"disabled"`
	Experimental bool     `json:"experimental"`
	// Our catalogues: the archive URL and its checksum, so no GitHub API is
	// needed (any static file server works).
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Own    bool   `json:"-"` // listed by one of our catalogues
}

type storefront struct {
	Name         string   `json:"name"`
	Paks         []catPak `json:"paks"`
	Experimental []catPak `json:"experimental_paks"`
}

type catalog struct {
	paks    []catPak
	fetched time.Time
}

func (p catPak) title() string {
	if p.StorefrontName != "" {
		return p.StorefrontName
	}
	return p.Name
}

func (p catPak) isPakz() bool { return strings.HasSuffix(p.ReleaseFilename, ".pakz") }

func (p catPak) supports(platform string) bool {
	if len(p.Platforms) == 0 {
		return true
	}
	for _, x := range p.Platforms {
		if strings.EqualFold(x, platform) {
			return true
		}
	}
	return false
}

func parseCatalog(data []byte) ([]catPak, error) {
	var sf storefront
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("catalogue is not valid JSON: %v", err)
	}
	if len(sf.Paks) == 0 {
		return nil, fmt.Errorf("catalogue lists no paks")
	}
	for i := range sf.Experimental {
		sf.Experimental[i].Experimental = true
	}
	return append(sf.Paks, sf.Experimental...), nil
}

// sources lists the catalogue URLs: the Pak Store, then <home>/sources.
func (e *env) sources() []string {
	out := []string{e.storefront}
	f, err := os.Open(filepath.Join(e.home, "sources"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if u := strings.TrimSpace(sc.Text()); u != "" && !strings.HasPrefix(u, "#") {
			out = append(out, u)
		}
	}
	return out
}

// cacheFile is where a catalogue is kept: storefront.json for the Pak Store,
// catalog-<hash>.json for ours.
func (e *env) cacheFile(src string) string {
	if src == e.storefront {
		return e.catalogFile()
	}
	h := sha256.Sum256([]byte(src))
	return filepath.Join(e.home, "catalog-"+hex.EncodeToString(h[:4])+".json")
}

func (e *env) fetchOne(src string) ([]catPak, error) {
	data, err := e.get(src, 3)
	if err != nil {
		return nil, fmt.Errorf("catalogue unreachable (%s): %v", src, err)
	}
	paks, err := parseCatalog(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", src, err)
	}
	return paks, writeAtomic(e.cacheFile(src), data)
}

func (e *env) cachedOne(src string) ([]catPak, time.Time, error) {
	f := e.cacheFile(src)
	data, err := os.ReadFile(f)
	if err != nil {
		return nil, time.Time{}, err
	}
	paks, err := parseCatalog(data)
	if err != nil {
		return nil, time.Time{}, err
	}
	st, _ := os.Stat(f)
	return paks, st.ModTime(), nil
}

// merge joins the catalogues; ours override Pak Store entries with the same id.
func merge(store []catPak, own [][]catPak) []catPak {
	seen := map[string]bool{}
	var out []catPak
	for _, ps := range own {
		for _, p := range ps {
			p.Own = true
			if p.ID == "" || !seen[p.ID] {
				seen[p.ID] = true
				out = append(out, p)
			}
		}
	}
	for _, p := range store {
		if p.ID == "" || !seen[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

// fetchCatalog downloads every catalogue and replaces the caches atomically.
// The Pak Store must answer; one of ours failing falls back to its cache.
func (e *env) fetchCatalog() (*catalog, error) {
	srcs := e.sources()
	store, err := e.fetchOne(srcs[0])
	if err != nil {
		return nil, err
	}
	var own [][]catPak
	for _, src := range srcs[1:] {
		ps, err := e.fetchOne(src)
		if err != nil {
			if ps, _, cerr := e.cachedOne(src); cerr == nil {
				e.warn("%v; using the cached copy", err)
				own = append(own, ps)
			} else {
				e.warn("%v", err)
			}
			continue
		}
		own = append(own, ps)
	}
	return &catalog{paks: merge(store, own), fetched: time.Now()}, nil
}

func (e *env) cachedCatalog() (*catalog, error) {
	srcs := e.sources()
	store, t, err := e.cachedOne(srcs[0])
	if err != nil {
		return nil, err
	}
	var own [][]catPak
	for _, src := range srcs[1:] {
		ps, ot, err := e.cachedOne(src)
		if err != nil {
			t = time.Time{} // never fetched: refresh soon
			continue
		}
		if ot.Before(t) {
			t = ot
		}
		own = append(own, ps)
	}
	return &catalog{paks: merge(store, own), fetched: t}, nil
}

// catalog returns the cached catalogue, refreshed when older than maxAge
// (0 = never refresh if a cache exists). A failed refresh falls back to the
// cache with a warning.
func (e *env) catalog(maxAge time.Duration) (*catalog, error) {
	c, err := e.cachedCatalog()
	if err == nil && (maxAge == 0 || time.Since(c.fetched) < maxAge) {
		return c, nil
	}
	fresh, ferr := e.fetchCatalog()
	if ferr == nil {
		return fresh, nil
	}
	if err == nil {
		e.warn("%v; using the catalogue from %s", ferr, c.fetched.Format("2006-01-02 15:04"))
		return c, nil
	}
	return nil, fmt.Errorf("%v (and no cached catalogue: run `pak update`)", ferr)
}

func (c *catalog) byID(id string) (catPak, bool) {
	for _, p := range c.paks {
		if p.ID == id && id != "" {
			return p, true
		}
	}
	return catPak{}, false
}

// find resolves a user-given name: id, then name, then storefront name,
// case-insensitive. Disabled entries are ignored. Several matches are
// narrowed to the platform, then to an installed pak.
func (e *env) find(c *catalog, reg *registry, q string) (catPak, error) {
	var hits []catPak
	for _, match := range []func(catPak) bool{
		func(p catPak) bool { return p.ID == q },
		func(p catPak) bool { return strings.EqualFold(p.Name, q) },
		func(p catPak) bool { return strings.EqualFold(p.StorefrontName, q) },
	} {
		for _, p := range c.paks {
			if !p.Disabled && match(p) {
				hits = append(hits, p)
			}
		}
		if len(hits) > 0 {
			break
		}
	}
	if len(hits) == 0 {
		return catPak{}, fmt.Errorf("%q is not in the catalogue (pak search %s)", q, q)
	}
	narrow := func(keep func(catPak) bool) {
		var out []catPak
		for _, p := range hits {
			if keep(p) {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			hits = out
		}
	}
	if len(hits) > 1 {
		narrow(func(p catPak) bool { return p.supports(e.platform) })
	}
	if len(hits) > 1 {
		narrow(func(p catPak) bool { return reg.byID(p.ID) != nil })
	}
	if len(hits) > 1 {
		var b strings.Builder
		fmt.Fprintf(&b, "%q is ambiguous, use the id:", q)
		for _, p := range hits {
			fmt.Fprintf(&b, "\n  %s  %s (%s)", p.ID, p.title(), p.Author)
		}
		return catPak{}, fmt.Errorf("%s", b.String())
	}
	return hits[0], nil
}

// cmpVersion compares like the Pak Store: leading "v" dropped, dot-separated
// numbers, anything non-numeric counts as 0.
func cmpVersion(a, b string) int {
	pa := strings.Split(strings.TrimPrefix(strings.TrimPrefix(a, "v"), "V"), ".")
	pb := strings.Split(strings.TrimPrefix(strings.TrimPrefix(b, "v"), "V"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// changelogLatest returns the changelog line of a version, if any.
func (p catPak) changelogFor(v string) string {
	if s, ok := p.Changelog[v]; ok {
		return s
	}
	for k, s := range p.Changelog {
		if cmpVersion(k, v) == 0 {
			return s
		}
	}
	return ""
}

func sortPaks(ps []catPak) {
	sort.Slice(ps, func(i, j int) bool { return strings.ToLower(ps[i].title()) < strings.ToLower(ps[j].title()) })
}

func (e *env) catalogFile() string { return filepath.Join(e.home, "storefront.json") }
