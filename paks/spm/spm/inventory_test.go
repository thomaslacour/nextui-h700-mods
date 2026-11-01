package main

import (
	"os"
	"path/filepath"
	"testing"
)

func testEnv(t *testing.T) *Env {
	t.Helper()
	return &Env{SD: t.TempDir()}
}

func mk(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectJoinsSavesStatesAndArtByName(t *testing.T) {
	e := testEnv(t)
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", "Pokemon Red.gb"))
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", ".media", "Pokemon Red.png"))
	mk(t, filepath.Join(e.saves(), "GB", "Pokemon Red.gb.sav"))
	mk(t, filepath.Join(e.shared(), "GB-gambatte", "Pokemon Red.gb.st0"))
	mk(t, filepath.Join(e.roms(), "Sony PlayStation (PS)", "Ape Escape (E).m3u"))

	games, err := e.collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 {
		t.Fatalf("got %d games, want 2", len(games))
	}
	byName := map[string]*Game{}
	for _, g := range games {
		byName[g.Name] = g
	}
	pkm := byName["Pokemon Red"]
	if pkm == nil || pkm.Tag != "GB" || pkm.System != "Game Boy" || pkm.Kind != "rom" {
		t.Fatalf("Pokemon Red not found correctly: %+v", pkm)
	}
	if pkm.Save == nil || pkm.Save.Path != filepath.Join(e.saves(), "GB", "Pokemon Red.gb.sav") {
		t.Fatalf("save not joined: %+v", pkm.Save)
	}
	if len(pkm.States) != 1 || pkm.States[0].Slot != 0 {
		t.Fatalf("states not joined: %+v", pkm.States)
	}
	if pkm.Art == "" || filepath.Base(pkm.Art) != "Pokemon Red.png" {
		t.Fatalf("art not joined: %q", pkm.Art)
	}
	ape := byName["Ape Escape"]
	if ape == nil || ape.Tag != "PS" || ape.Save != nil {
		t.Fatalf("Ape Escape not found correctly: %+v", ape)
	}
}

func TestScanSkipsArtSavesAndLaunchers(t *testing.T) {
	e := testEnv(t)
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", "Real.gb"))
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", "notes.txt"))            // skipExt
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", ".hidden.gb"))           // hidden
	mk(t, filepath.Join(e.roms(), "Streaming (MOONLIGHT)", "Moonlight.sh")) // launcher folder
	mk(t, filepath.Join(e.roms(), "Tools (TOOLS)", "0) Shortcut.sh"))       // pinned shortcut
	mk(t, filepath.Join(e.roms(), "Ports (PORTS)", "Chasm.sh"))             // a real port

	games, err := e.scanROMs()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range games {
		names = append(names, g.Name)
	}
	if len(games) != 2 || names[0] != "Chasm" || names[1] != "Real" {
		t.Fatalf("unexpected ROMs: %v", names)
	}
	if games[0].Kind != "port" || games[1].Kind != "rom" {
		t.Fatalf("kinds wrong: %+v", games)
	}
}

func TestAuditFindsOrphans(t *testing.T) {
	e := testEnv(t)
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", "Pokemon Red.gb"))
	mk(t, filepath.Join(e.saves(), "GB", "Pokemon Red.gb.sav"))
	mk(t, filepath.Join(e.saves(), "GB", "Ghost.gb.sav"))                  // orphan save
	mk(t, filepath.Join(e.shared(), "GB-gambatte", "Ghost.gb.st9"))        // orphan state
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", ".media", "Ghost.png")) // orphan art

	a, err := e.audit()
	if err != nil {
		t.Fatal(err)
	}
	if a.Games != 1 || a.WithSave != 1 || a.WithoutSave != 0 {
		t.Fatalf("counts wrong: %+v", a)
	}
	if len(a.OrphanSaves) != 1 || filepath.Base(a.OrphanSaves[0].Path) != "Ghost.gb.sav" {
		t.Fatalf("orphan saves wrong: %+v", a.OrphanSaves)
	}
	if len(a.OrphanStates) != 1 || a.OrphanStates[0].Name != "Ghost.gb" {
		t.Fatalf("orphan states wrong: %+v", a.OrphanStates)
	}
	if len(a.OrphanArt) != 1 || filepath.Base(a.OrphanArt[0].Path) != "Ghost.png" {
		t.Fatalf("orphan art wrong: %+v", a.OrphanArt)
	}
}

func TestAuditIgnoresPortSaves(t *testing.T) {
	e := testEnv(t)
	mk(t, filepath.Join(e.roms(), "Ports (PORTS)", "Chasm.sh"))
	a, err := e.audit()
	if err != nil {
		t.Fatal(err)
	}
	if a.WithSave != 0 || a.WithoutSave != 0 {
		t.Fatalf("ports must not count in save audit: %+v", a)
	}
}

func TestListJoinsHistory(t *testing.T) {
	e := testEnv(t)
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", "Pokemon Red.gb"))
	mk(t, filepath.Join(e.saves(), "GB", "Pokemon Red.gb.sav"))
	mk(t, filepath.Join(e.shared(), "GB-gambatte", "Pokemon Red.gb.st0"))
	mk(t, filepath.Join(e.shared(), "GB-gambatte", "Pokemon Red.gb.st1"))

	rows, err := e.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Name != "Pokemon Red" || r.System != "Game Boy" || r.Tag != "GB" {
		t.Fatalf("row identity wrong: %+v", r)
	}
	if r.Saves != 3 { // one in-game save + two states
		t.Fatalf("save count = %d, want 3", r.Saves)
	}
	if r.Path != filepath.Join("Game Boy (GB)", "Pokemon Red.gb") {
		t.Fatalf("path = %q", r.Path)
	}
}
