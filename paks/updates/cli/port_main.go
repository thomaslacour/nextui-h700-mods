package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const portUsage = `port — PortMaster ports on NextUI, without the PortMaster GUI

  port update                    refresh the port sources (PortMaster, Multiverse)
  port list [--available|--upgradable] [--ready] [--all] [--porcelain]
                                 installed ports (default), catalogue, updates
  port search [--ready] [--all] word...
                                 search the catalogue (title, name, genre, porter)
  port info name                 details: instructions, runtimes, files, status
  port install [--force] name... install (--force: reinstall, or another device)
  port upgrade [-n] [name...]    update every upgradable port, or the named ones
  port uninstall [--purge] name...
                                 remove a port; kept in the trash unless --purge
  port restore [name]            put back a removed port (no name: the trash)
  port trash [--empty]           removed ports kept on the SD card
  port hold [name...]            never upgrade these ports (no name: list)
  port unhold name...
  port runtimes [--all]          runtimes (Mono, Godot…) and the ports using them
  port runtimes install name     download a runtime
  port sync                      publish launchers and artwork in the menu,
                                 prepare new runtimes (as when PortMaster closes)
  port doctor                    check engine, ports, launchers, runtimes, network
  port version

A name is the port's name (celeste), its title ("Void Stranger"), its
launcher or its zip (celeste.zip). Ready = runs without game files.
Upgrades save the files they replace in .userdata/h700/port/backups.
Environment: SDCARD_PATH, PLATFORM, USERDATA_PATH, PORTS_ROM_DIR.
`

func runPort(p *portEnv, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(p.errw, portUsage)
		return 2
	}
	cmd := args[0]
	flags, names := parseArgs(args[1:])
	need := func(what string) int {
		fmt.Fprintf(p.errw, "%s: %s\n\n%s", cmd, what, portUsage)
		return 2
	}
	var err error
	switch cmd {
	case "update":
		err = p.mutate(p.cmdUpdate)
	case "list", "ls":
		err = p.cmdList(flags, nil)
	case "search", "find":
		if len(names) == 0 {
			return need("search what?")
		}
		err = p.cmdList(flags, names)
	case "info", "show":
		if len(names) != 1 {
			return need("one port name")
		}
		err = p.cmdInfo(names[0], flags["all"])
	case "install":
		if len(names) == 0 {
			return need("which port?")
		}
		err = p.mutate(func() error { return p.cmdInstall(names, flags["force"] || flags["f"]) })
	case "upgrade":
		err = p.mutate(func() error { return p.cmdUpgrade(names, flags["n"] || flags["dry-run"]) })
	case "uninstall", "remove":
		if len(names) == 0 {
			return need("which port?")
		}
		err = p.mutate(func() error { return p.cmdUninstall(names, flags["purge"]) })
	case "restore":
		if len(names) == 0 {
			err = p.cmdTrash(false)
		} else {
			err = p.mutate(func() error { return p.cmdRestore(names[0]) })
		}
	case "trash":
		if flags["empty"] {
			err = p.mutate(func() error { return p.cmdTrash(true) })
		} else {
			err = p.cmdTrash(false)
		}
	case "hold", "unhold":
		if cmd == "unhold" && len(names) == 0 {
			return need("which port?")
		}
		err = p.cmdHold(cmd == "hold", names)
	case "runtimes", "runtime":
		if len(names) > 0 && names[0] == "install" {
			if len(names) != 2 {
				return need("runtimes install <name>")
			}
			err = p.mutate(func() error { return p.cmdRuntimeInstall(names[1]) })
		} else {
			err = p.cmdRuntimes(flags["all"])
		}
	case "sync":
		err = p.mutate(func() error { p.finish(nil); p.say("ports published, runtimes prepared"); return nil })
	case "doctor":
		if !p.cmdDoctor() {
			return 1
		}
	case "version", "--version":
		p.say("port %s", version)
	case "help", "-h", "--help":
		fmt.Fprint(p.out, portUsage)
	default:
		return need("unknown command")
	}
	if err != nil {
		fmt.Fprintf(p.errw, "error: %v\n", err)
		return 1
	}
	return 0
}

func (p *portEnv) cmdUpdate() error {
	var r struct {
		Sources int `json:"sources"`
	}
	if err := p.hm(&r, true, "update"); err != nil {
		return err
	}
	s, err := p.state(true)
	if err != nil {
		return err
	}
	n := 0
	for _, c := range s.Catalogue {
		if c.Compatible && !c.Exp {
			n++
		}
	}
	p.say("sources: %d, %d ports for this device", r.Sources, n)
	recs := p.loadRecs()
	up := 0
	for i := range s.Installed {
		if p.statusOf(&s.Installed[i], recs).code == "update" {
			up++
		}
	}
	if up > 0 {
		p.say("%d update(s) available: port list --upgradable", up)
	}
	p.log("sources updated")
	return nil
}

// --- list, search, info -----------------------------------------------------

func (p *portEnv) cmdList(flags map[string]bool, words []string) error {
	porcelain := flags["porcelain"] || flags["p"]
	if flags["available"] || words != nil {
		s, err := p.state(true)
		if err != nil {
			return err
		}
		inst := map[string]bool{}
		for _, x := range s.Installed {
			inst[x.Name] = true
		}
		rows := [][]string{{"NAME", "TITLE", "UPDATED", "SIZE", "READY", "INSTALLED"}}
		for _, c := range s.Catalogue {
			if !flags["all"] && (!c.Compatible || c.Exp) {
				continue
			}
			if flags["ready"] && !c.RTR {
				continue
			}
			if !matchWords(words, c) {
				continue
			}
			ready, have := "", ""
			if c.RTR {
				ready = "yes"
			}
			if inst[c.Name] {
				have = "yes"
			}
			if porcelain { // name, title, updated, size, ready, installed, compatible
				p.say("%s\t%s\t%s\t%d\t%s\t%s\t%v", stem(c.Name), c.Title, c.DateUpdated, c.Size, ready, have, c.Compatible)
				continue
			}
			rows = append(rows, []string{stem(c.Name), c.Title, c.DateUpdated, mb(c.Size), ready, have})
		}
		if !porcelain {
			if len(rows) == 1 {
				p.say("No port found%s.", map[bool]string{false: " (--all: other devices too)"}[flags["all"]])
				return nil
			}
			p.table(rows)
		}
		return nil
	}
	s, err := p.state(false)
	if err != nil {
		return err
	}
	recs := p.loadRecs()
	rows := [][]string{{"NAME", "TITLE", "INSTALLED", "AVAILABLE", "STATUS"}}
	for i := range s.Installed {
		x := &s.Installed[i]
		st := p.statusOf(x, recs)
		if flags["upgradable"] && st.code != "update" && !(flags["all"] && st.code == "held") {
			continue
		}
		if porcelain { // name, title, installed, available, status
			p.say("%s\t%s\t%s\t%s\t%s", stem(x.Name), x.Title, st.have, st.avail, st.code)
			continue
		}
		rows = append(rows, []string{stem(x.Name), x.Title, st.have, st.avail, st.readable})
	}
	if !flags["upgradable"] {
		for _, u := range s.Unknown {
			if porcelain {
				p.say("%s\t%s\t\t\tunknown", strings.TrimSuffix(u, ".sh"), u)
				continue
			}
			rows = append(rows, []string{strings.TrimSuffix(u, ".sh"), u, "", "", "not managed by PortMaster"})
		}
	}
	if !porcelain {
		if len(rows) == 1 {
			if flags["upgradable"] {
				p.say("All ports are up to date.")
			} else {
				p.say("No port installed.")
			}
			return nil
		}
		p.table(rows)
	}
	return nil
}

func matchWords(words []string, c hmCat) bool {
	return matchAll(words, append(append([]string{c.Name, c.Title, c.Desc}, c.Genres...), c.Porters...)...)
}

func (p *portEnv) cmdInfo(q string, all bool) error {
	s, err := p.state(true)
	if err != nil {
		return err
	}
	x, _ := s.findInstalled(q)
	var c *hmCat
	if x != nil {
		for i := range s.Catalogue {
			if s.Catalogue[i].Name == x.Name {
				c = &s.Catalogue[i]
			}
		}
	} else if c, err = s.findCatalogue(q, all); err != nil {
		return err
	}
	kv := func(k, v string) {
		if v != "" {
			p.say("%-13s %s", k+":", v)
		}
	}
	name, title := "", ""
	if x != nil {
		name, title = x.Name, x.Title
	} else {
		name, title = c.Name, c.Title
	}
	kv("Name", stem(name))
	kv("Title", title)
	if c != nil {
		kv("Porters", strings.Join(c.Porters, ", "))
		kv("Genres", strings.Join(c.Genres, ", "))
		kv("Game files", map[bool]string{true: "not needed (ready to run)", false: "needed (" + c.Availability + ")"}[c.RTR])
		kv("Source", c.Prefix)
		kv("Updated", c.DateUpdated)
		kv("Added", c.DateAdded)
		kv("Size", mb(c.Size))
		kv("Archive", c.URL)
		kv("Arch", strings.Join(c.Arch, " "))
		if !c.Compatible {
			kv("Device", "not supported by this device (--force to install anyway)")
		}
		if c.Exp {
			kv("Experimental", "yes")
		}
	}
	runtimes := []string{}
	if x != nil {
		runtimes = x.Runtimes
	} else if c != nil {
		runtimes = c.Runtimes
	}
	for _, r := range runtimes {
		st := s.runtime(r)
		if st == "" {
			st = "unknown"
		}
		kv("Runtime", runtimeFile(r)+" ("+strings.ToLower(st)+")")
	}
	if c != nil {
		kv("Description", c.Desc)
		kv("Instructions", c.Inst)
	}
	if x == nil {
		kv("Installed", "no")
		return nil
	}
	st := p.statusOf(x, p.loadRecs())
	kv("Installed", st.have)
	kv("Status", st.readable)
	for _, f := range x.Files {
		where := filepath.Join(p.ports, strings.TrimSuffix(f, "/"))
		if strings.HasSuffix(f, ".sh") {
			kv("Launcher", f+" ("+map[string]string{"ok": "in the menu", "missing": "not in the menu: port sync",
				"differs": "menu copy differs: port sync", "no source": "missing"}[p.launcherState(f)]+")")
		} else {
			kv("Folder", p.rel(where))
		}
	}
	if p.heldPort(x) {
		kv("Held", "yes (port unhold "+stem(x.Name)+")")
	}
	return nil
}

// --- install, upgrade ------------------------------------------------------

// installOne downloads and installs a port through harbourmaster. A reinstall
// first saves every file the archive replaces with different content.
func (p *portEnv) installOne(name, title, date string, size int64, reinstall bool) error {
	if size > 0 {
		if free, err := freeBytes(p.rom); err == nil && free < uint64(size)*2+64<<20 {
			return fmt.Errorf("not enough space on the SD card (%d MB needed)", (size*2+64<<20)>>20)
		}
	}
	args := []string{"install", name}
	backup := ""
	if reinstall {
		backup = filepath.Join(p.backupDir(), stamp()+"-"+stem(name))
		args = append(args, "--backup", backup)
	}
	var r struct {
		BackedUp int      `json:"backed_up"`
		MD5      string   `json:"md5"`
		Date     string   `json:"date"`
		Missing  []string `json:"missing_runtimes"`
	}
	err := p.hm(&r, true, args...)
	if err != nil {
		p.log("%s: install FAILED: %v", stem(name), err)
		return err
	}
	if r.Date == "" {
		r.Date = date
	}
	p.record(name, r.Date, r.MD5)
	verb := "installed"
	if reinstall {
		verb = "updated"
	}
	p.log("%s %s %s (md5 %s, backup: %d file(s))", stem(name), r.Date, verb, r.MD5, r.BackedUp)
	p.say("%s %s %s (md5 ok)", title, r.Date, verb)
	if backup != "" {
		if r.BackedUp > 0 {
			p.say("  %d replaced file(s) saved in %s", r.BackedUp, p.rel(backup))
			p.pruneBackups(stem(name))
		} else {
			os.RemoveAll(backup)
		}
	}
	for _, m := range r.Missing {
		p.warn("runtime %s not installed: port runtimes install %s", m, m)
	}
	return nil
}

// portDirs: the folders of a port, for the progressor scan.
func (p *portEnv) portDirs(items []string) []string {
	var d []string
	for _, it := range items {
		if !strings.HasSuffix(it, ".sh") {
			if f := filepath.Join(p.ports, strings.TrimSuffix(it, "/")); isDir(f) {
				d = append(d, f)
			}
		}
	}
	return d
}

func (p *portEnv) cmdInstall(names []string, force bool) error {
	s, err := p.state(true)
	if err != nil {
		return err
	}
	var dirs []string
	changed := false
	defer func() {
		if changed {
			p.finish(append([]string{}, dirs...)) // only the new ports' folders
		}
	}()
	for _, q := range names {
		c, err := s.findCatalogue(q, force)
		if err != nil {
			if x, ierr := s.findInstalled(q); ierr == nil {
				return fmt.Errorf("%s is installed but in no PortMaster source: nothing to download", x.Title)
			}
			return err
		}
		if !c.Compatible && !force {
			return fmt.Errorf("%s does not support this device (%s; --force to install anyway)", c.Title, strings.Join(c.Arch, " "))
		}
		x, _ := s.findInstalled(c.Name)
		if x != nil && !force {
			p.say("%s is already installed (port upgrade %s, or --force to reinstall)", c.Title, stem(c.Name))
			continue
		}
		if err := p.installOne(c.Name, c.Title, c.DateUpdated, c.Size, x != nil); err != nil {
			return fmt.Errorf("%s: %v", c.Title, err)
		}
		changed = true
		dirs = append(dirs, p.portDirs(c.Items)...)
		if !c.RTR && c.Inst != "" {
			p.say("  game files needed: %s", c.Inst)
		}
	}
	return nil
}

func (p *portEnv) cmdUpgrade(names []string, dry bool) error {
	s, err := p.state(false)
	if err != nil {
		return err
	}
	recs := p.loadRecs()
	var todo []*hmPort
	if len(names) == 0 {
		for i := range s.Installed {
			x := &s.Installed[i]
			switch st := p.statusOf(x, recs); st.code {
			case "update":
				todo = append(todo, x)
			case "held":
				p.say("%s %s -> %s: held (port unhold %s)", x.Title, st.have, st.avail, stem(x.Name))
			}
		}
	} else {
		for _, q := range names {
			x, err := s.findInstalled(q)
			if err != nil {
				return err
			}
			switch st := p.statusOf(x, recs); st.code {
			case "update", "held": // naming a held port upgrades it
				todo = append(todo, x)
			case "ok":
				p.say("%s %s is up to date", x.Title, st.have)
			default:
				return fmt.Errorf("%s: %s (port install --force %s reinstalls it)", x.Title, st.readable, stem(x.Name))
			}
		}
	}
	if len(todo) == 0 {
		if len(names) == 0 {
			p.say("All ports are up to date.")
		}
		return nil
	}
	var dirs []string
	failed := 0
	for _, x := range todo {
		st := p.statusOf(x, recs)
		p.say("%s %s -> %s", x.Title, st.have, st.avail)
		if dry {
			continue
		}
		if err := p.installOne(x.Name, x.Title, st.avail, x.Src.Size, true); err != nil {
			fmt.Fprintf(p.errw, "error: %s: %v\n", x.Title, err)
			failed++
			continue
		}
		dirs = append(dirs, p.portDirs(x.Files)...)
	}
	if !dry {
		p.finish(append([]string{}, dirs...)) // only the upgraded ports' folders
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d update(s) failed", failed, len(todo))
	}
	return nil
}

// --- uninstall, trash, restore -------------------------------------------------

func (p *portEnv) cmdUninstall(names []string, purge bool) error {
	s, err := p.state(false)
	if err != nil {
		return err
	}
	for _, q := range names {
		x, err := s.findInstalled(q)
		if err != nil {
			return err
		}
		if err := p.uninstall(x, purge); err != nil {
			return fmt.Errorf("%s: %v", x.Title, err)
		}
	}
	p.publish()
	return nil
}

// uninstall removes what only this port owns, menu launcher and artwork first
// (the entry disappears even if a later step fails).
func (p *portEnv) uninstall(x *hmPort, purge bool) error {
	t := &trashEntry{Name: x.Name, Title: x.Title, Removed: stamp(),
		dir: filepath.Join(p.trashDir(), stamp()+"-"+stem(x.Name))}
	var pairs, keep []move // keep: not PortMaster's own files, never deleted
	add := func(from, sub string, own bool) {
		if !exists(from) {
			return
		}
		m := move{from, filepath.Join(t.dir, sub, filepath.Base(from))}
		if own || !purge {
			pairs = append(pairs, m)
		} else {
			keep = append(keep, m)
		}
	}
	var dirs []string
	for _, it := range x.Exclusive {
		if !strings.HasSuffix(it, ".sh") {
			dirs = append(dirs, filepath.Join(p.ports, strings.TrimSuffix(it, "/")))
		}
	}
	for _, it := range x.Exclusive {
		if !strings.HasSuffix(it, ".sh") {
			continue
		}
		add(filepath.Join(p.rom, it), "menu", p.launcherState(it) == "ok")
		img := filepath.Join(p.media, strings.TrimSuffix(it, ".sh")+".png")
		own := false
		for _, d := range dirs {
			own = own || ownArtwork(img, d)
		}
		if own { // an older image of the same name stays where it is
			add(img, "media", true)
		}
	}
	for _, it := range x.Exclusive {
		add(filepath.Join(p.ports, strings.TrimSuffix(it, "/")), "ports", true)
	}
	if len(pairs)+len(keep) == 0 {
		p.warn("%s: nothing left on the card", x.Title)
	}
	if purge {
		if err := t.moveAll(keep); err != nil {
			return err
		}
		if len(t.Moves) > 0 {
			if err := t.save(); err != nil {
				return err
			}
			p.say("  %d changed file(s) kept in the trash (%s)", len(t.Moves), p.rel(t.dir))
		}
		for _, m := range pairs {
			if err := os.RemoveAll(m.From); err != nil {
				return err
			}
		}
		p.record(x.Name, "", "")
		p.log("%s uninstalled (purged)", stem(x.Name))
		p.say("%s uninstalled (files deleted)", x.Title)
		return nil
	}
	err := t.moveAll(pairs)
	if len(t.Moves) > 0 {
		if serr := t.save(); serr != nil && err == nil {
			err = serr
		}
	}
	if err != nil {
		p.log("%s uninstall stopped: %v", stem(x.Name), err)
		return fmt.Errorf("%v (what was moved is in %s: port restore %s)", err, p.rel(t.dir), stem(x.Name))
	}
	p.record(x.Name, "", "")
	p.log("%s uninstalled, kept in %s", stem(x.Name), p.rel(t.dir))
	p.say("%s uninstalled, kept in the trash (%s): port restore %s, port trash --empty",
		x.Title, mb(dirSize(t.dir)), stem(x.Name))
	return nil
}

func (p *portEnv) cmdTrash(empty bool) error {
	ents := p.trashEntries()
	if len(ents) == 0 {
		p.say("The trash is empty.")
		return nil
	}
	if empty {
		var total int64
		for _, t := range ents {
			total += dirSize(t.dir)
			if err := os.RemoveAll(t.dir); err != nil {
				return err
			}
			p.log("trash: %s deleted", stem(t.Name))
		}
		p.say("%d port(s) deleted, %s freed", len(ents), mb(total))
		return nil
	}
	rows := [][]string{{"NAME", "TITLE", "REMOVED", "SIZE"}}
	for _, t := range ents {
		rows = append(rows, []string{stem(t.Name), t.Title, t.Removed, mb(dirSize(t.dir))})
	}
	p.table(rows)
	return nil
}

func (p *portEnv) cmdRestore(q string) error {
	for _, t := range p.trashEntries() { // newest first
		if !matchKeys(q, portKeys(t.Name, t.Title, nil)) {
			continue
		}
		for _, m := range t.Moves {
			if exists(m.From) {
				return fmt.Errorf("%s exists again: %s is not restored (it stays in %s)", p.rel(m.From), t.Title, p.rel(t.dir))
			}
		}
		for i := len(t.Moves) - 1; i >= 0; i-- { // folders first, menu entry last
			m := t.Moves[i]
			if err := os.MkdirAll(filepath.Dir(m.From), 0o755); err != nil {
				return err
			}
			if err := os.Rename(m.To, m.From); err != nil {
				t.Moves = t.Moves[:i+1]
				t.save()
				return fmt.Errorf("restore %s: %v", p.rel(m.From), err)
			}
		}
		os.RemoveAll(t.dir)
		p.log("%s restored", stem(t.Name))
		p.say("%s restored", t.Title)
		p.finish([]string{})
		return nil
	}
	return fmt.Errorf("%q is not in the trash (port trash)", q)
}

// --- runtimes -------------------------------------------------------------------

func (p *portEnv) cmdRuntimes(all bool) error {
	s, err := p.state(false)
	if err != nil {
		return err
	}
	used := map[string][]string{}
	for _, x := range s.Installed {
		for _, r := range x.Runtimes {
			used[runtimeFile(r)] = append(used[runtimeFile(r)], x.Title)
		}
	}
	rows := [][]string{{"RUNTIME", "STATUS", "PREPARED", "USED BY"}}
	for _, r := range s.Runtimes {
		if !all && r.Status == "Not Installed" && used[r.Name] == nil {
			continue
		}
		prep := ""
		if f := filepath.Join(p.libsDir(), r.Name); exists(f) {
			prep = map[bool]string{true: "no (port sync)", false: "yes"}[needsProcessing(f)]
		}
		rows = append(rows, []string{r.Name, strings.ToLower(r.Status), prep, strings.Join(used[r.Name], ", ")})
	}
	if s.RuntimesError != "" {
		p.warn("runtimes: %s", s.RuntimesError)
	}
	p.table(rows)
	return nil
}

func (p *portEnv) cmdRuntimeInstall(name string) error {
	name = runtimeFile(name)
	if err := p.hm(nil, true, "runtime", name); err != nil {
		return err
	}
	p.log("runtime %s installed", name)
	p.finish([]string{})
	p.say("runtime %s ready", name)
	return nil
}
