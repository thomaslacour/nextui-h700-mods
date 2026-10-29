package main

// Download, verification and installation of catalogue paks, the way the
// Pak Store does it (same URL, same folders, .pakz unpacked at the SD root),
// with more care: SHA-256 and size from the GitHub release when published,
// the whole archive checked (CRC, paths) before anything is written, free
// space checked, update_ignore honoured on updates, hidden Tools kept hidden,
// a fresh install staged in <name>.pak.new then renamed.

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const pakStoreID = "xK9mR2vL4w"

func (p catPak) downloadURL() string {
	if p.URL != "" {
		return p.URL
	}
	return strings.TrimRight(p.RepoURL, "/") + "/releases/download/" + p.Version + "/" + p.ReleaseFilename
}

// releaseAsset asks GitHub for the size and SHA-256 of the release file.
func (e *env) releaseAsset(p catPak) (int64, string, error) {
	if p.URL != "" { // our catalogues: checksum given, and required
		if len(p.SHA256) != 64 {
			return 0, "", fmt.Errorf("catalogue gives no sha256 for %s", p.Name)
		}
		return p.Size, strings.ToLower(p.SHA256), nil
	}
	u, err := url.Parse(p.RepoURL)
	if err != nil {
		return 0, "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return 0, "", fmt.Errorf("not a GitHub repository: %s", p.RepoURL)
	}
	repo := parts[len(parts)-2] + "/" + strings.TrimSuffix(parts[len(parts)-1], ".git")
	data, err := e.get(e.githubAPI+"/repos/"+repo+"/releases/tags/"+url.PathEscape(p.Version), 2)
	if err != nil {
		return 0, "", fmt.Errorf("GitHub API: %v", err)
	}
	var rel struct {
		Assets []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &rel); err != nil {
		return 0, "", fmt.Errorf("GitHub API: %v", err)
	}
	for _, a := range rel.Assets {
		if a.Name == p.ReleaseFilename {
			return a.Size, strings.TrimPrefix(a.Digest, "sha256:"), nil
		}
	}
	return 0, "", fmt.Errorf("release %s has no file %s", p.Version, p.ReleaseFilename)
}

// download fetches the release file into <home>/cache and verifies it.
// It returns the file and a one-word verification note.
func (e *env) download(p catPak) (string, string, error) {
	cache := filepath.Join(e.home, "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", "", err
	}
	olds, _ := filepath.Glob(filepath.Join(cache, "*"))
	for _, f := range olds {
		os.Remove(f) // one download at a time (lock): leftovers only
	}
	size, digest, aerr := e.releaseAsset(p)
	if aerr != nil && p.URL != "" {
		return "", "", aerr // never install our paks unverified
	}
	switch {
	case aerr != nil && e.strict:
		return "", "", fmt.Errorf("no checksum: %v (PAK_STRICT=1)", aerr)
	case aerr != nil:
		e.warn("checksum not available (%v): the archive is only checked for integrity", aerr)
	case digest == "" && e.strict:
		return "", "", errors.New("the release publishes no checksum (PAK_STRICT=1)")
	case digest == "":
		e.warn("the release publishes no checksum: the archive is only checked for integrity")
	}
	if size > 0 {
		if free, err := freeBytes(cache); err == nil && free < uint64(size)+16<<20 {
			return "", "", fmt.Errorf("not enough space on the SD card (%d MB needed)", size>>20+16)
		}
	}
	part := filepath.Join(cache, p.ID+".part")
	f, err := os.Create(part)
	if err != nil {
		return "", "", err
	}
	defer os.Remove(part)
	c := *e.client
	c.Timeout = 60 * time.Minute
	req, err := http.NewRequest("GET", p.downloadURL(), nil)
	if err != nil {
		f.Close()
		return "", "", err
	}
	req.Header.Set("User-Agent", e.userAgent())
	resp, err := c.Do(req)
	if err != nil {
		f.Close()
		return "", "", fmt.Errorf("download failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		f.Close()
		return "", "", fmt.Errorf("download failed: HTTP %d (%s)", resp.StatusCode, p.downloadURL())
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", "", fmt.Errorf("download interrupted: %v", err)
	}
	if size > 0 && n != size {
		return "", "", fmt.Errorf("incomplete download (%d of %d bytes)", n, size)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	note := "unverified"
	if digest != "" {
		if !strings.EqualFold(sum, digest) {
			return "", "", fmt.Errorf("wrong checksum (sha256 %s, release says %s)", sum, digest)
		}
		note = "sha256 ok"
	}
	final := filepath.Join(cache, p.ID+filepath.Ext(p.ReleaseFilename))
	if err := os.Rename(part, final); err != nil {
		return "", "", err
	}
	e.log("%s %s: %s sha256=%s (%s)", p.Name, p.Version, p.downloadURL(), sum, note)
	return final, note, nil
}

// planned is one archive member and where it goes, relative to the root.
type planned struct {
	f   *zip.File
	rel string
}

// cleanRel validates an archive path: relative, no "..", no backslash.
func cleanRel(name string) (string, bool) {
	if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return "", false
	}
	c := path.Clean(name)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

// ignored tells whether a path inside a pak matches update_ignore. Patterns
// match the path or any of its leading directories ("config.json",
// "drastic/config/drastic.cfg", "caps/", "PortMaster").
func ignored(pakRel string, patterns []string) bool {
	parts := strings.Split(pakRel, "/")
	for _, pat := range patterns {
		pat = strings.TrimSuffix(strings.TrimPrefix(pat, "./"), "/")
		if pat == "" {
			continue
		}
		for i := 1; i <= len(parts); i++ {
			if ok, _ := path.Match(pat, strings.Join(parts[:i], "/")); ok {
				return true
			}
		}
	}
	return false
}

// innerPakRel is the part of a .pakz path below its "<name>.pak/" folder.
func innerPakRel(rel string) string {
	parts := strings.Split(rel, "/")
	for i, s := range parts {
		if strings.HasSuffix(s, ".pak") || strings.HasSuffix(s, ".pak.disabled") {
			return strings.Join(parts[i+1:], "/")
		}
	}
	return rel
}

// plan checks the whole archive (paths, CRC by reading every member) and
// maps members to their destination. root is the pak folder for a .zip, the
// SD card for a .pakz.
func (e *env) plan(p catPak, zr *zip.Reader, update bool) ([]planned, uint64, error) {
	// A zip of the folder itself ("X.pak/launch.sh") instead of its content.
	strip := ""
	if !p.isPakz() && len(zr.File) > 0 {
		first := strings.SplitN(zr.File[0].Name, "/", 2)[0]
		if strings.HasSuffix(first, ".pak") {
			strip = first + "/"
			for _, f := range zr.File {
				if !strings.HasPrefix(f.Name, strip) && f.Name != first {
					strip = ""
					break
				}
			}
		}
	}
	var out []planned
	var total uint64
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, strip)
		if name == "" || name == strings.TrimSuffix(strip, "/") {
			continue
		}
		rel, ok := cleanRel(name)
		if !ok {
			return nil, 0, fmt.Errorf("unsafe path in the archive: %q", f.Name)
		}
		if f.Mode()&os.ModeSymlink != 0 {
			e.warn("symbolic link skipped: %s", rel)
			continue
		}
		pakRel := rel
		if p.isPakz() {
			parts := strings.Split(rel, "/")
			if (parts[0] == "Tools" || parts[0] == "Emus") && len(parts) > 1 && parts[1] != e.platform {
				continue // other devices
			}
			if parts[0] == "Tools" && len(parts) > 2 && strings.HasSuffix(parts[2], ".pak") {
				name := strings.TrimSuffix(parts[2], ".pak")
				dir := filepath.Join(e.tools, parts[2])
				if e.hiddenTool(name) || (!isDir(dir) && isDir(dir+".disabled")) {
					parts[2] += ".disabled"
					rel = strings.Join(parts, "/")
				}
			}
			pakRel = innerPakRel(rel)
		}
		if update && !f.FileInfo().IsDir() && ignored(pakRel, p.UpdateIgnore) {
			continue
		}
		if !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				return nil, 0, fmt.Errorf("damaged archive: %v", err)
			}
			_, err = io.Copy(io.Discard, rc)
			rc.Close()
			if err != nil {
				return nil, 0, fmt.Errorf("damaged archive (%s): %v", rel, err)
			}
			total += f.UncompressedSize64
		}
		out = append(out, planned{f, rel})
	}
	if len(out) == 0 {
		return nil, 0, errors.New("the archive installs nothing for this device")
	}
	return out, total, nil
}

func extract(root string, files []planned) error {
	for _, pl := range files {
		dst := filepath.Join(root, filepath.FromSlash(pl.rel))
		if !strings.HasPrefix(dst, filepath.Clean(root)+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path: %s", pl.rel)
		}
		if pl.f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rc, err := pl.f.Open()
		if err != nil {
			return err
		}
		tmp := dst + ".tmp"
		w, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err == nil {
			_, err = io.Copy(w, rc)
			if cerr := w.Close(); err == nil {
				err = cerr
			}
		}
		rc.Close()
		if err == nil {
			err = os.Rename(tmp, dst)
		}
		if err != nil {
			os.Remove(tmp)
			return fmt.Errorf("%s: %v", pl.rel, err)
		}
	}
	return nil
}

// install installs or updates one catalogue pak. force allows a pak of
// another platform and replacing a folder the catalogue did not install.
func (e *env) install(p catPak, reg *registry, force bool) error {
	if p.Disabled {
		return fmt.Errorf("%s is disabled in the catalogue", p.title())
	}
	if !p.supports(e.platform) && !force {
		return fmt.Errorf("%s does not support %s (platforms: %s; --force to install anyway)",
			p.title(), e.platform, strings.Join(p.Platforms, " "))
	}
	if p.ReleaseFilename == "" || (p.RepoURL == "" && p.URL == "") || p.Version == "" {
		return fmt.Errorf("%s: incomplete catalogue entry", p.title())
	}
	if p.Type != "TOOL" && p.Type != "EMU" && !p.isPakz() {
		return fmt.Errorf("%s: unknown pak type %q", p.title(), p.Type)
	}
	have := reg.byID(p.ID)
	if have == nil && p.URL == "" { // ours share one repository: id only
		have = reg.match(&installed{RepoURL: p.RepoURL})
	}
	dir, _, present := e.pakPath(p.Type, p.Name)
	if !p.isPakz() && present && have == nil && !force {
		return fmt.Errorf("%s already exists and was not installed from the catalogue (own pak?): --force to replace it",
			strings.TrimPrefix(dir, e.sd+"/"))
	}
	update := have != nil && present
	file, note, err := e.download(p)
	if err != nil {
		return err
	}
	defer os.Remove(file)
	zr, err := zip.OpenReader(file)
	if err != nil {
		return fmt.Errorf("not a zip archive: %v", err)
	}
	defer zr.Close()
	files, total, err := e.plan(p, &zr.Reader, update)
	if err != nil {
		return err
	}
	if free, err := freeBytes(e.sd); err == nil && free < total+16<<20 {
		return fmt.Errorf("not enough space on the SD card (%d MB needed)", total>>20+16)
	}
	// Updating the pak that holds this very program (Updates.pak/pak): keep
	// a copy, then check the new one starts, else put the copy back.
	self := e.selfIn(dir)
	if self != "" {
		os.MkdirAll(e.home, 0o755)
		if err := copyFile(self, e.previousSelf(), 0o755); err != nil {
			return fmt.Errorf("cannot keep a copy of %s: %v", filepath.Base(self), err)
		}
	}
	switch {
	case p.isPakz():
		err = extract(e.sd, files)
	case update:
		err = extract(dir, files)
	default: // fresh: stage, then swap (also replaces an own pak with --force)
		stage := dir + ".new"
		os.RemoveAll(stage)
		if err = extract(stage, files); err == nil {
			old := dir + ".old"
			os.RemoveAll(old)
			if present {
				err = os.Rename(dir, old)
			}
			if err == nil {
				err = os.Rename(stage, dir)
			}
			if err == nil {
				os.RemoveAll(old)
			}
		}
		if err != nil {
			os.RemoveAll(stage)
		}
	}
	if err == nil && self != "" {
		err = e.checkSelf(self)
	}
	if err != nil {
		e.log("%s %s: install FAILED: %v", p.Name, p.Version, err)
		return fmt.Errorf("install failed: %v", err)
	}
	if hid, _ := e.hideTools(); len(hid) > 0 {
		e.log("hidden again: %s", strings.Join(hid, ", "))
	}
	if dir, _, ok := e.pakPath(p.Type, p.Name); ok && !exists(filepath.Join(dir, "launch.sh")) {
		e.warn("%s has no launch.sh: NextUI will not list it", strings.TrimPrefix(dir, e.sd+"/"))
	}
	entry := &installed{ID: p.ID, Name: p.Name, DisplayName: p.title(), RepoURL: p.RepoURL, Type: p.Type,
		Version: p.Version, CanUninstall: p.ID != pakStoreID, Source: "pak"}
	if have != nil {
		entry.CanUninstall = have.CanUninstall
	}
	reg.put(entry)
	if err := e.saveRegistry(reg); err != nil {
		return err
	}
	sc, what := p.Scripts.PostInstall, "post_install"
	if update {
		sc, what = p.Scripts.PostUpdate, "post_update"
	}
	e.runScript(p, sc, what)
	syscall.Sync()
	verb := "installed"
	if update {
		verb = "updated"
	}
	e.log("%s %s %s (%s)", p.Name, p.Version, verb, note)
	e.say("%s %s %s (%s)", p.title(), p.Version, verb, note)
	return nil
}

func (e *env) runScript(p catPak, sc script, what string) {
	if sc.Path == "" {
		return
	}
	dir, _, _ := e.pakPath(p.Type, p.Name)
	bin := sc.Path
	if !filepath.IsAbs(bin) {
		bin = filepath.Join(dir, bin)
	}
	cmd := exec.Command(bin, sc.Args...)
	if isDir(dir) {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		e.warn("%s script failed: %v %s", what, err, strings.TrimSpace(string(out)))
		e.log("%s %s script failed: %v", p.Name, what, err)
	}
}

func (e *env) uninstall(x *installed, c *catalog, reg *registry, force bool) error {
	if !x.CanUninstall && !force {
		return fmt.Errorf("%s cannot be uninstalled (--force to remove it anyway)", x.DisplayName)
	}
	dir, _, ok := e.pakPath(x.Type, x.Name)
	if ok {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	reg.remove(x)
	if err := e.saveRegistry(reg); err != nil {
		return err
	}
	if c != nil {
		if p, ok := c.entry(x); ok {
			e.runScript(p, p.Scripts.PostUninstall, "post_uninstall")
		}
	}
	syscall.Sync()
	e.log("%s uninstalled (%s)", x.Name, dir)
	e.say("%s uninstalled", x.DisplayName)
	return nil
}

// selfIn returns this program's path when it lives in dir (resolved), else "".
func (e *env) selfIn(dir string) string {
	exe, err := e.exe, error(nil)
	if exe == "" {
		exe, err = os.Executable()
	}
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if d, err := filepath.EvalSymlinks(dir); err == nil {
		dir = d
	}
	if strings.HasPrefix(exe, dir+string(filepath.Separator)) {
		return exe
	}
	return ""
}

func (e *env) previousSelf() string { return filepath.Join(e.home, e.tool+".previous") }

// checkSelf runs the freshly installed program; if it does not answer
// "<tool> version", the kept copy is put back (written aside, then renamed).
func (e *env) checkSelf(self string) error {
	cmd := exec.Command(self, "version")
	cmd.Env = append(os.Environ(), "PAK_SELF_CHECK=1")
	out, err := runTimeout(cmd, 20*time.Second)
	if err == nil && strings.HasPrefix(strings.TrimSpace(out), filepath.Base(self)+" ") {
		return nil
	}
	if rerr := copyFile(e.previousSelf(), self, 0o755); rerr != nil {
		return fmt.Errorf("the new %s does not start (%v) and the copy could not be put back: %v",
			filepath.Base(self), err, rerr)
	}
	return fmt.Errorf("the new %s does not start (%v): previous version put back", filepath.Base(self), err)
}

func runTimeout(cmd *exec.Cmd, d time.Duration) (string, error) {
	var b strings.Builder
	cmd.Stdout, cmd.Stderr = &b, &b
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return b.String(), err
	case <-time.After(d):
		cmd.Process.Kill()
		<-done
		return b.String(), fmt.Errorf("no answer after %s", d)
	}
}
