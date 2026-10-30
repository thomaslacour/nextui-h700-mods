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

func TestStoreDB(t *testing.T) {
	// 512-byte pages: interior pages, an overflowing row, and a row written
	// before ALTER TABLE ADD COLUMN (missing values are NULL).
	db, err := openSQLite("testdata/pak-store.db")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.tableRows("installed_paks")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 153 {
		t.Fatalf("rows = %d, want 153", len(rows))
	}
	byName := map[string]map[string]any{}
	for _, r := range rows {
		byName[r["name"].(string)] = r
	}
	if r := byName["Pak Store"]; r["pak_id"] != "xK9mR2vL4w" || r["can_uninstall"] != int64(0) || r["version"] != "v4.3.0" {
		t.Errorf("Pak Store row: %v", r)
	}
	if r := byName["Old"]; r["pak_id"] != nil || r["version"] != "0.1.0" {
		t.Errorf("old row: %v", r)
	}
	if r := byName["Long"]; len(r["display_name"].(string)) != 2000 || r["pak_id"] != "LONG" {
		t.Errorf("overflow row: %d bytes", len(r["display_name"].(string)))
	}
	if r := byName["Filler150"]; r["version"] != "1.150.0" {
		t.Errorf("last row: %v", r)
	}
}

func TestColumns(t *testing.T) {
	got := strings.Join(columns(`CREATE TABLE x ( "a" text not null, b int default (1+2), unique (a), c TEXT)`), ",")
	if got != "a,b,c" {
		t.Errorf("columns = %s", got)
	}
}

func TestCmpVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"v1.2.0", "1.2", 0}, {"0.9.1", "0.10.0", -1}, {"v4.3.0", "v4.2.9", 1},
		{"1.0.0.1", "1.0.0", 1}, {"V1.1.0", "v1.1.0", 0}, {"", "0.1", -1},
	} {
		if got := cmpVersion(c.a, c.b); got != c.want {
			t.Errorf("cmpVersion(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIgnoredAndPaths(t *testing.T) {
	pats := []string{"config.json", "drastic/config/drastic.cfg", "caps/", "PortMaster"}
	for p, want := range map[string]bool{
		"config.json": true, "bin/config.json": false, "drastic/config/drastic.cfg": true,
		"caps/a/b.txt": true, "PortMaster/x/y": true, "launch.sh": false,
	} {
		if ignored(p, pats) != want {
			t.Errorf("ignored(%q) = %v", p, !want)
		}
	}
	if innerPakRel("Emus/h700/PORTS.pak/PortMaster/a") != "PortMaster/a" {
		t.Error("innerPakRel")
	}
	for name, ok := range map[string]bool{"a/b": true, "../x": false, "/etc/x": false, "a/../../x": false, `a\b`: false} {
		if _, got := cleanRel(name); got != ok {
			t.Errorf("cleanRel(%q) = %v", name, got)
		}
	}
}

// --- end to end on a fake SD card, catalogue and releases in file:// -------

type fixture struct {
	t    *testing.T
	root string
	e    *env
	out  *bytes.Buffer
	sf   map[string]any
}

func newFixture(t *testing.T) *fixture {
	root := t.TempDir()
	sd := filepath.Join(root, "sd")
	for _, d := range []string{"Tools/h700", "Emus/h700", ".userdata/h700/nextui-mods"} {
		os.MkdirAll(filepath.Join(sd, d), 0o755)
	}
	t.Setenv("SDCARD_PATH", sd)
	t.Setenv("PLATFORM", "h700")
	t.Setenv("USERDATA_PATH", "")
	t.Setenv("LOGS_PATH", "")
	t.Setenv("PAK_STRICT", "")
	t.Setenv("PAK_STOREFRONT", "file://"+filepath.Join(root, "storefront.json"))
	t.Setenv("PAK_GITHUB_API", "file://"+filepath.Join(root, "api"))
	f := &fixture{t: t, root: root, out: &bytes.Buffer{}, sf: map[string]any{"name": "test", "paks": []any{}}}
	f.e = newEnv()
	f.e.out, f.e.errw = f.out, f.out
	return f
}

// release publishes a pak: archive under repo/releases/download/<v>/<file>,
// GitHub API answer with its digest (bad digest when digest == "bad",
// none when digest == "none"), and the catalogue entry.
func (f *fixture) release(entry map[string]any, members map[string]string, digest string) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range members {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	repo := entry["repo_url"].(string)
	file := entry["release_filename"].(string)
	v := entry["version"].(string)
	rel := strings.TrimPrefix(repo, "file://")
	p := filepath.Join(rel, "releases", "download", v, file)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, buf.Bytes(), 0o644)
	sum := sha256.Sum256(buf.Bytes())
	d := "sha256:" + hex.EncodeToString(sum[:])
	switch digest {
	case "bad":
		d = "sha256:" + strings.Repeat("0", 64)
	case "none":
		d = ""
	}
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	api := filepath.Join(f.root, "api", "repos", parts[len(parts)-2], parts[len(parts)-1], "releases", "tags", v)
	os.MkdirAll(filepath.Dir(api), 0o755)
	js, _ := json.Marshal(map[string]any{"assets": []any{map[string]any{"name": file, "size": buf.Len(), "digest": d}}})
	os.WriteFile(api, js, 0o644)
	var paks []any
	for _, x := range f.sf["paks"].([]any) {
		if x.(map[string]any)["id"] != entry["id"] {
			paks = append(paks, x)
		}
	}
	f.sf["paks"] = append(paks, entry)
	js, _ = json.Marshal(f.sf)
	os.WriteFile(filepath.Join(f.root, "storefront.json"), js, 0o644)
	if _, err := f.e.fetchCatalog(); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) run(args ...string) int {
	f.out.Reset()
	return run(f.e, args)
}

func (f *fixture) sd(p string) string { return filepath.Join(f.e.sd, p) }

func (f *fixture) read(p string) string {
	b, _ := os.ReadFile(f.sd(p))
	return string(b)
}

func pakEntry(id, name, typ, version, file string, extra map[string]any) map[string]any {
	m := map[string]any{"id": id, "storefront_name": name + " (store)", "name": name, "version": version,
		"type": typ, "repo_url": "file:///REPO/owner/" + strings.ToLower(id), "release_filename": file,
		"platforms": []any{"h700", "tg5040"}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestInstallUpgradeUninstall(t *testing.T) {
	f := newFixture(t)
	repo := func(m map[string]any) map[string]any {
		m["repo_url"] = strings.Replace(m["repo_url"].(string), "/REPO", f.root+"/repo", 1)
		return m
	}
	tool := repo(pakEntry("T1", "Tooly", "TOOL", "1.0.0", "Tooly.pak.zip", map[string]any{"update_ignore": []any{"config.json"}}))
	f.release(tool, map[string]string{"launch.sh": "v1", "config.json": "default", "bin/x": "1"}, "")
	if rc := f.run("install", "tooly"); rc != 0 {
		t.Fatalf("install: %d %s", rc, f.out)
	}
	if f.read("Tools/h700/Tooly.pak/launch.sh") != "v1" || !strings.Contains(f.out.String(), "sha256 ok") {
		t.Fatalf("install result: %s", f.out)
	}
	os.WriteFile(f.sd("Tools/h700/Tooly.pak/config.json"), []byte("mine"), 0o644)

	// New version: listed, upgraded, config.json kept (update_ignore).
	tool["version"] = "1.1.0"
	f.release(tool, map[string]string{"launch.sh": "v2", "config.json": "default", "bin/x": "2"}, "")
	f.run("update")
	f.run("list", "--upgradable", "--porcelain")
	if !strings.Contains(f.out.String(), "T1\tTooly\tTooly (store)\t1.0.0\t1.1.0\tupdate\t0") {
		t.Fatalf("upgradable: %q", f.out)
	}
	if rc := f.run("upgrade"); rc != 0 {
		t.Fatalf("upgrade: %s", f.out)
	}
	if f.read("Tools/h700/Tooly.pak/launch.sh") != "v2" || f.read("Tools/h700/Tooly.pak/config.json") != "mine" {
		t.Fatalf("upgrade result: launch=%q config=%q", f.read("Tools/h700/Tooly.pak/launch.sh"), f.read("Tools/h700/Tooly.pak/config.json"))
	}
	f.run("upgrade")
	if !strings.Contains(f.out.String(), "All paks are up to date") {
		t.Errorf("second upgrade: %s", f.out)
	}

	// Wrong checksum: refused, nothing changed.
	tool["version"] = "1.2.0"
	f.release(tool, map[string]string{"launch.sh": "v3"}, "bad")
	f.run("update")
	if rc := f.run("upgrade", "Tooly"); rc == 0 || !strings.Contains(f.out.String(), "wrong checksum") {
		t.Fatalf("bad digest accepted: %s", f.out)
	}
	if f.read("Tools/h700/Tooly.pak/launch.sh") != "v2" {
		t.Fatal("bad archive written")
	}
	// No published checksum: accepted with a warning, refused when strict.
	f.release(tool, map[string]string{"launch.sh": "v3"}, "none")
	f.run("update")
	f.e.strict = true
	if rc := f.run("upgrade"); rc == 0 {
		t.Fatalf("strict accepted an unverified archive: %s", f.out)
	}
	f.e.strict = false
	f.run("hold", "Tooly")
	f.run("upgrade")
	if !strings.Contains(f.out.String(), "held") || f.read("Tools/h700/Tooly.pak/launch.sh") != "v2" {
		t.Fatalf("held pak upgraded: %s", f.out)
	}
	f.run("unhold", "Tooly")
	if rc := f.run("upgrade"); rc != 0 || !strings.Contains(f.out.String(), "unverified") {
		t.Fatalf("unverified upgrade: %s", f.out)
	}

	// Zip slip: refused before anything is written.
	evil := repo(pakEntry("E1", "Evil", "TOOL", "1.0.0", "Evil.pak.zip", nil))
	f.release(evil, map[string]string{"launch.sh": "x", "../../escape": "x"}, "")
	if rc := f.run("install", "Evil"); rc == 0 || !strings.Contains(f.out.String(), "unsafe path") || exists(f.sd("Tools/h700/Evil.pak")) || exists(filepath.Join(f.e.sd, "..", "escape")) {
		t.Fatalf("zip slip: %s", f.out)
	}

	// Own pak with the same name: not replaced without --force.
	own := repo(pakEntry("M1", "Mine", "TOOL", "1.0.0", "Mine.pak.zip", nil))
	f.release(own, map[string]string{"Mine.pak/launch.sh": "store"}, "") // zipped folder
	os.MkdirAll(f.sd("Tools/h700/Mine.pak"), 0o755)
	os.WriteFile(f.sd("Tools/h700/Mine.pak/launch.sh"), []byte("own"), 0o644)
	if rc := f.run("install", "Mine"); rc == 0 || f.read("Tools/h700/Mine.pak/launch.sh") != "own" {
		t.Fatalf("own pak replaced: %s", f.out)
	}
	if rc := f.run("install", "--force", "Mine"); rc != 0 || f.read("Tools/h700/Mine.pak/launch.sh") != "store" {
		t.Fatalf("forced install (zipped folder stripped): %s", f.out)
	}

	// Other platform: refused.
	other := repo(pakEntry("O1", "Other", "TOOL", "1.0.0", "Other.pak.zip", map[string]any{"platforms": []any{"tg5040"}}))
	f.release(other, map[string]string{"launch.sh": "x"}, "")
	if rc := f.run("install", "Other"); rc == 0 || !strings.Contains(f.out.String(), "does not support h700") {
		t.Fatalf("other platform: %s", f.out)
	}

	// Uninstall.
	if rc := f.run("uninstall", "Tooly"); rc != 0 || exists(f.sd("Tools/h700/Tooly.pak")) {
		t.Fatalf("uninstall: %s", f.out)
	}
	f.run("list", "--porcelain")
	if strings.Contains(f.out.String(), "Tooly") {
		t.Fatalf("still listed: %s", f.out)
	}
}

func TestPakzHiddenAndStoreDB(t *testing.T) {
	f := newFixture(t)
	os.WriteFile(f.e.hiddenFile(), []byte("# hidden\nUpdater\nPak Store\n"), 0o644)
	// The Pak Store database knows the Updater and the Pak Store itself.
	os.MkdirAll(filepath.Dir(f.e.storeDB()), 0o755)
	db, _ := os.ReadFile("testdata/pak-store.db")
	os.WriteFile(f.e.storeDB(), db, 0o644)
	for _, d := range []string{"Tools/h700/Updater.pak.disabled", "Tools/h700/Pak Store.pak.disabled"} {
		os.MkdirAll(f.sd(d), 0o755)
		os.WriteFile(f.sd(d+"/launch.sh"), []byte("old"), 0o644)
		os.WriteFile(f.sd(d+"/settings.txt"), []byte("keep"), 0o644)
	}
	upd := pakEntry("U1", "Updater", "TOOL", "0.9.0", "nextui.updater.pakz", nil)
	upd["repo_url"] = "file://" + f.root + "/repo/o/updater"
	f.release(upd, map[string]string{
		"Tools/h700/Updater.pak/launch.sh":   "new",
		"Tools/tg5040/Updater.pak/launch.sh": "other device",
	}, "")
	store := pakEntry(pakStoreID, "Pak Store", "TOOL", "v4.4.0", "Pak.Store.pak.zip", nil)
	store["repo_url"] = "file://" + f.root + "/repo/o/store"
	f.release(store, map[string]string{"launch.sh": "store 4.4"}, "")

	// Imported from the Pak Store database: v4.3.0 -> update to v4.4.0.
	f.run("list", "--porcelain")
	if !strings.Contains(f.out.String(), pakStoreID+"\tPak Store\tPak Store\tv4.3.0\tv4.4.0\tupdate\t1") {
		t.Fatalf("store row not imported: %q", f.out)
	}
	if rc := f.run("upgrade", "Pak Store"); rc != 0 {
		t.Fatalf("upgrade store: %s", f.out)
	}
	if f.read("Tools/h700/Pak Store.pak.disabled/launch.sh") != "store 4.4" || exists(f.sd("Tools/h700/Pak Store.pak")) {
		t.Fatal("hidden Pak Store not updated in place")
	}
	if rc := f.run("uninstall", "Pak Store"); rc == 0 {
		t.Fatal("Pak Store uninstalled without --force")
	}
	if rc := f.run("install", "Updater"); rc != 0 {
		t.Fatalf("install pakz: %s", f.out)
	}
	if f.read("Tools/h700/Updater.pak.disabled/launch.sh") != "new" ||
		f.read("Tools/h700/Updater.pak.disabled/settings.txt") != "keep" ||
		exists(f.sd("Tools/h700/Updater.pak")) || exists(f.sd("Tools/tg5040")) {
		t.Fatal("pakz: hidden Updater not updated in place, or other device files written")
	}

	// A NextUI update brings both Tools back: pak hide (and the boot guard) hide them again.
	for _, d := range []string{"Tools/h700/Updater.pak", "Tools/h700/Pak Store.pak"} {
		os.MkdirAll(f.sd(d), 0o755)
		os.WriteFile(f.sd(d+"/launch.sh"), []byte("from NextUI"), 0o644)
	}
	if rc := f.run("doctor"); rc == 0 || !strings.Contains(f.out.String(), "Updater is visible") {
		t.Fatalf("doctor missed visible Updater: %s", f.out)
	}
	f.run("hide")
	if exists(f.sd("Tools/h700/Updater.pak")) || f.read("Tools/h700/Updater.pak.disabled/launch.sh") != "from NextUI" {
		t.Fatalf("not hidden again: %s", f.out)
	}
	f.run("unhide", "Updater")
	if !exists(f.sd("Tools/h700/Updater.pak")) || strings.Contains(f.read(".userdata/h700/nextui-mods/hidden-tools"), "Updater") {
		t.Fatal("unhide")
	}
	f.run("hide", "Updater")
	if exists(f.sd("Tools/h700/Updater.pak")) || !strings.Contains(f.read(".userdata/h700/nextui-mods/hidden-tools"), "# hidden") {
		t.Fatal("hide by name")
	}
}
