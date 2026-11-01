package main

// cache.go — a small on-disk cache for ScreenScraper metadata, so repeated
// searches and `spm info` work offline and stay within the API rate limit.
// Each entry stores the non-secret query and the parsed "response"; credentials
// are never written to disk. `spm update` refreshes expired entries;
// `spm cache clean` removes them. Published cache size/count are bounded.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type cache struct {
	dir        string
	ttl        time.Duration
	maxBytes   int64
	maxEntries int
}

var cacheFileName = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)

const maxCacheEntry = (4 << 20) + 4096

func cacheDir(e *Env) string {
	if d := os.Getenv("SPM_CACHE_DIR"); d != "" {
		return d
	}
	return filepath.Join(e.SD, ".userdata", "h700", "spm", "cache")
}

func newCache(dir string) *cache {
	ttl := 24 * time.Hour
	if s := os.Getenv("SPM_CACHE_TTL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			ttl = d
		}
	}
	return &cache{dir: dir, ttl: ttl, maxBytes: 16 << 20, maxEntries: 64}
}

type cacheEntry struct {
	Query    string          `json:"query"`
	Response json.RawMessage `json:"response"`
}

func (c *cache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+".json")
}

// read returns a fresh cached response for key, or ok=false when absent/stale.
func (c *cache) read(key string) (json.RawMessage, bool) {
	p := c.path(key)
	p, err := guardedPath(c.dir, filepath.Base(p))
	if err != nil {
		return nil, false
	}
	info, err := os.Stat(p)
	if err != nil || info.Size() > maxCacheEntry || time.Since(info.ModTime()) >= c.ttl {
		return nil, false
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCacheEntry+1))
	if err != nil || len(data) > maxCacheEntry {
		return nil, false
	}
	var e cacheEntry
	if json.Unmarshal(data, &e) != nil || e.Query != key || !json.Valid(e.Response) || string(e.Response) == "null" {
		return nil, false
	}
	return e.Response, true
}

func (c *cache) write(key string, response json.RawMessage) error {
	data, err := json.Marshal(cacheEntry{Query: key, Response: response})
	if err != nil {
		return err
	}
	if len(data) > maxCacheEntry || int64(len(data)) > c.maxBytes {
		return fmt.Errorf("metadata exceeds cache budget")
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	lock, err := guardedPath(c.dir, ".spm-cache.lock")
	if err != nil {
		return err
	}
	if err := os.Mkdir(lock, 0o700); err != nil {
		return fmt.Errorf("cache locked: %w", err)
	}
	defer os.Remove(lock)
	files, err := c.files()
	if err != nil {
		return err
	}
	total, count := int64(len(data)), 1
	var candidates []os.FileInfo
	for _, file := range files {
		if file.Name() == filepath.Base(c.path(key)) {
			continue
		}
		info, err := file.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		count++
		candidates = append(candidates, info)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ModTime().Before(candidates[j].ModTime()) })
	for _, info := range candidates {
		if total <= c.maxBytes && count <= c.maxEntries {
			break
		}
		if err := os.Remove(filepath.Join(c.dir, info.Name())); err != nil {
			return err
		}
		total -= info.Size()
		count--
	}
	free, err := diskFree(c.dir)
	if err != nil || free < uint64(len(data))+(1<<20) {
		return fmt.Errorf("cache storage unavailable or full")
	}
	return atomicMetadata(c.dir, filepath.Base(c.path(key)), data, true)
}

// Own generated files only. Never count/delete another JSON file or follow a
// symlink because SPM_CACHE_DIR happens to contain one.
func (c *cache) files() ([]os.DirEntry, error) {
	entries, err := os.ReadDir(c.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []os.DirEntry
	for _, entry := range entries {
		if cacheFileName.MatchString(entry.Name()) && entry.Type().IsRegular() {
			files = append(files, entry)
		}
	}
	return files, nil
}

// queries returns every cached non-secret query, for `spm update`.
func (c *cache) queries() []string {
	entries, err := c.files()
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.Size() > maxCacheEntry {
			continue
		}
		data, err := os.ReadFile(filepath.Join(c.dir, e.Name()))
		if err != nil {
			continue
		}
		var ent cacheEntry
		if json.Unmarshal(data, &ent) == nil && ent.Query != "" && filepath.Base(c.path(ent.Query)) == e.Name() {
			out = append(out, ent.Query)
		}
	}
	return out
}

type cacheStatus struct {
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	Stale   int   `json:"stale"`
}

// status counts entries, bytes and how many are past their TTL.
func (c *cache) status() cacheStatus {
	var s cacheStatus
	entries, err := c.files()
	if err != nil {
		return s
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		s.Entries++
		s.Bytes += info.Size()
		if time.Since(info.ModTime()) >= c.ttl {
			s.Stale++
		}
	}
	return s
}

// clean removes expired entries (or every entry with all).
func (c *cache) clean(all bool) int {
	entries, err := c.files()
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && !all && time.Since(info.ModTime()) < c.ttl {
			continue
		}
		if os.Remove(filepath.Join(c.dir, e.Name())) == nil {
			removed++
		}
	}
	return removed
}

// splitQuery turns "jeuRecherche.php?game=mario" back into endpoint + params.
func splitQuery(q string) (string, url.Values, bool) {
	i := strings.IndexByte(q, '?')
	if i < 0 {
		return "", nil, false
	}
	params, err := url.ParseQuery(q[i+1:])
	if err != nil || len(params) == 0 {
		return "", nil, false
	}
	return q[:i], params, true
}

// humanBytes renders a byte count compactly.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
