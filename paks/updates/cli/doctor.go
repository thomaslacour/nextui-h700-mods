package main

// pak doctor: installed paks, rights, hidden Tools, catalogue and network.

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tools that must stay hidden: their work goes through the Updates pak.
var mustHide = []string{"Updater", "Pak Store"}

func (e *env) cmdDoctor() bool {
	chk := &checker{e: e}
	ok, warn, bad := chk.ok, chk.warn, chk.bad

	// Rights: the SD card and our folders must be writable.
	chk.writable(e.home, e.tools)
	chk.basics()

	// Leftovers of interrupted writes.
	for _, root := range []string{e.tools, e.emus} {
		for _, pat := range []string{"*.pak.new", "*.pak.old"} {
			m, _ := filepath.Glob(filepath.Join(root, pat))
			for _, f := range m {
				warn("leftover of an interrupted install: %s", strings.TrimPrefix(f, e.sd+"/"))
			}
		}
	}
	if m, _ := filepath.Glob(filepath.Join(e.home, "cache", "*")); len(m) > 0 {
		warn("%d leftover download(s) in %s (removed by the next install)", len(m), filepath.Join(e.home, "cache"))
	}

	// Every pak folder needs a launch.sh to show up in NextUI.
	broken := 0
	for _, root := range []string{e.tools, e.emus} {
		m, _ := filepath.Glob(filepath.Join(root, "*.pak"))
		for _, d := range m {
			if isDir(d) && !exists(filepath.Join(d, "launch.sh")) {
				bad("broken pak (no launch.sh): %s", strings.TrimPrefix(d, e.sd+"/"))
				broken++
			}
		}
	}
	if broken == 0 {
		ok("every pak in Tools and Emus has a launch.sh")
	}

	// Hidden Tools: Updater and Pak Store go through Updates.
	hidden := readList(e.hiddenFile())
	for _, n := range mustHide {
		vis := isDir(filepath.Join(e.tools, n+".pak"))
		switch {
		case !contains(hidden, n):
			bad("%s is not in %s (pak hide %q)", n, e.hiddenFile(), n)
		case vis:
			bad("%s is visible in Tools (pak hide)", n)
		default:
			ok("%s hidden (kept hidden after NextUI updates)", n)
		}
	}
	for _, n := range hidden {
		if !contains(mustHide, n) && isDir(filepath.Join(e.tools, n+".pak")) {
			warn("%s is listed as hidden but visible (pak hide)", n)
		}
	}

	// Pak Store database (imported) and our registry.
	if _, err := os.Stat(e.storeDB()); err == nil {
		if db, err := openSQLite(e.storeDB()); err != nil {
			warn("Pak Store database unreadable: %v", err)
		} else if rows, err := db.tableRows("installed_paks"); err != nil {
			warn("Pak Store database: %v", err)
		} else {
			ok("Pak Store database: %d paks", len(rows))
		}
		if st, err := os.Stat(e.storeDB() + "-wal"); err == nil && st.Size() > 0 {
			warn("Pak Store database has pending changes (-wal): open the Pak Store once, or ignore")
		}
	}
	reg := e.loadRegistry()

	// Catalogue: reachable, and the cache.
	start := time.Now()
	c, err := e.fetchCatalog()
	if err != nil {
		bad("%v", err)
		if c, err = e.cachedCatalog(); err == nil {
			warn("using the cached catalogue from %s", c.fetched.Format("2006-01-02 15:04"))
		}
	} else {
		ok("catalogue reachable (%d paks, %.1f s)", len(c.paks), time.Since(start).Seconds())
	}
	chk.github("GitHub API reachable (archive checksums)", "archives cannot be checked against their SHA-256")

	// Installed paks: versions and status.
	for _, x := range reg.paks {
		s := e.statusOf(c, x)
		name := x.Name
		switch s.code {
		case "missing":
			warn("%s: registered but %s is missing (pak uninstall %q to forget it)", name, strings.TrimPrefix(s.dir, e.sd+"/"), name)
		case "update":
			warn("%s %s: update %s available", name, s.have, s.p.Version)
		case "held":
			ok("%s %s: held (%s available)", name, s.have, s.p.Version)
		case "ok":
			ok("%s %s: up to date", name, s.have)
		default:
			ok("%s %s: %s", name, s.have, s.readable)
		}
		if s.present {
			if d := diskVersion(s.dir); d != "" && cmpVersion(d, x.Version) != 0 {
				chk.note("%s: pak.json says %s, records say %s", name, d, x.Version)
			}
		}
	}
	return chk.done()
}
