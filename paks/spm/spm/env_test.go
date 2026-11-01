package main

import "testing"

func TestDisplayName(t *testing.T) {
	cases := map[string]string{
		"Pokemon Red.gb": "Pokemon Red",
		"Legend of Zelda, The - Link's Awakening.gbc": "The Legend of Zelda - Link's Awakening",
		"001) My Game.gba":             "My Game",
		"Super Mario (USA).sfc":        "Super Mario",
		"game.p8.png":                  "game",
		"Multi [Disc 1].cue":           "Multi",
		"Mega Man (Europe) (Rev 1).md": "Mega Man",
		"no-extension":                 "no-extension",
		"Mr. Driller (J).psx":          "Mr. Driller",
	}
	for in, want := range cases {
		if got := displayName(in); got != want {
			t.Errorf("displayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSystemTagAndName(t *testing.T) {
	for folder, wantTag := range map[string]string{
		"Game Boy (GB)": "GB", "Game Boy Color (GBC)": "GBC",
		"Sony PlayStation (PS)": "PS", "Ports (PORTS)": "PORTS",
		"Famicom Disk System (FDS)": "FDS",
	} {
		if got := systemTag(folder); got != wantTag {
			t.Errorf("systemTag(%q) = %q, want %q", folder, got, wantTag)
		}
	}
	if got := systemName("Game Boy (GB)"); got != "Game Boy" {
		t.Errorf("systemName = %q, want %q", got, "Game Boy")
	}
	if got := systemTag("No Tag Here"); got != "" {
		t.Errorf("systemTag(no tag) = %q, want empty", got)
	}
}

func TestIsLauncher(t *testing.T) {
	if !isLauncher("Streaming (MOONLIGHT)", "Whatever.sh") {
		t.Error("MOONLIGHT folder is a launcher")
	}
	if !isLauncher("Tools (TOOLS)", "0) Portmaster") {
		t.Error("pinned '0) …' shortcut is a launcher")
	}
	if isLauncher("Game Boy (GB)", "Pokemon.gb") {
		t.Error("a real ROM is not a launcher")
	}
	if isLauncher("Ports (PORTS)", "Chasm.sh") {
		t.Error("an installed port launcher is not a shortcut")
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"Pokemon Red.gb":   "Pokemon Red.gb",
		"Pokemon: Red.gb":  "Pokemon_ Red.gb",
		"Mario/Bros.gbc":   "Mario_Bros.gbc",
		"Who? (U).sfc":     "Who_ (U).sfc",
		"Trailing Dot..gb": "Trailing Dot.gb",
		"A <B> C.md":       "A _B_ C.md",
	}
	for in, want := range cases {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
