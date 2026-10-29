package main

// port: PortMaster ports from the command line (adb, ssh, Terminal), the
// same binary as pak called by another name. Installs go through PortMaster's
// own engine, harbourmaster, through the bridge .userdata/h700/ports/port-hm.py
// (paks/ports/port-hm.py): same sources, md5 checks, runtimes and H700 patches
// as the PortMaster GUI. This side adds what the GUI does on exit (launchers
// in the menu, artwork, runtime scripts, progressor), a trash for removed
// ports, backups before an upgrade, held ports, a lock and a doctor.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type portEnv struct {
	*env
	rom, ports, media string // Roms/Ports (PORTS), its .ports and .media
	pakDir, emuDir    string // Emus/h700/PORTS.pak and its PortMaster folder
	shared            string // .userdata/shared
	python, bridge    string
	hook              string // boot.d/17-ports-sync-entries.sync.sh
	proc, tmp         string
}

func newPortEnv() *portEnv {
	e := newBase("port")
	p := &portEnv{env: e}
	p.rom = getenv("PORTS_ROM_DIR", filepath.Join(e.sd, "Roms", "Ports (PORTS)"))
	p.ports = filepath.Join(p.rom, ".ports")
	p.media = filepath.Join(p.rom, ".media")
	p.pakDir = getenv("PORT_PAK_DIR", filepath.Join(e.emus, "PORTS.pak"))
	p.emuDir = filepath.Join(p.pakDir, "PortMaster")
	p.shared = getenv("SHARED_USERDATA_PATH", filepath.Join(e.sd, ".userdata", "shared"))
	p.python = getenv("PORT_PYTHON", filepath.Join(p.pakDir, "bin", "python3"))
	p.bridge = getenv("PORT_BRIDGE", filepath.Join(e.userdata, "ports", "port-hm.py"))
	p.hook = getenv("PORT_SYNC_HOOK", filepath.Join(e.userdata, ".hooks", "boot.d", "17-ports-sync-entries.sync.sh"))
	p.proc = getenv("PORT_PROC", "/proc")
	p.tmp = getenv("PORT_TMP", "/tmp")
	return p
}

func (p *portEnv) rel(path string) string { return strings.TrimPrefix(path, p.sd+"/") }

// toolEnv is the environment of PORTS.pak/launch.sh, the part harbourmaster
// and the squashfs tools need (PATH, libraries, certificates, PortMaster home).
func (p *portEnv) toolEnv() []string {
	home := filepath.Join(p.shared, "PORTS-portmaster")
	set := map[string]string{
		"PATH":                 p.emuDir + ":" + filepath.Join(p.pakDir, "bin") + ":" + os.Getenv("PATH"),
		"LD_LIBRARY_PATH":      strings.TrimSuffix(filepath.Join(p.pakDir, "lib")+":"+filepath.Join(p.sd, ".system", p.platform, "lib")+":"+os.Getenv("LD_LIBRARY_PATH"), ":"),
		"HOME":                 home,
		"XDG_DATA_HOME":        filepath.Join(home, ".local", "share"),
		"GT_DISABLE_PM_UPDATE": "1", // never let PortMaster update itself (pinned H700 build)
		"HM_TOOLS_DIR":         p.pakDir,
		"HM_PORTS_DIR":         p.ports,
		"HM_SCRIPTS_DIR":       p.ports,
		"PORT_EMU_DIR":         p.emuDir,
		"PORT_HM_LOG":          filepath.Join(filepath.Dir(p.logFile), "port-harbourmaster.txt"),
	}
	if ca := filepath.Join(p.pakDir, "files", "ca-certificates.crt"); exists(ca) {
		set["SSL_CERT_FILE"] = ca
	}
	var out []string
	for _, kv := range os.Environ() {
		if _, ok := set[strings.SplitN(kv, "=", 2)[0]]; !ok {
			out = append(out, kv)
		}
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}

// hm runs one bridge command and decodes its JSON answer into out. With
// live, harbourmaster's messages (download progress) are shown as they come;
// otherwise only when the command fails.
func (p *portEnv) hm(out any, live bool, args ...string) error {
	if !isDir(p.emuDir) {
		return fmt.Errorf("PortMaster is not installed (%s missing)", p.rel(p.emuDir))
	}
	python := p.python
	if !strings.Contains(python, "/") { // a bare name (tests): from PATH
		python, _ = exec.LookPath(python)
	}
	for _, f := range []string{python, p.bridge} {
		if f == "" || !exists(f) {
			return fmt.Errorf("%s missing (PORTS.pak python, or mac/deploy.sh for the bridge)", map[bool]string{true: p.python, false: f}[f == ""])
		}
	}
	cmd := exec.Command(python, append([]string{p.bridge}, args...)...)
	cmd.Dir = p.emuDir
	cmd.Env = p.toolEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if live {
		cmd.Stderr = p.errw
	} else {
		cmd.Stderr = &stderr
	}
	runErr := cmd.Run()
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	last := []byte(lines[len(lines)-1])
	if err := json.Unmarshal(last, &r); err != nil {
		if !live && stderr.Len() > 0 {
			fmt.Fprint(p.errw, tail(stderr.String(), 20))
		}
		if runErr != nil {
			return fmt.Errorf("PortMaster engine failed: %v (logs/port-harbourmaster.txt)", runErr)
		}
		return errors.New("PortMaster engine gave no answer (logs/port-harbourmaster.txt)")
	}
	if !r.OK {
		if !live && stderr.Len() > 0 {
			fmt.Fprint(p.errw, tail(stderr.String(), 20))
		}
		if r.Error == "" {
			r.Error = "PortMaster engine failed"
		}
		return errors.New(r.Error)
	}
	if out != nil {
		return json.Unmarshal(last, out)
	}
	return nil
}

func tail(s string, n int) string {
	l := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n") + "\n"
}

// --- what the bridge reports ------------------------------------------------

type hmSrc struct {
	Prefix      string `json:"prefix"`
	MD5         string `json:"md5"`
	DateAdded   string `json:"date_added"`
	DateUpdated string `json:"date_updated"`
	Size        int64  `json:"size"`
	URL         string `json:"url"`
}

type hmPort struct {
	Name      string   `json:"name"` // zip name, harbourmaster's key: celeste.zip
	Title     string   `json:"title"`
	Items     []string `json:"items"`
	Files     []string `json:"files"`     // top-level items installed
	Exclusive []string `json:"exclusive"` // ... that no other port shares
	MD5       string   `json:"md5"`       // of the installed archive ("" = local file)
	From      string   `json:"from"`
	Runtimes  []string `json:"runtimes"`
	Broken    bool     `json:"broken"`
	Update    bool     `json:"update"`
	Src       *hmSrc   `json:"src"`
}

type hmCat struct {
	hmSrc
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Desc         string   `json:"desc"`
	Inst         string   `json:"inst"`
	Genres       []string `json:"genres"`
	Porters      []string `json:"porters"`
	Runtimes     []string `json:"runtimes"`
	Arch         []string `json:"arch"`
	Items        []string `json:"items"`
	RTR          bool     `json:"rtr"`
	Exp          bool     `json:"exp"`
	Availability string   `json:"availability"`
	Compatible   bool     `json:"compatible"`
}

type hmRuntime struct {
	Name   string `json:"name"`
	Status string `json:"status"` // Verified, Unverified, Broken, Not Installed
}

type hmState struct {
	Device struct {
		Name, Device, Platform string
	} `json:"device"`
	Patched bool `json:"patched"`
	Sources []struct {
		Prefix string `json:"prefix"`
		Name   string `json:"name"`
		Ports  int    `json:"ports"`
	} `json:"sources"`
	Installed     []hmPort    `json:"installed"`
	Unknown       []string    `json:"unknown"`
	Runtimes      []hmRuntime `json:"runtimes"`
	RuntimesError string      `json:"runtimes_error"`
	Catalogue     []hmCat     `json:"catalogue"`
}

func (p *portEnv) state(catalogue bool) (*hmState, error) {
	args := []string{"state"}
	if catalogue {
		args = append(args, "--catalogue")
	}
	s := &hmState{}
	if err := p.hm(s, false, args...); err != nil {
		return nil, err
	}
	sort.Slice(s.Installed, func(i, j int) bool {
		return strings.ToLower(s.Installed[i].Title) < strings.ToLower(s.Installed[j].Title)
	})
	sort.Slice(s.Catalogue, func(i, j int) bool {
		return strings.ToLower(s.Catalogue[i].Title) < strings.ToLower(s.Catalogue[j].Title)
	})
	return s, nil
}

func (s *hmState) runtime(name string) string {
	name = runtimeFile(name)
	for _, r := range s.Runtimes {
		if r.Name == name {
			return r.Status
		}
	}
	return ""
}

func runtimeFile(n string) string {
	if strings.HasSuffix(n, ".squashfs") {
		return n
	}
	return n + ".squashfs"
}

// --- names ------------------------------------------------------------------

func stem(zip string) string { return strings.TrimSuffix(zip, ".zip") }

// portKeys: every name a port answers to — celeste.zip, celeste, Celeste
// (title), its launcher "Celeste" (Celeste.sh) and its folders.
func portKeys(name, title string, items []string) []string {
	keys := []string{name, stem(name), title}
	for _, it := range items {
		it = strings.TrimSuffix(it, "/")
		keys = append(keys, it, strings.TrimSuffix(it, ".sh"))
	}
	return keys
}

func matchKeys(q string, keys []string) bool {
	q2 := strings.TrimSuffix(strings.TrimSuffix(q, ".zip"), ".sh")
	for _, k := range keys {
		if k != "" && (strings.EqualFold(k, q) || strings.EqualFold(k, q2)) {
			return true
		}
	}
	return false
}

func (s *hmState) findInstalled(q string) (*hmPort, error) {
	var hit []*hmPort
	for i := range s.Installed {
		x := &s.Installed[i]
		if matchKeys(q, portKeys(x.Name, x.Title, append(x.Items, x.Files...))) {
			hit = append(hit, x)
		}
	}
	switch len(hit) {
	case 0:
		return nil, fmt.Errorf("%q is not installed (port list)", q)
	case 1:
		return hit[0], nil
	}
	return nil, ambiguous(q, len(hit), func(i int) string { return stem(hit[i].Name) + " (" + hit[i].Title + ")" })
}

func (s *hmState) findCatalogue(q string, all bool) (*hmCat, error) {
	var hit []*hmCat
	for i := range s.Catalogue {
		c := &s.Catalogue[i]
		if matchKeys(q, portKeys(c.Name, c.Title, c.Items)) {
			hit = append(hit, c)
		}
	}
	if len(hit) > 1 && !all { // the device decides between namesakes
		var ok []*hmCat
		for _, c := range hit {
			if c.Compatible {
				ok = append(ok, c)
			}
		}
		if len(ok) > 0 {
			hit = ok
		}
	}
	switch len(hit) {
	case 0:
		return nil, fmt.Errorf("%q is not in the PortMaster sources (port search %s)", q, q)
	case 1:
		return hit[0], nil
	}
	return nil, ambiguous(q, len(hit), func(i int) string { return stem(hit[i].Name) + " (" + hit[i].Title + ")" })
}

func ambiguous(q string, n int, name func(int) string) error {
	var l []string
	for i := 0; i < n; i++ {
		l = append(l, name(i))
	}
	return fmt.Errorf("%q matches several ports: %s", q, strings.Join(l, ", "))
}

// --- our records: when a port was installed by `port`, and from which date --

type portRec struct {
	Date string `json:"date"` // date_updated of the release installed
	MD5  string `json:"md5"`
	At   string `json:"installed_at"`
}

func (p *portEnv) recFile() string { return filepath.Join(p.home, "installed.json") }

func (p *portEnv) loadRecs() map[string]portRec {
	r := map[string]portRec{}
	if data, err := os.ReadFile(p.recFile()); err == nil {
		if json.Unmarshal(data, &r) != nil {
			p.warn("%s is damaged: install dates forgotten", p.recFile())
			r = map[string]portRec{}
		}
	}
	return r
}

func (p *portEnv) saveRecs(r map[string]portRec) {
	data, _ := json.MarshalIndent(r, "", "  ")
	if err := writeAtomic(p.recFile(), append(data, '\n')); err != nil {
		p.warn("cannot save %s: %v", p.recFile(), err)
	}
}

func (p *portEnv) record(name, date, md5 string) {
	r := p.loadRecs()
	if md5 == "" {
		delete(r, name)
	} else {
		r[name] = portRec{Date: date, MD5: md5, At: time.Now().Format("2006-01-02 15:04")}
	}
	p.saveRecs(r)
}

// --- status of an installed port -------------------------------------------

type portStatus struct {
	have, avail, code, readable string
}

func (p *portEnv) heldPort(x *hmPort) bool {
	h := readList(p.heldFile())
	for _, k := range portKeys(x.Name, x.Title, nil) {
		if contains(h, k) {
			return true
		}
	}
	return false
}

func (p *portEnv) statusOf(x *hmPort, recs map[string]portRec) portStatus {
	s := portStatus{}
	if x.Src != nil {
		s.avail = x.Src.DateUpdated
	}
	switch {
	case x.MD5 == "":
		s.have = "file"
	case x.Src != nil && x.MD5 == x.Src.MD5:
		s.have = x.Src.DateUpdated
	case recs[x.Name].MD5 == x.MD5 && recs[x.Name].Date != "":
		s.have = recs[x.Name].Date
	default:
		s.have = "older"
	}
	switch {
	case x.Broken:
		s.code, s.readable = "broken", "broken (damaged port.json)"
	case x.Src == nil:
		s.code, s.readable = "local", "not in the PortMaster sources"
	case x.MD5 == "":
		s.code, s.readable = "file", "installed from a file (version unknown)"
	case x.Update && p.heldPort(x):
		s.code, s.readable = "held", "held"
	case x.Update:
		s.code, s.readable = "update", "update available"
	default:
		s.code, s.readable = "ok", "up to date"
	}
	return s
}
