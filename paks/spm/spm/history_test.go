package main

import (
	"os"
	"path/filepath"
	"testing"
)

// copyGameLogs puts the committed NextUI tracker fixture into a temp SD card.
func copyGameLogs(t *testing.T, e *Env) {
	t.Helper()
	data, err := os.ReadFile("testdata/game_logs.sqlite")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dst := e.gameLogs()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryAggregatesSessions(t *testing.T) {
	e := testEnv(t)
	copyGameLogs(t, e)

	rows, err := e.history()
	if err != nil {
		t.Fatal(err)
	}
	// "Zelda" (rom id 4) has no play_activity: it must not appear.
	if len(rows) != 3 {
		t.Fatalf("got %d history rows, want 3", len(rows))
	}
	byName := map[string]HistoryGame{}
	for _, h := range rows {
		byName[h.Name] = h
	}
	pkm := byName["Pokemon Red"]
	if pkm.Time != 960 || pkm.Sessions != 3 || pkm.Last != 5000 || pkm.First != 1000 {
		t.Fatalf("Pokemon Red aggregation wrong: %+v", pkm)
	}
	if pkm.System != "Game Boy" || pkm.Path != "Game Boy (GB)/Pokemon Red.gb" {
		t.Fatalf("Pokemon Red identity wrong: %+v", pkm)
	}
	ape := byName["Ape Escape"]
	if ape.Time != 1200 || ape.Sessions != 1 || ape.System != "Sony PlayStation" {
		t.Fatalf("Ape Escape aggregation wrong: %+v", ape)
	}
}

func TestHistorySearch(t *testing.T) {
	e := testEnv(t)
	copyGameLogs(t, e)
	rows, err := e.history()
	if err != nil {
		t.Fatal(err)
	}
	got := searchHistory(rows, []string{"ape"})
	if len(got) != 1 || got[0].Name != "Ape Escape" {
		t.Fatalf("search(ape) = %+v", got)
	}
	if got := searchHistory(rows, []string{"game boy", "gone"}); len(got) != 1 || got[0].Name != "Gone Game" {
		t.Fatalf("search(system + name) = %+v", got)
	}
}

func TestHistoryAuditFlagsMissingROMAndWAL(t *testing.T) {
	e := testEnv(t)
	copyGameLogs(t, e)
	// Present on the card: Pokemon Red and Ape Escape. "Gone Game" is absent.
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", "Pokemon Red.gb"))
	mk(t, filepath.Join(e.roms(), "Sony PlayStation (PS)", "Ape Escape (E).m3u"))

	a, err := e.auditHistory()
	if err != nil {
		t.Fatal(err)
	}
	if a.Games != 3 {
		t.Fatalf("games = %d, want 3", a.Games)
	}
	if len(a.Missing) != 1 || a.Missing[0].Name != "Gone Game" {
		t.Fatalf("missing = %+v", a.Missing)
	}
	if a.WAL {
		t.Fatal("no -wal file was written")
	}
	if err := os.WriteFile(e.gameLogs()+"-wal", []byte("pending"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, _ = e.auditHistory()
	if !a.WAL {
		t.Fatal("pending -wal file must be flagged")
	}
}

func TestHistoryWithoutTracker(t *testing.T) {
	e := testEnv(t)
	if _, err := e.history(); err == nil {
		t.Fatal("expected an error when game_logs.sqlite is absent")
	}
	if err := os.MkdirAll(e.roms(), 0o755); err != nil {
		t.Fatal(err)
	}
	rows, err := e.list() // list must still work without a tracker, on an empty card
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("no ROMs, got %d rows", len(rows))
	}
}
