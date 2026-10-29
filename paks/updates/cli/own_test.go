package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ownRelease publishes a pak in our own catalogue (url + sha256, no GitHub API).
// sum: "" = correct, "bad" = wrong, "none" = missing.
func (f *fixture) ownRelease(id, name, version string, members map[string]string, sum string) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, body := range members {
		w, _ := zw.Create(n)
		w.Write([]byte(body))
	}
	zw.Close()
	file := filepath.Join(f.root, "own", name+"-"+version+".zip")
	os.MkdirAll(filepath.Dir(file), 0o755)
	os.WriteFile(file, buf.Bytes(), 0o644)
	h := sha256.Sum256(buf.Bytes())
	s := hex.EncodeToString(h[:])
	switch sum {
	case "bad":
		s = strings.Repeat("0", 64)
	case "none":
		s = ""
	}
	entry := map[string]any{"id": id, "name": name, "version": version, "type": "TOOL",
		"repo_url": "https://github.com/me/mods", "release_filename": name + ".pak.zip",
		"url": "file://" + file, "sha256": s, "size": buf.Len(), "platforms": []any{"h700"}}
	js, _ := json.Marshal(map[string]any{"name": "own", "paks": []any{entry}})
	os.WriteFile(filepath.Join(f.root, "own.json"), js, 0o644)
	os.MkdirAll(f.e.home, 0o755)
	os.WriteFile(filepath.Join(f.e.home, "sources"), []byte("# ours\nfile://"+filepath.Join(f.root, "own.json")+"\n"), 0o644)
}

func TestOwnCatalogue(t *testing.T) {
	f := newFixture(t)
	f.release(pakEntry("S1", "Store", "TOOL", "1.0.0", "Store.pak.zip", map[string]any{
		"repo_url": "file://" + f.root + "/repo/o/s1"}), map[string]string{"launch.sh": "s"}, "")

	// A pak the Mac installed (same folder name) is adopted, then upgraded.
	os.MkdirAll(f.sd("Tools/h700/Music.pak"), 0o755)
	os.WriteFile(f.sd("Tools/h700/Music.pak/pak.json"), []byte(`{"version":"1.0.0"}`), 0o644)
	os.WriteFile(f.sd("Tools/h700/Music.pak/settings.conf"), []byte("mine"), 0o644)
	f.ownRelease("music", "Music", "1.1.0", map[string]string{"launch.sh": "v2", "pak.json": `{"version":"1.1.0"}`}, "")
	if rc := f.run("update"); rc != 0 {
		t.Fatalf("update: %s", f.out)
	}
	f.run("list", "--upgradable", "--porcelain")
	if !strings.Contains(f.out.String(), "music\tMusic\tMusic\t1.0.0\t1.1.0") {
		t.Fatalf("own pak not upgradable: %q", f.out)
	}
	if rc := f.run("upgrade"); rc != 0 || !strings.Contains(f.out.String(), "sha256 ok") {
		t.Fatalf("upgrade: %s", f.out)
	}
	if f.read("Tools/h700/Music.pak/launch.sh") != "v2" || f.read("Tools/h700/Music.pak/settings.conf") != "mine" {
		t.Fatal("upgrade result wrong (files or settings)")
	}

	// A Pak Store entry on the same folder is taken over by ours.
	reg := f.e.loadRegistry()
	reg.put(&installed{ID: "ST0RE", Name: "Music", Type: "TOOL", Version: "0.4.0", Source: "pak-store"})
	f.e.saveRegistry(reg)
	if reg := f.e.loadRegistry(); reg.byID("ST0RE") != nil || reg.byID("music") == nil {
		t.Fatalf("store entry not replaced: %+v", reg.paks)
	}
	reg = f.e.loadRegistry() // store entry alone (ours never recorded): adopted in its place
	reg.paks = []*installed{{ID: "ST0RE", Name: "Music", Type: "TOOL", Version: "0.4.0", Source: "pak-store"}}
	f.e.saveRegistry(reg)
	if reg := f.e.loadRegistry(); len(reg.paks) != 1 || reg.byID("music") == nil {
		t.Fatalf("store entry not taken over: %+v", reg.paks)
	}

	// Ours are never installed unverified, strict or not.
	for _, sum := range []string{"bad", "none"} {
		f.ownRelease("music", "Music", "1.2.0", map[string]string{"launch.sh": "evil"}, sum)
		f.run("update")
		if rc := f.run("upgrade", "Music"); rc == 0 {
			t.Fatalf("%s checksum accepted: %s", sum, f.out)
		}
		if f.read("Tools/h700/Music.pak/launch.sh") != "v2" {
			t.Fatalf("%s checksum: archive written", sum)
		}
	}

	// Our catalogue down: the Pak Store still works, ours come from the cache.
	os.Remove(filepath.Join(f.root, "own.json"))
	if rc := f.run("update"); rc != 0 || !strings.Contains(f.out.String(), "using the cached copy") {
		t.Fatalf("own catalogue down: %d %s", rc, f.out)
	}
	if c, _ := f.e.cachedCatalog(); c == nil || len(c.paks) != 2 {
		t.Fatalf("cached catalogue: %+v", c)
	}
}

func TestSelfUpdateRollback(t *testing.T) {
	f := newFixture(t)
	f.release(pakEntry("S1", "Store", "TOOL", "1.0.0", "Store.pak.zip", map[string]any{
		"repo_url": "file://" + f.root + "/repo/o/s1"}), map[string]string{"launch.sh": "s"}, "")
	dir := f.sd("Tools/h700/Updates.pak")
	os.MkdirAll(dir, 0o755)
	good := "#!/bin/sh\necho pak 1.0.0\n"
	os.WriteFile(filepath.Join(dir, "pak"), []byte(good), 0o755)
	os.WriteFile(filepath.Join(dir, "pak.json"), []byte(`{"version":"1.0.0"}`), 0o644)
	f.e.exe = filepath.Join(dir, "pak")

	// A new version whose program does not start: refused, old one back.
	f.ownRelease("updates", "Updates", "1.1.0", map[string]string{"pak": "#!/bin/sh\nexit 3\n", "pak.json": `{"version":"1.1.0"}`}, "")
	f.run("update")
	if rc := f.run("upgrade", "Updates"); rc == 0 || !strings.Contains(f.out.String(), "previous version put back") {
		t.Fatalf("broken self-update accepted: %s", f.out)
	}
	if f.read("Tools/h700/Updates.pak/pak") != good {
		t.Fatal("previous program not restored")
	}

	// A good new version: installed, the copy stays for a manual rollback.
	next := "#!/bin/sh\necho pak 1.2.0\n"
	f.ownRelease("updates", "Updates", "1.2.0", map[string]string{"pak": next, "pak.json": `{"version":"1.2.0"}`}, "")
	f.run("update")
	if rc := f.run("upgrade", "Updates"); rc != 0 {
		t.Fatalf("self-update: %s", f.out)
	}
	if f.read("Tools/h700/Updates.pak/pak") != next || !exists(f.e.previousSelf()) {
		t.Fatal("self-update result wrong")
	}
}
