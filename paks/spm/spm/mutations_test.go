package main

import (
	"path/filepath"
	"testing"
)

func TestFixNamesProposesSanitizedNames(t *testing.T) {
	e := testEnv(t)
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", "Pokemon: Red.gb"))
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", "Good.gb"))
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", "Trailing .gbc"))

	fixes, err := e.fixNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(fixes) != 2 {
		t.Fatalf("got %d fixes, want 2: %+v", len(fixes), fixes)
	}
	want := map[string]string{"Pokemon: Red.gb": "Pokemon_ Red.gb", "Trailing .gbc": "Trailing.gbc"}
	for _, f := range fixes {
		if want[filepath.Base(f.Path)] != f.New {
			t.Fatalf("wrong rename for %q: %q", filepath.Base(f.Path), f.New)
		}
	}
}

func TestFixNamesKeepsCleanNamesUntouched(t *testing.T) {
	e := testEnv(t)
	mk(t, filepath.Join(e.roms(), "Game Boy (GB)", "Pokemon Red.gb"))
	fixes, err := e.fixNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(fixes) != 0 {
		t.Fatalf("clean names must not be listed: %+v", fixes)
	}
}
