package main

// env.go — SD-card paths and the NextUI naming conventions (system tag,
// display name, what counts as a game). One place, shared by every command.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Env points every command at one SD card. Tests build a temporary card and
// use the same struct, so no command reaches the real card by accident.
type Env struct {
	SD          string
	BackupMount string // Optional required external mount, never created by spm.
}

func newEnv() *Env {
	sd := os.Getenv("SDCARD_PATH")
	if sd == "" {
		sd = "/mnt/SDCARD"
	}
	return &Env{SD: sd, BackupMount: os.Getenv("SPM_BACKUP_MOUNT")}
}

func (e *Env) roms() string     { return filepath.Join(e.SD, "Roms") }
func (e *Env) saves() string    { return filepath.Join(e.SD, "Saves") }
func (e *Env) shared() string   { return filepath.Join(e.SD, ".userdata", "shared") }
func (e *Env) gameLogs() string { return filepath.Join(e.shared(), "game_logs.sqlite") }

var tagRe = regexp.MustCompile(`\s*\(([^)]+)\)$`)
var sortPrefixRe = regexp.MustCompile(`^\d+\)\s*`)
var articleRe = regexp.MustCompile(`^(.+?), (The|A|An|Le|La|Les|L'|Der|Die|Das|El|Los|Las|Il|Lo|Gli)( - .*|: .*)?$`)

// Characters the SD card (FAT/exFAT) cannot store, plus control bytes.
var invalidNameRe = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)

// systemTag returns the "(TAG)" of a Roms folder, e.g. "Game Boy (GB)" -> "GB".
func systemTag(folder string) string {
	if m := tagRe.FindStringSubmatch(folder); m != nil {
		return m[1]
	}
	return ""
}

// systemName is the folder without its tag: "Game Boy (GB)" -> "Game Boy".
func systemName(folder string) string { return tagRe.ReplaceAllString(folder, "") }

// skipExt are files NextUI lists but which are not games (art, notes, saves).
var skipExt = map[string]bool{
	"txt": true, "png": true, "jpg": true, "jpeg": true, "xml": true, "cfg": true,
	"ini": true, "srm": true, "sav": true, "state": true, "bak": true, "db": true,
	"dat": true, "json": true,
}

// isLauncher matches streaming launchers (MOONLIGHT) and the "0) …" shortcuts
// pinned at the top of a folder; neither is a real game on the SD card.
func isLauncher(folder, name string) bool {
	if strings.EqualFold(systemTag(folder), "MOONLIGHT") {
		return true
	}
	return sortPrefixRe.MatchString(name)
}

// displayName mirrors NextUI 6.15.0 getDisplayName + trimSortingMeta: drop
// extensions of 2–4 letters (".p8.png" too), cut everything from the last "("
// or "[" while something remains, remove a leading sort number ("001) "), then
// move a trailing article back in front ("Zelda, The" -> "The Zelda").
func displayName(name string) string {
	out := filepath.Base(name)
	for {
		i := strings.LastIndexByte(out, '.')
		if i < 0 || !(2 < len(out)-i && len(out)-i <= 5) {
			break
		}
		out = out[:i]
	}
	work := out
	for {
		i := strings.LastIndexByte(out, '(')
		if i < 0 {
			i = strings.LastIndexByte(out, '[')
		}
		if i <= 0 {
			break
		}
		out = out[:i]
	}
	if out == "" {
		out = work
	}
	out = strings.TrimRight(out, " ")
	if trimmed := sortPrefixRe.ReplaceAllString(out, ""); trimmed != "" {
		out = trimmed
	}
	if m := articleRe.FindStringSubmatch(out); m != nil {
		sep := " "
		if strings.HasSuffix(m[2], "'") {
			sep = ""
		}
		out = m[2] + sep + m[1] + m[3]
	}
	return out
}

// sanitizeFileName makes a file name safe for the SD card (FAT/exFAT) and for
// emulators/scrapers: characters those filesystems cannot store become "_",
// and a trailing space or dot is dropped. The extension is kept untouched.
// An unchanged return means the name is already safe.
func sanitizeFileName(name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	stem = invalidNameRe.ReplaceAllString(stem, "_")
	stem = strings.TrimRight(stem, " .")
	if stem == "" {
		stem = "_"
	}
	return stem + ext
}
