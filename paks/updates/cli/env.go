package main

// Paths, HTTP, small file helpers, held paks and hidden Tools.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type env struct {
	tool                   string // "pak" or "port": names the home, log, lock
	sd, platform, userdata string
	tools, emus            string
	home                   string // <userdata>/<tool>
	storefront, githubAPI  string
	logFile                string
	out, errw              io.Writer
	client                 *http.Client
	strict                 bool
	exe                    string // this program's path (tests); "" = os.Executable
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func newEnv() *env { return newBase("pak") }

// newBase sets up what pak and port share: paths, log, HTTP client, output.
func newBase(tool string) *env {
	sd := getenv("SDCARD_PATH", "/mnt/SDCARD")
	plat := getenv("PLATFORM", "h700")
	ud := getenv("USERDATA_PATH", filepath.Join(sd, ".userdata", plat))
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.RegisterProtocol("file", http.NewFileTransport(http.Dir("/"))) // tests
	tr.ResponseHeaderTimeout = 30 * time.Second
	return &env{
		tool: tool, sd: sd, platform: plat, userdata: ud,
		tools:      filepath.Join(sd, "Tools", plat),
		emus:       filepath.Join(sd, "Emus", plat),
		home:       filepath.Join(ud, tool),
		storefront: getenv("PAK_STOREFRONT", defaultStorefront),
		githubAPI:  strings.TrimRight(getenv("PAK_GITHUB_API", "https://api.github.com"), "/"),
		logFile:    filepath.Join(getenv("LOGS_PATH", filepath.Join(ud, "logs")), tool+".txt"),
		out:        os.Stdout, errw: os.Stderr,
		client: &http.Client{Transport: tr},
		strict: os.Getenv("PAK_STRICT") == "1",
	}
}

func (e *env) say(f string, a ...any)  { fmt.Fprintf(e.out, f+"\n", a...) }
func (e *env) warn(f string, a ...any) { fmt.Fprintf(e.errw, "warning: "+f+"\n", a...) }

// log appends one line to logs/<tool>.txt (mutating commands only).
func (e *env) log(f string, a ...any) {
	_ = os.MkdirAll(filepath.Dir(e.logFile), 0o755)
	fh, err := os.OpenFile(e.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	fmt.Fprintf(fh, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(f, a...))
}

// get fetches a small document (catalogue, GitHub API) with retries.
func (e *env) get(url string, tries int) ([]byte, error) {
	var last error
	for i := 0; i < tries; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i) * 2 * time.Second)
		}
		data, err := e.getOnce(url, 60*time.Second, 16<<20)
		if err == nil {
			return data, nil
		}
		last = err
		var he httpError
		if errors.As(err, &he) && he.code >= 400 && he.code < 500 && he.code != 429 {
			break // not found, rate limited (403): retrying will not help
		}
	}
	return nil, last
}

type httpError struct{ code int }

func (h httpError) Error() string { return fmt.Sprintf("HTTP %d", h.code) }

func (e *env) getOnce(url string, timeout time.Duration, max int64) ([]byte, error) {
	c := *e.client
	c.Timeout = timeout
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", e.userAgent())
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, httpError{resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, max))
}

func isDir(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }
func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// writeAtomic writes path.tmp then renames it over path.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// lock serialises mutating commands (Updates pak, adb, Terminal), per tool.
func (e *env) lock() (func(), error) {
	if err := os.MkdirAll(e.home, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(e.home, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another %s command is running", e.tool)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// --- name lists: held paks, hidden Tools ---------------------------------

func readList(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(strings.TrimSuffix(sc.Text(), "\r"))
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// editList adds or removes names, keeping comments and order.
func editList(path, header string, add bool, names ...string) (bool, error) {
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(data), "\r", ""), "\n"), "\n")
	if len(data) == 0 {
		lines = []string{header}
	}
	changed := false
	for _, n := range names {
		idx := -1
		for i, l := range lines {
			if strings.TrimSpace(l) == n {
				idx = i
			}
		}
		if add && idx < 0 {
			lines = append(lines, n)
			changed = true
		} else if !add && idx >= 0 {
			lines = append(lines[:idx], lines[idx+1:]...)
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	return true, writeAtomic(path, []byte(strings.Join(lines, "\n")+"\n"))
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func (e *env) heldFile() string   { return filepath.Join(e.home, "held") }
func (e *env) hiddenFile() string { return filepath.Join(e.userdata, "nextui-mods", "hidden-tools") }

func (e *env) held(x *installed, p catPak) bool {
	h := readList(e.heldFile())
	return contains(h, x.Name) || (x.ID != "" && contains(h, x.ID)) || (p.ID != "" && contains(h, p.ID))
}

func (e *env) hiddenTool(name string) bool { return contains(readList(e.hiddenFile()), name) }

// hideTools applies the hidden-tools list like nextui/guard.sh does at boot:
// a <name>.pak that came back (NextUI update, pakz) becomes <name>.pak.disabled,
// the newest copy wins. Returns the names it hid.
func (e *env) hideTools() ([]string, error) {
	var done []string
	for _, n := range readList(e.hiddenFile()) {
		p := filepath.Join(e.tools, n+".pak")
		if !isDir(p) {
			continue
		}
		if err := os.RemoveAll(p + ".disabled"); err != nil {
			return done, err
		}
		if err := os.Rename(p, p+".disabled"); err != nil {
			return done, err
		}
		done = append(done, n)
	}
	return done, nil
}
