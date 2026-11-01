package main

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBackupFilesListsSavesStatesAndRtc(t *testing.T) {
	e := testEnv(t)
	write(t, filepath.Join(e.saves(), "GB", "Pokemon.gb.sav"), "a")
	write(t, filepath.Join(e.saves(), "GBC", "Clock.gbc.rtc"), "b")
	write(t, filepath.Join(e.saves(), "GB", "._AppleDouble.sav"), "skip")
	write(t, filepath.Join(e.shared(), "GB-gambatte", "Pokemon.gb.st0"), "c")
	write(t, filepath.Join(e.shared(), "GB-gambatte", "Pokemon.gb.st9"), "d")
	write(t, filepath.Join(e.shared(), "GB-gambatte", "notes.txt"), "skip")
	write(t, filepath.Join(e.roms(), "Game Boy (GB)", "Pokemon.gb"), "rom")

	files, err := e.backupFiles()
	if err != nil {
		t.Fatal(err)
	}
	rels := map[string]bool{}
	for _, f := range files {
		rels[f.Rel] = true
	}
	if len(files) != 4 {
		t.Fatalf("got %d files, want 4: %v", len(files), rels)
	}
	for _, want := range []string{"Saves/GB/Pokemon.gb.sav", "Saves/GBC/Clock.gbc.rtc",
		".userdata/shared/GB-gambatte/Pokemon.gb.st0", ".userdata/shared/GB-gambatte/Pokemon.gb.st9"} {
		if !rels[want] {
			t.Errorf("missing %s in %v", want, rels)
		}
	}
}

func TestBackupPlanRejectsDestInsideSD(t *testing.T) {
	e := testEnv(t)
	if _, err := e.backupPlan(filepath.Join(e.SD, "somewhere")); err == nil {
		t.Fatal("destination inside the SD card must be rejected")
	}
	if _, err := e.backupPlan(""); err == nil {
		t.Fatal("empty destination must be rejected")
	}
	if _, err := e.backupPlan("relative/path"); err == nil {
		t.Fatal("relative destination must be rejected")
	}
}

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	e := testEnv(t)
	write(t, filepath.Join(e.saves(), "GB", "Pokemon.gb.sav"), "v1")
	write(t, filepath.Join(e.shared(), "GB-gambatte", "Pokemon.gb.st0"), "state")

	dest := t.TempDir() // outside e.SD
	plan, err := e.backupApply(dest)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TotalBytes == 0 || len(plan.Files) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := os.Stat(filepath.Join(dest, "manifest.json")); err != nil {
		t.Fatal("manifest not written")
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "Saves", "GB", "Pokemon.gb.sav")); string(got) != "v1" {
		t.Fatalf("copied save = %q", got)
	}

	// Change the SD save, then restore: it must come back and keep the old one.
	write(t, filepath.Join(e.saves(), "GB", "Pokemon.gb.sav"), "changed")
	rplan, err := e.restorePlan(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(rplan.Overwrites) != 2 {
		t.Fatalf("overwrites = %v", rplan.Overwrites)
	}
	found := false
	for _, o := range rplan.Overwrites {
		if o == "Saves/GB/Pokemon.gb.sav" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sav not among overwrites: %v", rplan.Overwrites)
	}
	if _, err := e.restoreApply(dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(e.saves(), "GB", "Pokemon.gb.sav")); string(got) != "v1" {
		t.Fatalf("restored save = %q", got)
	}
	backups, _ := filepath.Glob(filepath.Join(dest, ".spm-restore-backup", "*", "Saves", "GB", "Pokemon.gb.sav"))
	if len(backups) != 1 {
		t.Fatalf("overwritten save not kept: %v", backups)
	}
	if got, _ := os.ReadFile(backups[0]); string(got) != "changed" {
		t.Fatalf("kept save = %q", got)
	}
}

func TestBackupVerificationDetectsCorruption(t *testing.T) {
	e := testEnv(t)
	write(t, filepath.Join(e.saves(), "GB", "Pokemon.gb.sav"), "v1")
	dest := t.TempDir()
	if _, err := e.backupApply(dest); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dest, "Saves", "GB", "Pokemon.gb.sav"), "corrupt")
	m, err := readManifest(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyManifest(dest, *m); err == nil {
		t.Fatal("corruption must be detected")
	}
}

func TestBackupRequiresFreeSpace(t *testing.T) {
	e := testEnv(t)
	write(t, filepath.Join(e.saves(), "GB", "Pokemon.gb.sav"), "v1")
	dest := t.TempDir()
	old := diskFree
	diskFree = func(string) (uint64, error) { return 1, nil }
	defer func() { diskFree = old }()
	if _, err := e.backupApply(dest); err == nil {
		t.Fatal("insufficient free space must be refused")
	}
}

func TestBackupDryRunWritesNothing(t *testing.T) {
	e := testEnv(t)
	write(t, filepath.Join(e.saves(), "GB", "Pokemon.gb.sav"), "v1")
	dest := t.TempDir()
	if _, err := e.backupPlan(dest); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Fatalf("dry run must not write: %v", entries)
	}
}
