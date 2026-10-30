package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMode(t *testing.T) {
	for arg0, want := range map[string]string{
		"/usr/bin/port": "port", "port": "port", "/x/Updates.pak/pak": "pak", "./pak": "pak",
		"/tmp/portable": "pak", "PORT.exe": "port",
	} {
		if got := mode(arg0); got != want {
			t.Errorf("mode(%q) = %s, want %s", arg0, got, want)
		}
	}
}

func TestParseArgs(t *testing.T) {
	f, n := parseArgs([]string{"--force", "a", "-n", "--", "-weird"})
	if !f["force"] || !f["n"] || len(n) != 2 || n[1] != "-weird" {
		t.Errorf("parseArgs: %v %v", f, n)
	}
}

func TestMatchKeys(t *testing.T) {
	keys := portKeys("voidstranger.zip", "Void Stranger", []string{"Void Stranger.sh", "voidstranger/"})
	for q, want := range map[string]bool{
		"voidstranger": true, "voidstranger.zip": true, "void stranger": true, "Void Stranger.sh": true,
		"void": false, "": false,
	} {
		if matchKeys(q, keys) != want {
			t.Errorf("matchKeys(%q) = %v", q, !want)
		}
	}
}

func TestFixScript(t *testing.T) {
	emu := "/mnt/SDCARD/Emus/h700/PORTS.pak/PortMaster"
	for _, c := range []struct{ in, out string }{
		{"#!/bin/bash\nsource /roms/ports/PortMaster/control.txt\n", "#!/usr/bin/env bash\nsource " + emu + "/control.txt\n"},
		{"#!/usr/bin/env bash\necho ok\n", ""},                                      // already fine
		{"#!/bin/sh\n. /roms/ports/PortMaster/x\n", "#!/bin/sh\n. " + emu + "/x\n"}, // path only
		{"ELF binary /roms/ports/PortMaster", ""},                                   // not a script
		{"#!/usr/bin/python3\n/roms/ports/PortMaster\n", ""},                        // not a shell script
	} {
		got, ok := fixScript([]byte(c.in), emu)
		if (c.out == "") == ok || (ok && string(got) != c.out) {
			t.Errorf("fixScript(%q) = %q, %v", c.in, got, ok)
		}
	}
}

func TestFixRuntimeScripts(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string, mode os.FileMode) {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), mode)
	}
	write("bin/run", "#!/bin/bash\necho\n", 0o755)       // no extension: scanned
	write("bin/run.sh", "#!/bin/bash\necho\n", 0o644)    // not executable: skipped
	write("bin/data.json", "#!/bin/bash\necho\n", 0o755) // other extension: skipped
	write("lib/start.txt", "#!/bin/bash\necho\n", 0o755) // .txt: scanned
	n, err := fixRuntimeScripts(root, "/emu")
	if err != nil || n != 2 {
		t.Fatalf("fixRuntimeScripts = %d, %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "bin/run.sh")); string(b) != "#!/bin/bash\necho\n" {
		t.Error("non-executable script changed")
	}
	if st, _ := os.Stat(filepath.Join(root, "bin/run")); st.Mode().Perm() != 0o755 {
		t.Error("mode lost")
	}
}

func TestNeedsProcessing(t *testing.T) {
	d := t.TempDir()
	sq := filepath.Join(d, "x.squashfs")
	os.WriteFile(sq, []byte("x"), 0o644)
	if !needsProcessing(sq) {
		t.Error("no marker: must process")
	}
	st, _ := os.Stat(sq)
	os.WriteFile(sq+".processed", nil, 0o644)
	os.Chtimes(sq+".processed", st.ModTime().Add(2e9), st.ModTime().Add(2e9))
	if needsProcessing(sq) {
		t.Error("newer marker: done")
	}
}
