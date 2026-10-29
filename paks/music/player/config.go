package main

// config.txt (written once by mac/music.sh login, never by the player):
//
//	url   = https://music.example.com/rest
//	user  = me
//	token = <md5(password + salt)>
//	salt  = <random>
//	ca    = ca.pem            # extra root CA, relative to the config dir
//	ip    = 192.168.1.10      # optional: dial this IP, keep the TLS name
//
// settings.txt holds what the player itself changes (quality, after exit).

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type config struct {
	dir                         string
	URL, User, Token, Salt      string
	Password                    string // accepted, but login writes a token
	CA, IP                      string
	settings                    map[string]string
	settingsPath, queuePath     string
	mpdSocket, proxyAddr, state string
}

func readKV(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	kv := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		kv[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return kv, sc.Err()
}

func writeKV(path string, kv map[string]string, header string) error {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(header)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", k, kv[k])
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func dataDir() string {
	if d := os.Getenv("MUSIC_DIR"); d != "" {
		return d
	}
	u := os.Getenv("USERDATA_PATH")
	if u == "" {
		u = "/mnt/SDCARD/.userdata/h700"
	}
	return filepath.Join(u, "music")
}

func tmpDir() string {
	if d := os.Getenv("TMPDIR"); d != "" {
		return d
	}
	return "/tmp"
}

var errNoConfig = errors.New("not signed in")

func loadConfig() (*config, error) {
	dir := dataDir()
	c := &config{dir: dir,
		settingsPath: filepath.Join(dir, "settings.txt"),
		queuePath:    filepath.Join(dir, "queue.json"),
		mpdSocket:    filepath.Join(tmpDir(), "music-mpd.sock"),
		proxyAddr:    "127.0.0.1:46301",
		state:        filepath.Join(dir, "mpd.state"),
	}
	if v := os.Getenv("MUSIC_MPD_SOCKET"); v != "" {
		c.mpdSocket = v
	}
	if v := os.Getenv("MUSIC_PROXY_ADDR"); v != "" {
		c.proxyAddr = v
	}
	c.settings, _ = readKV(c.settingsPath)
	if c.settings == nil {
		c.settings = map[string]string{}
	}
	kv, err := readKV(filepath.Join(dir, "config.txt"))
	if err != nil {
		if os.IsNotExist(err) {
			return c, errNoConfig
		}
		return c, err
	}
	c.URL = strings.TrimRight(kv["url"], "/")
	c.User, c.Token, c.Salt, c.Password = kv["user"], kv["token"], kv["salt"], kv["password"]
	c.CA, c.IP = kv["ca"], kv["ip"]
	if c.CA != "" && !filepath.IsAbs(c.CA) {
		c.CA = filepath.Join(dir, c.CA)
	}
	if c.URL == "" || c.User == "" || (c.Token == "" || c.Salt == "") && c.Password == "" {
		return c, errNoConfig
	}
	return c, nil
}

func (c *config) setting(k, def string) string {
	if v, ok := c.settings[k]; ok && v != "" {
		return v
	}
	return def
}

func (c *config) setSetting(k, v string) error {
	c.settings[k] = v
	return writeKV(c.settingsPath, c.settings, "# Music player settings (changed from the player).\n")
}
