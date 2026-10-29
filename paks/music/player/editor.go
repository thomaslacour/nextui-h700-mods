package main

// On-screen keyboard (same look as the rest) and the account settings it
// edits on the console: server URL, user, password. The password is turned
// into a Subsonic token at once (md5(password + salt)); only the token and
// the salt are written, as mac/music.sh login does.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

var keyRows = []string{"1234567890", "qwertyuiop", "asdfghjkl-", "zxcvbnm._/", ":@?&=~#!+%"}

type editor struct {
	title  string
	value  string
	masked bool
	shift  bool
	x, y   int
	save   func(a *app, v string)
}

func (e *editor) crumb(a *app) string { return e.title }

func (e *editor) keyAt(x, y int) string {
	k := string(keyRows[y][x])
	if e.shift {
		k = strings.ToUpper(k)
	}
	return k
}

func (e *editor) draw(a *app) {
	c, col := a.cv, a.col
	c.pill(20, 44, 600, 44, 22, col.chip)
	shown := e.value
	if e.masked {
		shown = strings.Repeat("•", len([]rune(e.value)))
		if _, ok := face21.glyphs['•']; !ok {
			shown = strings.Repeat("*", len([]rune(e.value)))
		}
	}
	// Keep the end of a long value in view (the caret is there).
	for textWidth(&face21, shown) > 540 {
		r := []rune(shown)
		shown = string(r[1:])
	}
	x := c.textMid(38, 44, 44, &face21, shown, col.fg)
	c.rect(x+3, 54, 3, 24, col.fg)
	const kw, kh, gap, top = 54, 44, 6, 104
	left := (640 - (10*kw + 9*gap)) / 2
	for y, row := range keyRows {
		for i := range row {
			kx, ky := left+i*(kw+gap), top+y*(kh+gap)
			bg, fg := col.card, col.fg
			if i == e.x && y == e.y {
				bg, fg = col.pill, col.pillFg
			}
			c.pill(kx, ky, kw, kh, 12, bg)
			k := e.keyAt(i, y)
			c.textMid(kx+(kw-textWidth(&face21, k))/2, ky, kh, &face21, k, fg)
		}
	}
	shift := "ABC"
	if e.shift {
		shift = "abc"
	}
	a.drawHints([]hint{{"X", "DELETE"}, {"Y", shift}}, []hint{{"B", "CANCEL"}, {"START", "SAVE"}})
}

func (e *editor) key(a *app, k key) {
	switch k {
	case kUp:
		e.y = (e.y + len(keyRows) - 1) % len(keyRows)
	case kDown:
		e.y = (e.y + 1) % len(keyRows)
	case kLeft:
		e.x = (e.x + 9) % 10
	case kRight:
		e.x = (e.x + 1) % 10
	case kA:
		if len(e.value) < 200 {
			e.value += e.keyAt(e.x, e.y)
		}
	case kX:
		if r := []rune(e.value); len(r) > 0 {
			e.value = string(r[:len(r)-1])
		}
	case kY:
		e.shift = !e.shift
	case kStart:
		a.pop()
		e.save(a, e.value)
	case kB:
		a.pop()
	}
	a.dirty = true
}

// ---------------------------------------------------------------- account

func (a *app) configPath() string { return filepath.Join(a.cfg.dir, "config.txt") }

// saveAccount rewrites config.txt with the given changes, reloads the
// client, restarts the relay (it holds the old token) and tests the server.
func (a *app) saveAccount(changes map[string]string) {
	kv, _ := readKV(a.configPath())
	if kv == nil {
		kv = map[string]string{}
	}
	for k, v := range changes {
		if v == "" {
			delete(kv, k)
		} else {
			kv[k] = v
		}
	}
	os.MkdirAll(a.cfg.dir, 0o755)
	if err := writeKV(a.configPath(), kv, "# Music player sign-in (token = md5(password + salt); the password is not stored).\n"); err != nil {
		a.fail("Cannot save", err)
		return
	}
	cfg, err := loadConfig()
	if err != nil {
		a.cfgOK = false
		a.toast("Saved. Still missing: server, user or password")
		return
	}
	c, err := newClient(cfg)
	if err != nil {
		a.fail("Server", err)
		return
	}
	a.cfg, a.api, a.cfgOK = cfg, c, true
	a.clearLibrary()
	restartRelay()
	testServer(a)
}

// restartRelay replaces the running `music --serve` (pid file of launch.sh).
func restartRelay() {
	pidFile := filepath.Join(tmpDir(), "music-relay.pid")
	if b, err := os.ReadFile(pidFile); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	logs := os.Getenv("LOGS_PATH")
	if logs == "" {
		logs = filepath.Join(filepath.Dir(dataDir()), "logs")
	}
	out, _ := os.OpenFile(filepath.Join(logs, "music-relay.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	cmd := exec.Command(exe, "--serve")
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		log.Printf("relay: %v", err)
		return
	}
	os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o644)
	go cmd.Wait()
}

func accountRows(a *app) []row {
	kv, _ := readKV(a.configPath())
	if kv == nil {
		kv = map[string]string{}
	}
	pw := "Not set"
	if kv["token"] != "" || kv["password"] != "" {
		pw = "Set"
	}
	return []row{
		{label: "Server", value: hostOf(kv["url"]), open: func(a *app) {
			a.push(&editor{title: "Settings · Server URL", value: kv["url"], save: func(a *app, v string) {
				v = strings.TrimRight(strings.TrimSpace(v), "/")
				if v != "" && !strings.HasPrefix(v, "http") {
					v = "https://" + v
				}
				if v != "" && !strings.HasSuffix(v, "/rest") {
					v += "/rest"
				}
				a.saveAccount(map[string]string{"url": v})
			}})
		}},
		{label: "User", value: kv["user"], open: func(a *app) {
			a.push(&editor{title: "Settings · User", value: kv["user"], save: func(a *app, v string) {
				a.saveAccount(map[string]string{"user": strings.TrimSpace(v)})
			}})
		}},
		{label: "Password", value: pw, open: func(a *app) {
			a.push(&editor{title: "Settings · Password", masked: true, save: func(a *app, v string) {
				if v == "" {
					return
				}
				salt := randomSalt()
				a.saveAccount(map[string]string{"token": makeToken(v, salt), "salt": salt, "password": ""})
			}})
		}},
	}
}
