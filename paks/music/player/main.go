// music — Subsonic / Navidrome player for NextUI on the RG35XX SP (H700).
//
// Linux ARM64, Go stdlib only. Draws on /dev/fb0, reads the buttons in
// evdev, plays through mpd (Entware mpd-full) via a local relay.
//
//	music                     the player (Tools > Music)
//	music --serve             local relay for mpd (started by launch.sh)
//	music --check             sign-in and server test, plain text
//	music --ctl pause|stop    control playback (hooks, launch.sh)
//	music --ctl send CMD...   raw mpd commands (diagnostics)
//	music --keys              print button presses (diagnostics)
//	music --preview <dir>     render sample screens to PNG, off-device
package main

import (
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strings"
	"time"
)

const usage = `music — Subsonic player (RG35XX SP)
  (no option)        open the player
  --serve            local relay for mpd
  --check            test the sign-in and the server
  --ctl CMD          pause | stop | status | send 'mpd command'...
  --keys             print button presses for 15 s
  --preview DIR      render sample screens to PNG
`

func main() {
	log.SetFlags(log.LstdFlags)
	args := os.Args[1:]
	cfg, cfgErr := loadConfig()
	if len(args) == 0 {
		fb, err := openFramebuffer()
		if err != nil {
			log.Fatal(err)
		}
		defer fb.close()
		a := newApp(cfg, cfgErr, fb)
		if err := a.run(); err != nil {
			log.Fatal(err)
		}
		return
	}
	switch args[0] {
	case "--serve":
		if cfgErr != nil {
			log.Fatalf("relay: %v", cfgErr)
		}
		if err := serve(cfg); err != nil {
			log.Fatalf("relay: %v", err)
		}
	case "--check":
		os.Exit(check(cfg, cfgErr))
	case "--ctl":
		if len(args) < 2 {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		os.Exit(ctl(cfg, args[1], args[2:]...))
	case "--get": // diagnostics: --get method [k=v...] -> status, type, start of body (no token shown)
		if cfgErr != nil || len(args) < 2 {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		c, err := newClient(cfg)
		if err != nil {
			log.Fatal(err)
		}
		q := url.Values{}
		for _, kv := range args[2:] {
			k, v, _ := strings.Cut(kv, "=")
			q.Set(k, v)
		}
		resp, err := c.http.Get(c.endpoint(args[1], q))
		if err != nil {
			log.Fatal(cleanErr(err))
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 600))
		resp.Body.Close()
		fmt.Printf("HTTP %d %s %s\n", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Length"))
		if strings.Contains(resp.Header.Get("Content-Type"), "json") || strings.Contains(resp.Header.Get("Content-Type"), "text") {
			fmt.Println(string(b))
		}
	case "--keys":
		keys := make(chan keyEvent, 64)
		if err := listenInput(keys); err != nil {
			log.Fatal(err)
		}
		fmt.Println("inputs:", inputPaths())
		end := time.After(15 * time.Second)
		for {
			select {
			case ev := <-keys:
				fmt.Printf("%s pressed=%v\n", ev.k, ev.pressed)
			case <-end:
				return
			}
		}
	case "--preview":
		if len(args) < 2 {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		if err := renderPreviews(args[1]); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func check(cfg *config, cfgErr error) int {
	if cfgErr != nil {
		fmt.Println("config:", cfgErr)
		return 1
	}
	c, err := newClient(cfg)
	if err != nil {
		fmt.Println("client:", err)
		return 1
	}
	t := time.Now()
	r, err := c.ping()
	if err != nil {
		fmt.Println("server:", err)
		return 1
	}
	fmt.Printf("server: OK %s %s (API %s) in %d ms\n", r.Type, r.ServerVersion, r.Version, time.Since(t).Milliseconds())
	if st, err := newMPD(cfg.mpdSocket).status(); err == nil {
		fmt.Printf("mpd: %s, queue %s\n", st["state"], st["playlistlength"])
	} else {
		fmt.Println("mpd:", err)
	}
	return 0
}

func ctl(cfg *config, cmd string, rest ...string) int {
	m := newMPD(cfg.mpdSocket)
	var err error
	switch cmd {
	case "pause":
		var st map[string]string
		if st, err = m.status(); err == nil && st["state"] == "play" {
			err = m.simple("pause 1")
		}
	case "stop":
		err = m.simple("stop")
	case "status":
		var st map[string]string
		if st, err = m.status(); err == nil {
			fmt.Println(st["state"])
		}
	case "send": // diagnostics: raw mpd commands, one per argument
		var ps []pair
		if ps, err = m.run(rest...); err == nil {
			for _, p := range ps {
				fmt.Printf("%s: %s\n", p.k, p.v)
			}
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
