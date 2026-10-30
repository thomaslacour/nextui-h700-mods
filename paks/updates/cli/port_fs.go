package main

// What PORTS.pak does when its GUI closes (run_portmaster_gui in its
// launch.sh), redone after every port change, plus the trash and backups.
// Never destructive by default: removed ports go to <home>/removed, files an
// upgrade replaces go to <home>/backups, squashfs images are rebuilt beside
// the original and renamed only once complete.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// busy names the PORTS.pak session running (GUI or a port), "" if none.
// Installing under a running port could change files it has open.
func (p *portEnv) busy() string {
	m, _ := filepath.Glob(filepath.Join(p.proc, "[0-9]*", "cmdline"))
	self := fmt.Sprint(os.Getpid())
	for _, f := range m {
		if filepath.Base(filepath.Dir(f)) == self {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil || !bytes.Contains(data, []byte("PORTS.pak/launch.sh")) {
			continue
		}
		args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
		return filepath.Base(args[len(args)-1])
	}
	return ""
}

// staleMount: a bind of .ports left by a PORTS.pak session that crashed.
// Harmless (PORTS.pak clears it at its next launch), reported by doctor.
func (p *portEnv) staleMount() bool {
	data, err := os.ReadFile(filepath.Join(p.proc, "mounts"))
	return err == nil && bytes.Contains(bytes.ToLower(data), []byte(".ports_temp/ports"))
}

func (p *portEnv) mutate(fn func() error) error {
	if b := p.busy(); b != "" {
		return fmt.Errorf("PortMaster is running (%s): close it first", b)
	}
	unlock, err := p.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// finish publishes the ports in the menu and prepares new runtimes, like
// PORTS.pak after its GUI. dirs limits the progressor scan (nil = all ports).
func (p *portEnv) finish(dirs []string) {
	p.replaceProgressor(dirs)
	p.processSquashfs()
	p.publish()
	syscall.Sync()
}

// publish runs the boot hook that copies launchers and artwork to the menu
// (one implementation for boot and for port).
func (p *portEnv) publish() {
	if !exists(p.hook) {
		p.warn("%s missing: launchers not published in the menu (mac/deploy.sh)", p.hook)
		return
	}
	cmd := exec.Command("/bin/sh", p.hook)
	if !exists("/bin/sh") {
		cmd = exec.Command("sh", p.hook)
	}
	cmd.Env = append(os.Environ(), "SDCARD_PATH="+p.sd, "USERDATA_PATH="+p.userdata, "PORTS_ROM_DIR="+p.rom)
	if out, err := cmd.CombinedOutput(); err != nil {
		p.warn("publishing the launchers failed: %v %s", err, strings.TrimSpace(string(out)))
	}
}

// launcherState: "ok", "missing" (not in the menu) or "differs" from .ports.
func (p *portEnv) launcherState(script string) string {
	src, err1 := os.ReadFile(filepath.Join(p.ports, script))
	dst, err2 := os.ReadFile(filepath.Join(p.rom, script))
	switch {
	case err1 != nil:
		return "no source"
	case err2 != nil:
		return "missing"
	case !bytes.Equal(src, dst):
		return "differs"
	}
	return "ok"
}

func sameFile(a, b string) bool {
	x, err1 := os.ReadFile(a)
	y, err2 := os.ReadFile(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// replaceProgressor: ports that ship PortMaster's progressor get the NextUI
// one (and minui-presenter beside it), as PORTS.pak does.
func (p *portEnv) replaceProgressor(dirs []string) {
	prog := filepath.Join(p.pakDir, "files", "progressor")
	pres := filepath.Join(p.pakDir, "files", "minui-presenter")
	if !exists(prog) || !exists(pres) {
		return
	}
	if dirs == nil {
		dirs = []string{p.ports}
	}
	for _, d := range dirs {
		filepath.WalkDir(d, func(path string, de fs.DirEntry, err error) error {
			if err != nil || !de.Type().IsRegular() || de.Name() != "progressor" {
				return nil
			}
			if !sameFile(prog, path) {
				if err := copyFile(prog, path, 0o755); err != nil {
					p.warn("progressor %s: %v", p.rel(path), err)
					return nil
				}
				p.log("progressor replaced: %s", p.rel(path))
			}
			if t := filepath.Join(filepath.Dir(path), "minui-presenter"); !exists(t) {
				copyFile(pres, t, 0o755)
			}
			return nil
		})
	}
}

// --- runtimes: the scripts inside a squashfs need NextUI's paths ------------

func (p *portEnv) libsDir() string { return filepath.Join(p.emuDir, "libs") }

func (p *portEnv) toolPath(name string) string {
	if f := filepath.Join(p.pakDir, "bin", name); exists(f) {
		return f
	}
	if f, err := exec.LookPath(name); err == nil {
		return f
	}
	return ""
}

func needsProcessing(sq string) bool {
	st, err := os.Stat(sq)
	if err != nil {
		return false
	}
	m, err := os.Stat(sq + ".processed")
	return err != nil || !st.ModTime().Before(m.ModTime())
}

// processSquashfs is PORTS.pak's process_squashfs_files: each runtime not yet
// processed is unpacked, its shell scripts get "#!/usr/bin/env bash" and the
// real PortMaster path, then it is rebuilt — into <file>.new, renamed only
// once mksquashfs succeeded (PORTS.pak deletes the original first).
func (p *portEnv) processSquashfs() {
	m, _ := filepath.Glob(filepath.Join(p.libsDir(), "*.squashfs"))
	failed := readList(p.failedFile())
	for _, sq := range m {
		if !needsProcessing(sq) {
			continue
		}
		id := fileID(sq)
		if contains(failed, id) {
			continue // same file failed before: not retried until it changes
		}
		if err := p.processOne(sq); err != nil {
			p.warn("runtime %s: %v (left as is)", filepath.Base(sq), err)
			p.log("runtime %s not processed: %v", filepath.Base(sq), err)
			editList(p.failedFile(), "# Runtimes that could not be prepared (name size mtime): retried once changed", true, id)
		}
	}
}

func (p *portEnv) failedFile() string { return filepath.Join(p.home, "runtimes-failed") }

// fileID names one version of a file: "name size mtime".
func fileID(f string) string {
	st, err := os.Stat(f)
	if err != nil {
		return filepath.Base(f)
	}
	return fmt.Sprintf("%s %d %d", filepath.Base(f), st.Size(), st.ModTime().Unix())
}

// ownArtwork: the menu image is the port's own (what the boot hook copies:
// cover.png, else a screenshot), not an older image of the same name.
func ownArtwork(media, portDir string) bool {
	cands, _ := filepath.Glob(filepath.Join(portDir, "screenshot*"))
	for _, c := range append([]string{filepath.Join(portDir, "cover.png")}, cands...) {
		if sameFile(c, media) {
			return true
		}
	}
	return false
}

func (p *portEnv) processOne(sq string) error {
	un, mk := p.toolPath("unsquashfs"), p.toolPath("mksquashfs")
	if un == "" || mk == "" {
		return fmt.Errorf("unsquashfs/mksquashfs missing")
	}
	st, err := os.Stat(sq)
	if err != nil {
		return err
	}
	// Extracted size estimated at 4x: must fit in /tmp (tmpfs), as PORTS.pak.
	if free, err := freeBytes(p.tmp); err == nil && uint64(st.Size())*4 > free {
		return fmt.Errorf("too big to unpack in %s", p.tmp)
	}
	work, err := os.MkdirTemp(p.tmp, "port-squashfs.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	root := filepath.Join(work, "root")
	env := p.toolEnv()
	run := func(name string, args ...string) error {
		c := exec.Command(name, args...)
		c.Env = env
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %v %s", filepath.Base(name), err, tail(string(out), 3))
		}
		return nil
	}
	if err := run(un, "-no-progress", "-d", root, sq); err != nil {
		return err
	}
	changed, err := fixRuntimeScripts(root, p.emuDir)
	if err != nil {
		return err
	}
	if changed > 0 {
		tmp := sq + ".new"
		os.Remove(tmp)
		if err := run(mk, root, tmp, "-noappend", "-comp", "xz", "-no-progress"); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, sq); err != nil {
			os.Remove(tmp)
			return err
		}
		p.log("runtime %s: %d script(s) adapted", filepath.Base(sq), changed)
		p.say("runtime %s prepared (%d script(s) adapted)", filepath.Base(sq), changed)
	}
	// The marker must be newer than the image: FAT keeps 2 s steps.
	if st, err = os.Stat(sq); err != nil {
		return err
	}
	mark := sq + ".processed"
	if err := os.WriteFile(mark, nil, 0o644); err != nil {
		return err
	}
	t := st.ModTime().Add(2 * time.Second)
	return os.Chtimes(mark, t, t)
}

// fixRuntimeScripts adapts shell scripts like PORTS.pak's find_shell_scripts +
// update_shebangs + update_portmaster_path. Returns the number of files changed.
func fixRuntimeScripts(root, emuDir string) (int, error) {
	n := 0
	err := filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil || !de.Type().IsRegular() {
			return err
		}
		info, err := de.Info()
		if err != nil || info.Mode()&0o111 == 0 {
			return err
		}
		name := de.Name()
		switch filepath.Ext(name) {
		case ".sh", ".bash", ".src", ".txt":
		default:
			if strings.Contains(name, ".") {
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out, ok := fixScript(data, emuDir)
		if !ok {
			return nil
		}
		if err := os.WriteFile(path, out, info.Mode().Perm()); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

// fixScript returns the adapted script and whether it changed.
func fixScript(data []byte, emuDir string) ([]byte, bool) {
	first, _, _ := bytes.Cut(data, []byte("\n"))
	first = bytes.TrimSuffix(first, []byte("\r"))
	if !bytes.HasPrefix(first, []byte("#!")) || !bytes.HasSuffix(first, []byte("sh")) {
		return nil, false
	}
	out := data
	if bytes.Contains(out, []byte("#!/bin/bash")) && string(first) != "#!/usr/bin/env bash" {
		_, rest, found := bytes.Cut(out, []byte("\n"))
		out = append([]byte("#!/usr/bin/env bash"), func() []byte {
			if found {
				return append([]byte("\n"), rest...)
			}
			return nil
		}()...)
	}
	out = bytes.ReplaceAll(out, []byte("/roms/ports/PortMaster"), []byte(emuDir))
	return out, !bytes.Equal(out, data)
}

// --- trash and backups --------------------------------------------------------

type move struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type trashEntry struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Removed string `json:"removed"`
	Moves   []move `json:"moves"`
	dir     string
}

func (p *portEnv) trashDir() string  { return filepath.Join(p.home, "removed") }
func (p *portEnv) backupDir() string { return filepath.Join(p.home, "backups") }

func stamp() string { return time.Now().Format("20060102-150405") }

func (p *portEnv) trashEntries() []trashEntry {
	m, _ := filepath.Glob(filepath.Join(p.trashDir(), "*", "manifest.json"))
	var out []trashEntry
	for _, f := range m {
		var t trashEntry
		data, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(data, &t) != nil {
			p.warn("unreadable trash entry: %s", filepath.Dir(f))
			continue
		}
		t.dir = filepath.Dir(f)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dir > out[j].dir }) // newest first
	return out
}

func (t *trashEntry) save() error {
	data, _ := json.MarshalIndent(t, "", "  ")
	return writeAtomic(filepath.Join(t.dir, "manifest.json"), append(data, '\n'))
}

// moveAll renames each pair in order, recording what was done in t (so a
// half-done removal can still be restored). Stops at the first failure.
func (t *trashEntry) moveAll(pairs []move) error {
	for _, m := range pairs {
		if err := os.MkdirAll(filepath.Dir(m.To), 0o755); err != nil {
			return err
		}
		if err := os.Rename(m.From, m.To); err != nil {
			return fmt.Errorf("move %s: %v", m.From, err)
		}
		t.Moves = append(t.Moves, m)
	}
	return nil
}

func dirSize(root string) int64 {
	var n int64
	filepath.WalkDir(root, func(_ string, de fs.DirEntry, err error) error {
		if err == nil && de.Type().IsRegular() {
			if i, err := de.Info(); err == nil {
				n += i.Size()
			}
		}
		return nil
	})
	return n
}

// pruneBackups keeps the 3 newest backups of each port.
func (p *portEnv) pruneBackups(name string) {
	m, _ := filepath.Glob(filepath.Join(p.backupDir(), "*-"+name))
	sort.Sort(sort.Reverse(sort.StringSlice(m)))
	for i, d := range m {
		if i >= 3 {
			os.RemoveAll(d)
		}
	}
}
