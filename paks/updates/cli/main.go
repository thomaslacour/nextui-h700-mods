// pak — command-line package manager for NextUI paks (Pak Store catalogue).
//
// Linux ARM64, Go stdlib only. Used over adb, ssh or the Terminal pak, and by
// the Updates pak (Tools > Updates), which replaces the hidden Pak Store.
// See README.md (section « Updates » and « Commande pak »).
package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const version = "1.2.3"

const usage = `pak — NextUI pak manager (Pak Store catalogue)

  pak update                    download the catalogue
  pak list [--available|--upgradable] [--all] [--porcelain]
                                installed paks (default), catalogue, updates
  pak search [--all] word...    search the catalogue (name, description, author, category)
  pak upgrade [-n] [name...]    update every upgradable pak, or the named ones
  pak install [--force] name    install a pak (or reinstall it)
  pak uninstall [--force] name  remove an installed pak
  pak info name                 details of a pak
  pak hold [name...]            never upgrade these paks (no name: list)
  pak unhold name...
  pak hide [name...]            keep these Tools hidden (no name: apply the list)
  pak unhide name...            show a hidden Tool again
  pak doctor                    check paks, rights, catalogue and network
  pak verify file sig key       Ed25519 signature check (key: base64), for scripts
  pak version

A name is a pak name (Tools/Emus folder), its store name or its catalogue id.
More catalogues (same format, entries with url and sha256): one URL per line
in <userdata>/pak/sources, optionally followed by ed25519:<base64 key> (then
<url>.sig must be a valid signature); their entries win over the Pak Store's.
Environment: SDCARD_PATH, PLATFORM, USERDATA_PATH, PAK_STOREFRONT (catalogue
URL), PAK_STRICT=1 (refuse archives without a published SHA-256).
`

// The catalogue is refreshed automatically when older than this.
const maxAge = time.Hour

func main() {
	if mode(os.Args[0]) == "port" {
		os.Exit(runPort(newPortEnv(), os.Args[1:]))
	}
	os.Exit(run(newEnv(), os.Args[1:]))
}

func run(e *env, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.errw, usage)
		return 2
	}
	cmd := args[0]
	flags, names := parseArgs(args[1:])
	force := flags["force"] || flags["f"]
	var err error
	switch cmd {
	case "update":
		err = e.cmdUpdate()
	case "list", "ls":
		err = e.cmdList(flags, nil)
	case "search", "find":
		if len(names) == 0 {
			return usageErr(e, "search: what?")
		}
		err = e.cmdList(flags, names)
	case "upgrade":
		err = e.mutate(func() error { return e.cmdUpgrade(names, flags["n"] || flags["dry-run"]) })
	case "install":
		if len(names) == 0 {
			return usageErr(e, "install: which pak?")
		}
		err = e.mutate(func() error { return e.cmdInstall(names, force) })
	case "uninstall", "remove":
		if len(names) == 0 {
			return usageErr(e, "uninstall: which pak?")
		}
		err = e.mutate(func() error { return e.cmdUninstall(names, force) })
	case "info", "show":
		if len(names) != 1 {
			return usageErr(e, "info: one pak name")
		}
		err = e.cmdInfo(names[0])
	case "hold", "unhold":
		if cmd == "unhold" && len(names) == 0 {
			return usageErr(e, "unhold: which pak?")
		}
		err = e.cmdHold(cmd == "hold", names)
	case "hide", "unhide":
		if cmd == "unhide" && len(names) == 0 {
			return usageErr(e, "unhide: which Tool?")
		}
		err = e.mutate(func() error { return e.cmdHide(cmd == "hide", names) })
	case "doctor":
		if !e.cmdDoctor() {
			return 1
		}
	case "version", "--version":
		e.say("pak %s", version)
	case "verify":
		if len(names) != 3 {
			return usageErr(e, "verify: file sig key")
		}
		data, err1 := os.ReadFile(names[0])
		sig, err2 := os.ReadFile(names[1])
		if err1 != nil || err2 != nil {
			fmt.Fprintln(e.errw, "error: cannot read the file or its signature")
			return 1
		}
		if err := verifySig(parseKey(names[2]), data, sig); err != nil {
			fmt.Fprintln(e.errw, "error:", err)
			return 1
		}
		e.say("signature ok")
	case "help", "-h", "--help":
		fmt.Fprint(e.out, usage)
	default:
		return usageErr(e, "unknown command: "+cmd)
	}
	if err != nil {
		fmt.Fprintf(e.errw, "error: %v\n", err)
		return 1
	}
	return 0
}

func usageErr(e *env, msg string) int {
	fmt.Fprintf(e.errw, "%s\n\n%s", msg, usage)
	return 2
}

func (e *env) mutate(fn func() error) error {
	unlock, err := e.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func (e *env) cmdUpdate() error {
	c, err := e.fetchCatalog()
	if err != nil {
		return err
	}
	n := 0
	for _, p := range c.paks {
		if !p.Disabled && !p.Experimental && p.supports(e.platform) {
			n++
		}
	}
	e.say("catalogue: %d paks, %d for %s", len(c.paks), n, e.platform)
	up, _ := e.upgradable(c, e.loadRegistry())
	if len(up) > 0 {
		e.say("%d update(s) available: pak list --upgradable", len(up))
	}
	return nil
}

// --- installed paks and their status ---------------------------------------

type status struct {
	x        *installed
	p        catPak
	inCat    bool
	have     string // installed version
	dir      string
	hidden   bool
	present  bool
	code     string // ok update held unknown platform missing
	readable string
}

func (e *env) statusOf(c *catalog, x *installed) status {
	s := status{x: x, have: e.version(x)}
	s.dir, s.hidden, s.present = e.pakPath(x.Type, x.Name)
	if c != nil {
		s.p, s.inCat = c.entry(x)
	}
	switch {
	case !s.present:
		s.code, s.readable = "missing", "folder missing"
	case !s.inCat:
		s.code, s.readable = "unknown", "not in catalogue"
	case s.p.Disabled:
		s.code, s.readable = "unknown", "withdrawn from catalogue"
	case !s.p.supports(e.platform):
		s.code, s.readable = "platform", "not for "+e.platform
	case cmpVersion(s.p.Version, s.have) > 0 && e.held(x, s.p):
		s.code, s.readable = "held", "held"
	case cmpVersion(s.p.Version, s.have) > 0:
		s.code, s.readable = "update", "update available"
	default:
		s.code, s.readable = "ok", "up to date"
	}
	return s
}

func (e *env) upgradable(c *catalog, reg *registry) ([]status, []status) {
	var up, held []status
	for _, x := range reg.paks {
		s := e.statusOf(c, x)
		switch s.code {
		case "update":
			up = append(up, s)
		case "held":
			held = append(held, s)
		}
	}
	return up, held
}

// cmdList lists installed paks, or the catalogue (--available, or words to
// search for).
func (e *env) cmdList(flags map[string]bool, words []string) error {
	porcelain := flags["porcelain"] || flags["p"]
	if flags["available"] || words != nil {
		c, err := e.catalog(0)
		if err != nil {
			return err
		}
		reg := e.loadRegistry()
		var ps []catPak
		for _, p := range c.paks {
			if p.Disabled || (!flags["all"] && (p.Experimental || !p.supports(e.platform))) {
				continue
			}
			if !matchAll(words, append([]string{p.ID, p.Name, p.title(), p.Description, p.Author}, p.Categories...)...) {
				continue
			}
			ps = append(ps, p)
		}
		sortPaks(ps)
		rows := [][]string{{"ID", "NAME", "VERSION", "TYPE", "INSTALLED", "STORE NAME"}}
		for _, p := range ps {
			inst := ""
			if x := reg.byID(p.ID); x != nil {
				inst = e.version(x)
			}
			if porcelain {
				e.say("%s\t%s\t%s\t%s\t%s\t%s", p.ID, p.Name, p.Version, p.Type, inst, p.title())
				continue
			}
			rows = append(rows, []string{p.ID, p.Name, p.Version, p.Type, inst, p.title()})
		}
		if !porcelain {
			if len(rows) == 1 {
				e.say("No pak found%s.", map[bool]string{false: " (--all: other devices too)"}[flags["all"]])
				return nil
			}
			e.table(rows)
		}
		return nil
	}
	c, err := e.catalog(0)
	if err != nil {
		e.warn("%v", err)
		c = nil
	}
	reg := e.loadRegistry()
	rows := [][]string{{"NAME", "INSTALLED", "AVAILABLE", "STATUS"}}
	for _, x := range reg.paks {
		s := e.statusOf(c, x)
		if flags["upgradable"] && s.code != "update" && !(flags["all"] && s.code == "held") {
			continue
		}
		avail := ""
		if s.inCat {
			avail = s.p.Version
		}
		hid := "0"
		readable := s.readable
		if s.hidden {
			hid = "1"
			readable += " (hidden)"
		}
		if porcelain {
			// id, name, title, installed, available, status, hidden
			e.say("%s\t%s\t%s\t%s\t%s\t%s\t%s", x.ID, x.Name, x.DisplayName, s.have, avail, s.code, hid)
			continue
		}
		rows = append(rows, []string{x.Name, s.have, avail, readable})
	}
	if !porcelain {
		if len(rows) == 1 && flags["upgradable"] {
			e.say("All paks are up to date.")
			return nil
		}
		e.table(rows)
	}
	return nil
}

// --- changes ------------------------------------------------------------------

func (e *env) cmdUpgrade(names []string, dry bool) error {
	c, err := e.catalog(maxAge)
	if err != nil {
		return err
	}
	reg := e.loadRegistry()
	var todo []status
	if len(names) == 0 {
		var held []status
		todo, held = e.upgradable(c, reg)
		for _, s := range held {
			e.say("%s %s -> %s: held (pak unhold %s)", s.x.Name, s.have, s.p.Version, s.x.Name)
		}
	} else {
		for _, n := range names {
			x, err := e.findInstalled(c, reg, n)
			if err != nil {
				return err
			}
			s := e.statusOf(c, x)
			switch s.code {
			case "update", "held": // naming a held pak upgrades it
				todo = append(todo, s)
			case "ok":
				e.say("%s %s is up to date", x.Name, s.have)
			default:
				return fmt.Errorf("%s: %s", x.Name, s.readable)
			}
		}
	}
	if len(todo) == 0 {
		if len(names) == 0 {
			e.say("All paks are up to date.")
		}
		return nil
	}
	failed := 0
	for _, s := range todo {
		e.say("%s %s -> %s", s.p.title(), s.have, s.p.Version)
		if dry {
			continue
		}
		if err := e.install(s.p, reg, false); err != nil {
			fmt.Fprintf(e.errw, "error: %s: %v\n", s.p.title(), err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d update(s) failed", failed, len(todo))
	}
	return nil
}

func (e *env) cmdInstall(names []string, force bool) error {
	c, err := e.catalog(maxAge)
	if err != nil {
		return err
	}
	reg := e.loadRegistry()
	for _, n := range names {
		p, err := e.find(c, reg, n)
		if err != nil {
			return err
		}
		if err := e.install(p, reg, force); err != nil {
			return fmt.Errorf("%s: %v", p.title(), err)
		}
	}
	return nil
}

// findInstalled resolves a name among installed paks (name, store name, id),
// then through the catalogue.
func (e *env) findInstalled(c *catalog, reg *registry, q string) (*installed, error) {
	for _, x := range reg.paks {
		if x.ID == q || strings.EqualFold(x.Name, q) || strings.EqualFold(x.DisplayName, q) {
			return x, nil
		}
	}
	if c != nil {
		if p, err := e.find(c, reg, q); err == nil {
			if x := reg.byID(p.ID); x != nil {
				return x, nil
			}
		}
	}
	return nil, fmt.Errorf("%q is not installed from the catalogue (pak list)", q)
}

func (e *env) cmdUninstall(names []string, force bool) error {
	c, _ := e.catalog(0)
	reg := e.loadRegistry()
	for _, n := range names {
		x, err := e.findInstalled(c, reg, n)
		if err != nil {
			return err
		}
		if err := e.uninstall(x, c, reg, force); err != nil {
			return err
		}
	}
	return nil
}

func (e *env) cmdInfo(q string) error {
	c, cerr := e.catalog(0)
	reg := e.loadRegistry()
	var x *installed
	var p catPak
	inCat := false
	if c != nil {
		if pp, err := e.find(c, reg, q); err == nil {
			p, inCat = pp, true
			x = reg.byID(p.ID)
		}
	}
	if x == nil {
		x, _ = e.findInstalled(c, reg, q)
		if x != nil && c != nil {
			p, inCat = c.entry(x)
		}
	}
	if x == nil && !inCat {
		if cerr != nil {
			return cerr
		}
		return fmt.Errorf("%q: not installed and not in the catalogue", q)
	}
	kv := func(k, v string) {
		if v != "" {
			e.say("%-13s %s", k+":", v)
		}
	}
	if inCat {
		kv("Name", p.Name)
		kv("Store name", p.title())
		kv("Id", p.ID)
		kv("Type", p.Type)
		kv("Author", p.Author)
		kv("Repository", p.RepoURL)
		kv("Latest", p.Version)
		kv("Changes", p.changelogFor(p.Version))
		kv("Archive", p.downloadURL())
		kv("Platforms", strings.Join(p.Platforms, " "))
		kv("Categories", strings.Join(p.Categories, ", "))
		kv("Keeps", strings.Join(p.UpdateIgnore, " "))
		if p.Experimental {
			kv("Experimental", "yes")
		}
		kv("Description", p.Description)
	} else {
		kv("Name", x.Name)
		kv("Catalogue", "not in catalogue")
	}
	if x != nil {
		s := e.statusOf(c, x)
		kv("Installed", s.have)
		kv("Status", s.readable)
		kv("Folder", s.dir)
		if s.hidden {
			kv("Hidden", "yes (nextui-mods/hidden-tools)")
		}
		if e.held(x, p) {
			kv("Held", "yes (pak unhold "+x.Name+")")
		}
		kv("Source", x.Source)
	} else {
		kv("Installed", "no")
	}
	return nil
}

func (e *env) cmdHide(hide bool, names []string) error {
	if !hide {
		if _, err := editList(e.hiddenFile(), "", false, names...); err != nil {
			return err
		}
		for _, n := range names {
			p := e.tools + "/" + n + ".pak"
			if isDir(p+".disabled") && !isDir(p) {
				if err := os.Rename(p+".disabled", p); err != nil {
					return err
				}
				e.log("unhidden: %s", n)
				e.say("%s is visible again in Tools", n)
			}
		}
		return nil
	}
	if len(names) > 0 {
		if _, err := editList(e.hiddenFile(), "# Tools kept disabled after an update (one name per line)", true, names...); err != nil {
			return err
		}
	}
	hid, err := e.hideTools()
	for _, n := range hid {
		e.log("hidden: %s", n)
		e.say("%s hidden", n)
	}
	return err
}
