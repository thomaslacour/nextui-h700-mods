package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheRoundTripAndStale(t *testing.T) {
	c := newCache(t.TempDir())
	key := "jeuRecherche.php?game=mario"
	if _, ok := c.read(key); ok {
		t.Fatal("empty cache must miss")
	}
	if err := c.write(key, []byte(`{"jeux":[]}`)); err != nil {
		t.Fatal(err)
	}
	raw, ok := c.read(key)
	if !ok || string(raw) != `{"jeux":[]}` {
		t.Fatalf("read = %s %v", raw, ok)
	}
	c.ttl = 0 // everything is now stale
	if _, ok := c.read(key); ok {
		t.Fatal("expired entry must miss")
	}
}

func TestCacheStatusAndClean(t *testing.T) {
	c := newCache(t.TempDir())
	c.write("a?x=1", []byte(`{}`))
	c.write("b?x=2", []byte(`{}`))
	if st := c.status(); st.Entries != 2 {
		t.Fatalf("entries = %d, want 2", st.Entries)
	}
	c.ttl = 0
	if n := c.clean(false); n != 2 {
		t.Fatalf("clean(false) removed %d, want 2", n)
	}
	c.ttl = 24 * time.Hour
	c.write("c?x=3", []byte(`{}`))
	if n := c.clean(false); n != 0 {
		t.Fatalf("clean(false) must keep fresh entries, removed %d", n)
	}
	if n := c.clean(true); n != 1 {
		t.Fatalf("clean(true) removed %d, want 1", n)
	}
}

func TestSplitQuery(t *testing.T) {
	endpoint, params, ok := splitQuery("jeuRecherche.php?game=mario+land&systemeid=4")
	if !ok || endpoint != "jeuRecherche.php" || params.Get("game") != "mario land" || params.Get("systemeid") != "4" {
		t.Fatalf("splitQuery = %q %v %v", endpoint, params, ok)
	}
	if _, _, ok := splitQuery("no-question-mark"); ok {
		t.Fatal("malformed query must fail")
	}
}

func TestScraperCachesSearch(t *testing.T) {
	var calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/jeuRecherche.php", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, `{"header":{"success":"true"},"response":{"jeux":[{"id":1,"noms":[{"region":"ss","text":"Game"}],"systeme":{"text":"GB"}}]}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := newCache(t.TempDir())
	creds := Credentials{DevID: "d", DevPassword: "p", SSID: "i", SSPassword: "w"}
	s := newScraper(server.URL+"/api2/", creds)
	s.cache = c

	if _, err := s.search("game"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.search("game"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("second search must come from cache, got %d HTTP calls", calls)
	}

	s.refresh = true
	if _, err := s.search("game"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("--refresh must refetch, got %d HTTP calls", calls)
	}
}

func TestScraperRefreshAll(t *testing.T) {
	var calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/jeuRecherche.php", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, `{"header":{"success":"true"},"response":{"jeux":[{"id":1,"noms":[{"region":"ss","text":"Game"}],"systeme":{"text":"GB"}}]}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := newCache(t.TempDir())
	s := newScraper(server.URL+"/api2/", Credentials{DevID: "d", DevPassword: "p", SSID: "i", SSPassword: "w"})
	s.cache = c
	s.search("game") // populates the cache
	if st := c.status(); st.Entries != 1 {
		t.Fatalf("cache entries = %d", st.Entries)
	}
	updated, failed := s.refreshAll()
	if updated != 0 || failed != 0 || atomic.LoadInt32(&calls) != 1 {
		t.Fatal("update should not refetch fresh entries")
	}
	c.ttl = 0
	updated, failed = s.refreshAll()
	if updated != 1 || failed != 0 {
		t.Fatalf("refreshAll = %d updated, %d failed", updated, failed)
	}
}

func TestCacheBudgetAndCleanDoNotTouchForeignFilesOrSymlinks(t *testing.T) {
	c := newCache(t.TempDir())
	c.maxEntries, c.maxBytes = 2, 1024
	foreign := filepath.Join(c.dir, "settings.json")
	if err := os.WriteFile(foreign, []byte(`{"keep":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"search?a", "search?b", "search?c"} {
		if err := c.write(key, []byte(`{"games":[]}`)); err != nil {
			t.Fatal(err)
		}
	}
	if st := c.status(); st.Entries != 2 || st.Bytes > 1024 {
		t.Fatalf("cache budget exceeded: %+v", st)
	}
	outside := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(outside, []byte("private data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, c.path("linked")); err != nil {
		t.Fatal(err)
	}
	if err := c.write("linked", []byte(`{}`)); err == nil {
		t.Fatal("cache followed a symlink")
	}
	c.clean(true)
	if contents(t, outside) != "private data" || contents(t, foreign) != `{"keep":true}` {
		t.Fatal("unrelated file changed")
	}
}

func TestFreshCacheWorksWithoutScreenScraperCredentials(t *testing.T) {
	e := testEnv(t)
	for _, name := range []string{"SS_DEVID", "SS_DEVPASSWORD", "SS_ID", "SS_PASSWORD"} {
		t.Setenv(name, "")
	}
	c := newCache(t.TempDir())
	t.Setenv("SPM_CACHE_DIR", c.dir)
	if err := c.write("jeuRecherche.php?game=mario", []byte(`{"jeux":[]}`)); err != nil {
		t.Fatal(err)
	}
	s, err := cachedScraper(e, nil)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := s.search("mario")
	if err != nil || len(hits) != 0 {
		t.Fatalf("offline cache failed: %v", err)
	}
}

func TestCacheNeverStoresCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"response":{"jeux":[]}}`)
	}))
	defer srv.Close()
	creds := Credentials{DevID: "developer-private", DevPassword: "developer-password", SSID: "user-private", SSPassword: "user-password"}
	s := newScraper(srv.URL+"/", creds)
	s.cache = newCache(t.TempDir())
	if _, err := s.search("mario"); err != nil {
		t.Fatal(err)
	}
	for _, key := range s.cache.queries() {
		data := contents(t, s.cache.path(key))
		for _, secret := range []string{creds.DevID, creds.DevPassword, creds.SSID, creds.SSPassword, "devid=", "sspassword="} {
			if strings.Contains(data, secret) {
				t.Fatal("credentials stored in cache")
			}
		}
	}
}
