package main

// `music --serve`: a small local relay for mpd. mpd plays
// http://127.0.0.1:46301/stream/<id>.<suffix>; the relay adds the token and
// talks TLS to the server with the home CA. So mpd's queue and state file
// never hold a token, and mpd needs no CA setup. Range requests (seeking)
// pass through untouched.
//
// It also keeps the console awake while music plays after the pak exits
// (/tmp/stay_awake, the file NextUI's launcher checks before auto-sleep), and
// keeps /tmp/music-playing while mpd plays: the launcher's status row shows
// a note then (nextui/patches/20-status-dial.patch, SS_musicShown).

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// streamParams: quality setting -> stream parameters (read per request, so a
// change in the player applies to the next track).
// mpd (Entware mpd-full, FFmpeg) decodes every common format itself, so
// songs stream as stored; a server may still ignore these (smolsonic does).
func streamParams(settings map[string]string) url.Values {
	q := url.Values{}
	switch settings["quality"] {
	case "320", "192", "128":
		q.Set("maxBitRate", settings["quality"])
		q.Set("format", "mp3")
	}
	return q
}

func streamID(p string) (string, bool) {
	if !strings.HasPrefix(p, "/stream/") {
		return "", false
	}
	id := strings.TrimPrefix(p, "/stream/")
	if ext := path.Ext(id); ext != "" && len(ext) <= 6 {
		id = strings.TrimSuffix(id, ext)
	}
	id, err := url.PathUnescape(id)
	return id, err == nil && id != ""
}

func newRelay(cfg *config, c *client) http.Handler {
	rp := &httputil.ReverseProxy{
		Transport: c.transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			id, _ := streamID(pr.In.URL.Path)
			settings, _ := readKV(cfg.settingsPath)
			q := streamParams(settings)
			q.Set("id", id)
			target, _ := url.Parse(c.endpoint("stream", q))
			pr.Out.URL = target
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Cookie")
		},
		// A Subsonic error instead of audio (smolsonic: "could not read
		// file"): 502 for mpd, and the reason for the player to show.
		ModifyResponse: func(resp *http.Response) error {
			if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
				return nil
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			msg := "the server sent no audio"
			var env struct {
				R response `json:"subsonic-response"`
			}
			if json.Unmarshal(body, &env) == nil && env.R.Error != nil && env.R.Error.Message != "" {
				msg = env.R.Error.Message
			}
			id, _ := streamID(resp.Request.URL.Path)
			if id == "" {
				id = resp.Request.URL.Query().Get("id")
			}
			log.Printf("stream %s: server error: %s", id, msg)
			os.WriteFile(streamErrorFile(), []byte(id+"\t"+msg+"\n"), 0o644)
			resp.StatusCode, resp.Status = http.StatusBadGateway, "502 Bad Gateway"
			resp.Body = io.NopCloser(strings.NewReader(""))
			resp.ContentLength = 0
			resp.Header.Del("Content-Length")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("relay %s: %v", r.URL.Path, cleanErr(err))
			http.Error(w, "upstream error", http.StatusBadGateway)
		},
		ErrorLog: log.New(os.Stderr, "relay: ", 0),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/stream/", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := streamID(r.URL.Path); !ok || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		log.Printf("stream %s range=%q", r.URL.Path, r.Header.Get("Range"))
		rp.ServeHTTP(w, r)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	return mux
}

func streamErrorFile() string { return filepath.Join(tmpDir(), "music-stream-error") }

func relayURL(cfg *config, s song) string {
	// The real suffix: mpd picks its decoder from it (and from the MIME type).
	ext := strings.ToLower(s.Suffix)
	if ext == "" || len(ext) > 5 || strings.ContainsAny(ext, "/?#") {
		ext = "bin"
	}
	return "http://" + cfg.proxyAddr + "/stream/" + url.PathEscape(string(s.ID)) + "." + ext
}

// keepAwake mirrors mpd's state into /tmp/stay_awake.
func keepAwake(cfg *config, stop <-chan struct{}) {
	const flag, playing = "/tmp/stay_awake", "/tmp/music-playing"
	ours := false
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		st, err := newMPD(cfg.mpdSocket).status()
		isPlaying := err == nil && st["state"] == "play"
		if isPlaying {
			if _, err := os.Stat(playing); err != nil {
				os.WriteFile(playing, nil, 0o644)
			}
		} else {
			os.Remove(playing)
		}
		if isPlaying && !ours {
			if _, err := os.Stat(flag); os.IsNotExist(err) {
				if os.WriteFile(flag, []byte("1\n"), 0o644) == nil {
					ours = true
				}
			}
		} else if !isPlaying && ours {
			os.Remove(flag)
			ours = false
		}
		select {
		case <-stop:
			os.Remove(playing)
			if ours {
				os.Remove(flag)
			}
			return
		case <-t.C:
		}
	}
}

func serve(cfg *config) error {
	c, err := newClient(cfg)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.proxyAddr)
	if err != nil {
		var oe *net.OpError
		if errors.As(err, &oe) && strings.Contains(err.Error(), "address already in use") {
			return errors.New("relay already running")
		}
		return err
	}
	stop := make(chan struct{})
	defer close(stop)
	go keepAwake(cfg, stop)
	log.Printf("relay on %s for %s", cfg.proxyAddr, c.base.Host)
	srv := &http.Server{Handler: newRelay(cfg, c), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}
