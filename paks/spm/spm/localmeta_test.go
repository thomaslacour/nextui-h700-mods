package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchROMsWithoutXML(t *testing.T) {
	e := testEnv(t)
	write(t, filepath.Join(e.roms(), "Game Boy (GB)", "Zelda.gb"), "rom")
	write(t, filepath.Join(e.roms(), "Game Boy (GB)", "Mario.gb"), "rom")
	hits, err := e.searchLocal("ZELDA boy")
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	if hits[0].Name != "Zelda" || hits[0].System != "Game Boy" {
		t.Fatalf("%+v", hits[0])
	}
	info, err := e.localInfo(hits[0].ID)
	if err != nil || info.Path != hits[0].Path {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	count, err := e.updateLocalIndex()
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestXMLOnlyEnrichesExactExistingROM(t *testing.T) {
	e := testEnv(t)
	dir := filepath.Join(e.roms(), "Game Boy (GB)")
	write(t, filepath.Join(dir, "Zelda.gb"), "rom")
	write(t, filepath.Join(dir, "gamelist.xml"), `<gameList>
<game id="123"><path>./Zelda.gb</path><name>Link's Awakening</name><desc>A quest in Koholint</desc><publisher>Nintendo</publisher></game>
<game><path>./Missing.gb</path><name>Missing Zelda</name></game>
</gameList>`)
	hits, err := e.searchLocal("zelda nintendo")
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	if hits[0].Name != "Link's Awakening" || hits[0].Desc == "" {
		t.Fatalf("%+v", hits[0])
	}
	if hits[0].ID != "Roms/Game Boy (GB)/Zelda.gb" {
		t.Fatal(hits[0].ID)
	}
	all, err := e.localIndex()
	if err != nil || len(all) != 1 {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	hits, err = e.searchLocal("koholint")
	if err != nil || len(hits) != 1 {
		t.Fatalf("description search: %v %v", hits, err)
	}
}

func TestMalformedOptionalXMLDoesNotHideROMs(t *testing.T) {
	e := testEnv(t)
	dir := filepath.Join(e.roms(), "Game Boy (GB)")
	write(t, filepath.Join(dir, "Zelda.gb"), "rom")
	write(t, filepath.Join(dir, "gamelist.xml"), `<broken`)
	hits, err := e.searchLocal("zelda")
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}

func TestInfoRejectsAmbiguousAndEmptyQueries(t *testing.T) {
	e := testEnv(t)
	for _, system := range []string{"Game Boy (GB)", "Game Boy Color (GBC)"} {
		write(t, filepath.Join(e.roms(), system, "Zelda.gb"), "rom")
	}
	if _, err := e.localInfo("Zelda.gb"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatal(err)
	}
	if _, err := e.localInfo(""); err == nil {
		t.Fatal("empty query accepted")
	}
}

func TestMetadataMatchesSDRootAlias(t *testing.T) {
	e := testEnv(t)
	dir := filepath.Join(e.roms(), "Game Boy (GB)")
	write(t, filepath.Join(dir, "Zelda.gb"), "rom")
	write(t, filepath.Join(dir, "gamelist.xml"), `<gameList><game><path>`+filepath.Join(dir, "Zelda.gb")+`</path><name>Enriched Zelda</name></game></gameList>`)
	alias := filepath.Join(t.TempDir(), "SDCARD")
	if err := os.Symlink(e.SD, alias); err != nil {
		t.Fatal(err)
	}
	e.SD = alias
	hits, err := e.searchLocal("enriched")
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}
