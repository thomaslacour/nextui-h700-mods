package main

// port doctor: PortMaster engine, ports, menu launchers, runtimes, network.

import (
	"os"
	"path/filepath"
	"strings"
)

func (p *portEnv) cmdDoctor() bool {
	c := &checker{e: p.env}

	// Engine: PORTS.pak, its python and the bridge, PortMaster prepared.
	if !exists(filepath.Join(p.pakDir, "launch.sh")) {
		c.bad("PORTS.pak missing: %s", p.rel(p.pakDir))
		return c.done()
	}
	if exists(filepath.Join(p.emuDir, "pylibs.zip")) {
		c.bad("PortMaster was updated and is not prepared yet (pylibs.zip): start any port once")
	}
	c.writable(p.home, p.ports, p.libsDir())
	c.basics()
	if b := p.busy(); b != "" {
		c.note("PortMaster is running (%s): installs wait until it closes", b)
	} else if p.staleMount() {
		c.note("a .ports_temp mount was left by a PortMaster session (harmless, cleared at its next launch)")
	}
	if dev := firstLineOf(filepath.Join(p.shared, "PORTS-portmaster", ".config", ".DEVICE")); dev != "" {
		c.ok("PortMaster device profile: %s", dev)
	} else {
		c.warn("no PortMaster device profile yet (written at each port launch): start any port once")
	}
	s, err := p.state(false)
	if err != nil {
		c.bad("PortMaster engine: %v", err)
		return c.done()
	}
	c.ok("PortMaster engine: %s, %s", s.Device.Name, s.Device.Device)
	if !s.Patched {
		c.bad("PortMaster's H700 patches are not applied (start any port once)")
	}
	for _, src := range s.Sources {
		if src.Ports == 0 {
			c.bad("source %s is empty: port update", src.Name)
		} else {
			c.ok("source %s: %d ports", src.Name, src.Ports)
		}
	}
	if len(s.Sources) == 0 {
		c.bad("no port source (PortMaster config/*.source.json): start PortMaster once")
	}
	c.github("GitHub reachable (ports and runtimes downloads)", "installs and updates will fail")

	// Installed ports: files, menu entries, runtimes, updates.
	recs := p.loadRecs()
	needed := map[string]bool{}
	for i := range s.Installed {
		x := &s.Installed[i]
		st := p.statusOf(x, recs)
		problem := false
		if x.Broken {
			c.bad("%s: damaged port.json (port install --force %s)", x.Title, stem(x.Name))
			continue
		}
		for _, f := range x.Files {
			if strings.HasSuffix(f, ".sh") {
				switch p.launcherState(f) {
				case "no source":
					c.bad("%s: launcher %s missing from .ports (port install --force %s)", x.Title, f, stem(x.Name))
					problem = true
				case "missing":
					c.warn("%s: not in the menu (port sync)", x.Title)
					problem = true
				case "differs":
					c.warn("%s: the menu launcher differs from .ports/%s (copied over it at boot, or by port sync)", x.Title, f)
					problem = true
				}
			} else if !isDir(filepath.Join(p.ports, strings.TrimSuffix(f, "/"))) {
				c.bad("%s: folder %s missing", x.Title, f)
				problem = true
			}
		}
		for _, r := range x.Runtimes {
			r = runtimeFile(r)
			needed[r] = true
			switch rs := s.runtime(r); rs {
			case "Verified":
			case "", "Not Installed":
				c.bad("%s: runtime %s missing (port runtimes install %s)", x.Title, r, r)
				problem = true
			default:
				c.note("%s: runtime %s is %s (custom or prepared build)", x.Title, r, strings.ToLower(rs))
			}
		}
		switch st.code {
		case "update":
			c.warn("%s %s: update %s available (port upgrade %s)", x.Title, st.have, st.avail, stem(x.Name))
		case "held":
			c.ok("%s %s: held (%s available)", x.Title, st.have, st.avail)
		case "ok":
			if !problem {
				c.ok("%s %s: up to date", x.Title, st.have)
			}
		default:
			if !problem {
				c.ok("%s: %s", x.Title, st.readable)
			}
		}
	}
	for _, u := range s.Unknown {
		c.warn("%s in .ports is not managed by PortMaster (no port.json)", u)
	}
	if s.RuntimesError != "" {
		c.warn("runtimes: %s", s.RuntimesError)
	}
	for r := range needed {
		if f := filepath.Join(p.libsDir(), r); exists(f) && needsProcessing(f) {
			c.warn("runtime %s not prepared for NextUI yet (port sync)", r)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(p.libsDir(), "*.new")); len(m) > 0 {
		c.warn("%d leftover of an interrupted runtime rebuild in %s", len(m), p.rel(p.libsDir()))
	}

	for _, id := range readList(p.failedFile()) {
		if n := strings.Fields(id); len(n) == 3 && id == fileID(filepath.Join(p.libsDir(), n[0])) {
			c.note("runtime %s could not be prepared (damaged image?): not retried until it changes", n[0])
		}
	}

	// Menu entries without a port behind them (own launchers, or leftovers).
	inPorts := map[string]bool{}
	for _, x := range s.Installed {
		for _, f := range x.Files {
			inPorts[f] = true
		}
	}
	m, _ := filepath.Glob(filepath.Join(p.rom, "*.sh"))
	for _, f := range m {
		n := filepath.Base(f)
		if strings.HasPrefix(n, ".") { // hidden from the menu (.Portmaster.sh: the GUI trigger)
			continue
		}
		if !inPorts[n] && !exists(filepath.Join(p.ports, n)) {
			c.note("%s in the menu belongs to no port (own launcher?)", n)
		}
	}

	// Kept copies and the GUI.
	if t := p.trashEntries(); len(t) > 0 {
		c.note("%d removed port(s) in the trash, %s (port trash --empty)", len(t), mb(dirSize(p.trashDir())))
	}
	if n := dirSize(p.backupDir()); n > 0 {
		c.note("upgrade backups: %s in %s", mb(n), p.rel(p.backupDir()))
	}
	switch {
	case isDir(filepath.Join(p.tools, "PortMaster.pak")):
		c.note("the PortMaster GUI is visible in Tools (pak hide PortMaster hides it)")
	case isDir(filepath.Join(p.tools, "PortMaster.pak.disabled")):
		c.ok("PortMaster GUI hidden from Tools (pak unhide PortMaster shows it again)")
	}
	return c.done()
}

func firstLineOf(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	l, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(l)
}
