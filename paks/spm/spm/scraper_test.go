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
)

func TestScraperSearchAndInfo(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/jeuRecherche.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("devid") != "dev" || r.URL.Query().Get("softname") != softname || r.URL.Query().Get("game") == "" {
			t.Errorf("bad query: %s", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"header":{"success":"true"},"response":{"jeux":[
			{"id":1028,"noms":[{"region":"ss","text":"Super Mario Land"},{"region":"us","text":"Super Mario Land"}],
			 "systeme":{"id":4,"text":"Game Boy"},"dates":[{"region":"us","text":"1989-04-21"}],
			 "synopsis":[{"langue":"en","text":"A platform game."}]}]}}`)
	})
	mux.HandleFunc("/api2/jeuInfos.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("gameid") != "1028" {
			t.Errorf("bad gameid: %s", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"header":{"success":"true"},"response":{"jeu":{
			"id":1028,"noms":[{"region":"ss","text":"Super Mario Land"}],
			"systeme":{"text":"Game Boy"},"dates":[{"text":"1989-04-21"}],
			"editeur":{"text":"Nintendo"},"developpeur":{"text":"Nintendo R&D1"},
			"joueurs":{"text":"1"},"note":{"text":"8/20"},
			"genres":[{"noms":[{"langue":"en","text":"Platform"}]}],
			"synopsis":[{"langue":"fr","text":"Jeu de plateforme."},{"langue":"en","text":"A platform game."}]}}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	s := newScraper(server.URL+"/api2/", Credentials{DevID: "dev", DevPassword: "dp", SSID: "ss", SSPassword: "sp"})

	hits, err := s.search("mario")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Name != "Super Mario Land" || hits[0].System != "Game Boy" || hits[0].Year != "1989" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].ID != "1028" {
		t.Fatalf("id = %q", hits[0].ID)
	}

	g, err := s.info("1028")
	if err != nil {
		t.Fatal(err)
	}
	if g.Name != "Super Mario Land" || g.Publisher != "Nintendo" || g.Developer != "Nintendo R&D1" ||
		g.Players != "1" || g.Year != "1989" || g.Rating != "8/20" {
		t.Fatalf("info = %+v", g)
	}
	if g.Description == "" || len(g.Genres) != 1 || g.Genres[0] != "Platform" {
		t.Fatalf("description/genres wrong: %+v", g)
	}
}

func TestScraperErrors(t *testing.T) {
	httpError := func(status int, body string) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/api2/jeuRecherche.php", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		})
		return httptest.NewServer(mux)
	}
	creds := Credentials{DevID: "d", DevPassword: "p", SSID: "i", SSPassword: "w"}

	srv := httpError(403, `{"header":{"error":"Forbidden"}}`)
	if _, err := newScraper(srv.URL+"/api2/", creds).search("x"); err == nil {
		t.Fatal("HTTP 403 must surface")
	}
	srv.Close()

	srv = httpError(200, `{"header":{"error":"Invalid developer id"}}`)
	if _, err := newScraper(srv.URL+"/api2/", creds).search("x"); err == nil {
		t.Fatal("header.error must surface")
	}
	srv.Close()
}

func TestScreenScraperCredsFromEnvAndFile(t *testing.T) {
	e := testEnv(t)
	for _, name := range []string{"SS_DEVID", "SS_DEVPASSWORD", "SS_ID", "SS_PASSWORD"} {
		t.Setenv(name, "")
	}
	if _, err := e.screenScraperCreds(); err == nil {
		t.Fatal("missing credentials must error")
	}

	t.Setenv("SS_DEVID", "d")
	t.Setenv("SS_DEVPASSWORD", "p")
	t.Setenv("SS_ID", "i")
	t.Setenv("SS_PASSWORD", "w")
	c, err := e.screenScraperCreds()
	if err != nil || c.DevID != "d" || c.SSPassword != "w" {
		t.Fatalf("env credentials: %+v %v", c, err)
	}

	t.Setenv("SS_DEVID", "")
	t.Setenv("SS_DEVPASSWORD", "")
	t.Setenv("SS_ID", "")
	t.Setenv("SS_PASSWORD", "")
	path := filepath.Join(e.SD, ".userdata", "h700", "spm", "screenscraper.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"devid":"f","devpassword":"g","ssid":"h","sspassword":"k"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err = e.screenScraperCreds()
	if err != nil || c.DevID != "f" || c.SSID != "h" {
		t.Fatalf("file credentials: %+v %v", c, err)
	}
	t.Setenv("SS_ID", "environment-user")
	c, err = e.screenScraperCreds()
	if err != nil || c.SSID != "environment-user" || c.DevID != "f" {
		t.Fatal("environment must override individual file credentials")
	}
}

func TestScraperDoesNotLeakCredentialURLsOnNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	creds := Credentials{DevID: "private-developer", DevPassword: "password-developer", SSID: "private-user", SSPassword: "password-user"}
	s := newScraper(srv.URL+"/api2/", creds)
	_, err := s.search("mario")
	if err == nil {
		t.Fatal("expected a network error")
	}
	for _, secret := range []string{creds.DevID, creds.DevPassword, creds.SSID, creds.SSPassword, "devid=", "sspassword="} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("network error leaked credential URL")
		}
	}
}

func TestScraperRefusesRedirectWithoutSendingCredentials(t *testing.T) {
	var forwarded int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&forwarded, 1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/?"+r.URL.RawQuery, http.StatusFound)
	}))
	defer source.Close()
	s := newScraper(source.URL+"/", Credentials{DevID: "d", DevPassword: "p", SSID: "u", SSPassword: "s"})
	if _, err := s.search("mario"); err == nil {
		t.Fatal("redirect accepted")
	}
	if atomic.LoadInt32(&forwarded) != 0 {
		t.Fatal("credentials forwarded to redirect target")
	}
}
