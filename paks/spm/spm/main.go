package main

// main.go — terminal-only CLI. ROM/history mutations remain plan-only;
// backup/restore require an explicit --apply and share the same flag parser.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

const version = "0.1.0"

const usage = `spm — SP manager

  spm list [--json]             games: name, system, play time, saves, last
  spm audit [--json]            ROM/save/state/box-art correspondence (alias: doctor)
  spm history list [--json]     play history from game_logs.sqlite
  spm history search word...    history whose name/system/path matches every word
  spm history audit             tracker rows whose ROM no longer exists
  spm clean                     plan: move orphan saves/states/art to quarantine (dry-run)
  spm fix names                 plan: rename ROMs with unsupported characters (dry-run)
  spm backup --to <dir> [--apply]   verified save snapshot (alias: bk; dry-run)
  spm restore --from <dir> [--apply]  put a backup back (dry-run; keeps overwritten saves)
  spm verify --from <dir>       verify every backup file without restoring
  spm version
  spm --check                   probe the card and report its layout

Backup/restore default to --dry-run (-n); only --apply writes. Do not combine them.
Use --mount /path/to/usb (or SPM_BACKUP_MOUNT) to require mounted external storage.
Backup needs a new empty directory; clean/fix remain plan-only.
Paths come from SDCARD_PATH (default /mnt/SDCARD).`

// listRow is one line of `spm list`, ROM joined with history and saves.
type listRow struct {
	Name     string `json:"name"`
	System   string `json:"system"`
	Tag      string `json:"tag"`
	Path     string `json:"path"`
	Time     int64  `json:"time_seconds"`
	Sessions int    `json:"sessions"`
	Saves    int    `json:"saves"`
	Last     int64  `json:"last_played"`
}

func relPath(env *Env, abs string) string {
	if p, err := filepath.Rel(env.roms(), abs); err == nil && !strings.HasPrefix(p, "..") {
		return p
	}
	return abs
}

func (e *Env) list() ([]listRow, error) {
	games, err := e.collect()
	if err != nil {
		return nil, err
	}
	hist, err := e.history()
	byPath := map[string]HistoryGame{}
	if err == nil {
		for _, h := range hist {
			byPath[h.Path] = h
		}
	}
	rows := make([]listRow, 0, len(games))
	for _, g := range games {
		saves := 0
		if g.Save != nil {
			saves++
		}
		saves += len(g.States)
		h := byPath[relPath(e, g.Path)]
		rows = append(rows, listRow{
			Name: g.Name, System: g.System, Tag: g.Tag, Path: relPath(e, g.Path),
			Time: h.Time, Sessions: h.Sessions, Saves: saves, Last: h.Last})
	}
	return rows, nil
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// cachedScraper builds a ScreenScraper client with the on-disk cache, honouring
// --refresh (bypass the cache). Credentials are loaded from env/config.
func cachedScraper(e *Env, rest []string) (*scraper, error) {
	creds, err := e.screenScraperCreds()
	if err != nil {
		// A fresh cache is usable without credentials or a network connection.
		// Missing credentials are reported by get() only on an actual cache miss.
		creds = Credentials{}
	}
	s := newScraper("", creds)
	s.cache = newCache(cacheDir(e))
	s.refresh = hasFlag(rest, "--refresh")
	return s, nil
}

// stripFlags drops the given flags from args, keeping positional terms.
func stripFlags(args []string, names ...string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if hasFlag([]string{a}, names...) {
			continue
		}
		out = append(out, a)
	}
	return out
}

func printList(rows []listRow) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSYSTEM\tTIME\tSESSIONS\tSAVES\tLAST\tPATH")
	for _, r := range rows {
		time := "—"
		if r.Sessions > 0 {
			time = duration(r.Time)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
			r.Name, r.System, time, r.Sessions, r.Saves, unixLabel(r.Last), r.Path)
	}
	w.Flush()
}

func printAudit(a *Audit) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "games\t%d\nwith in-game save\t%d\nwithout in-game save\t%d\n", a.Games, a.WithSave, a.WithoutSave)
	if len(a.OrphanSaves) > 0 {
		fmt.Fprintf(w, "\norphan saves (%d)\t\n", len(a.OrphanSaves))
		for _, o := range a.OrphanSaves {
			fmt.Fprintf(w, "  %s\t[%s] %s\n", o.Path, o.Tag, o.Name)
		}
	}
	if len(a.OrphanStates) > 0 {
		fmt.Fprintf(w, "\norphan save states (%d)\t\n", len(a.OrphanStates))
		for _, o := range a.OrphanStates {
			fmt.Fprintf(w, "  %s\t[%s] %s\n", o.Path, o.Tag, o.Name)
		}
	}
	if len(a.OrphanArt) > 0 {
		fmt.Fprintf(w, "\norphan box art (%d)\t\n", len(a.OrphanArt))
		for _, o := range a.OrphanArt {
			fmt.Fprintf(w, "  %s\t\n", o.Path)
		}
	}
	w.Flush()
}

func printHistory(rows []HistoryGame) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSYSTEM\tTIME\tSESSIONS\tLAST\tPATH")
	for _, h := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
			h.Name, h.System, duration(h.Time), h.Sessions, unixLabel(h.Last), h.Path)
	}
	w.Flush()
}

func printHistoryAudit(a *historyAudit) {
	fmt.Printf("tracked games: %d\n", a.Games)
	if a.WAL {
		fmt.Println("warning: game_logs.sqlite-wal has uncommitted sessions; close the game/console first")
	}
	if len(a.Missing) == 0 {
		fmt.Println("no tracked game points at a missing ROM")
		return
	}
	fmt.Printf("missing ROMs: %d\n", len(a.Missing))
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, h := range a.Missing {
		fmt.Fprintf(w, "%s\t%s\t%s\n", h.Name, h.System, h.Path)
	}
	w.Flush()
}

func printSearch(hits []SearchHit) {
	if len(hits) == 0 {
		fmt.Println("no match")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTITLE\tSYSTEM\tYEAR\tDESCRIPTION")
	for _, h := range hits {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", h.ID, h.Name, h.System, h.Year, firstLine(h.Synopsis))
	}
	w.Flush()
}

func printInfo(g *GameInfo) {
	fmt.Printf("id: %s\n", g.ID)
	fmt.Printf("title: %s\n", g.Name)
	fmt.Printf("system: %s\n", g.System)
	fmt.Printf("year: %s\n", g.Year)
	fmt.Printf("publisher: %s\n", g.Publisher)
	fmt.Printf("developer: %s\n", g.Developer)
	fmt.Printf("players: %s\n", g.Players)
	fmt.Printf("rating: %s\n", g.Rating)
	if len(g.Genres) > 0 {
		fmt.Printf("genres: %s\n", strings.Join(g.Genres, ", "))
	}
	if g.Description != "" {
		fmt.Printf("\n%s\n", g.Description)
	}
}

func printBackupPlan(p *backupPlan) {
	fmt.Printf("backup to: %s\n", p.Dest)
	fmt.Printf("files: %d · %s\n", len(p.Files), humanBytes(p.TotalBytes))
	if p.FreeBytes > 0 {
		enough := "enough"
		if !p.Enough {
			enough = "NOT ENOUGH"
		}
		fmt.Printf("free space: %s · %s\n", humanBytes(int64(p.FreeBytes)), enough)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, f := range p.Files {
		fmt.Fprintf(w, "%s\t%s\n", humanBytes(f.Size), f.Rel)
	}
	w.Flush()
}

func printRestorePlan(p *restorePlan) {
	fmt.Printf("restore from: %s\n", p.From)
	fmt.Printf("files: %d · overwriting %d current save(s)\n", len(p.Files), len(p.Overwrites))
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, f := range p.Files {
		mark := ""
		for _, o := range p.Overwrites {
			if o == f.Rel {
				mark = "overwrite"
				break
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", humanBytes(f.Size), mark, f.Rel)
	}
	w.Flush()
}

type transferOptions struct {
	Path, Mount string
	Apply, JSON bool
}

func parseTransferOptions(command string, args []string) (transferOptions, error) {
	var o transferOptions
	var dry bool
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	location := "from"
	if command == "backup" {
		location = "to"
	}
	flags.StringVar(&o.Path, location, "", "snapshot directory")
	flags.StringVar(&o.Mount, "mount", os.Getenv("SPM_BACKUP_MOUNT"), "required external mount")
	flags.BoolVar(&o.JSON, "json", false, "JSON output")
	if command != "verify" {
		flags.BoolVar(&o.Apply, "apply", false, "perform the operation")
		flags.BoolVar(&dry, "dry-run", false, "preview only")
		flags.BoolVar(&dry, "n", false, "preview only")
	}
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	if flags.NArg() != 0 || o.Path == "" {
		return o, fmt.Errorf("%s requires --%s <directory> and no positional arguments", command, location)
	}
	if dry && o.Apply {
		return o, fmt.Errorf("--dry-run/-n and --apply cannot be combined")
	}
	return o, nil
}

func runTransfer(env *Env, command string, rest []string) {
	o, err := parseTransferOptions(command, rest)
	if err == flag.ErrHelp {
		fmt.Println(usage)
		return
	}
	if err != nil {
		fail(err.Error())
	}
	env.BackupMount = o.Mount
	var result any
	switch command {
	case "backup":
		var plan *backupPlan
		if o.Apply {
			plan, err = env.backupApply(o.Path)
		} else {
			plan, err = env.backupPlan(o.Path)
		}
		result = plan
		if err == nil && !o.JSON {
			if o.Apply {
				fmt.Printf("backed up %d files to %s\n", len(plan.Files), o.Path)
			} else {
				printBackupPlan(plan)
			}
		}
	case "restore":
		var plan *restorePlan
		if o.Apply {
			plan, err = env.restoreApply(o.Path)
		} else {
			plan, err = env.restorePlan(o.Path)
		}
		result = plan
		if err == nil && !o.JSON {
			if o.Apply {
				fmt.Printf("restored %d files; originals/journal: %s\n", len(plan.Files), plan.Rollback)
			} else {
				printRestorePlan(plan)
			}
		}
	case "verify":
		err = env.checkDest(o.Path)
		if err == nil {
			var manifest *Manifest
			manifest, err = readManifest(o.Path)
			if err == nil {
				err = verifyManifest(o.Path, *manifest)
			}
			if err == nil {
				result = map[string]any{"verified": true, "files": len(manifest.Files)}
				if !o.JSON {
					fmt.Printf("verified %d files\n", len(manifest.Files))
				}
			}
		}
	}
	if err != nil {
		fail(err.Error())
	}
	if o.JSON {
		if err := writeJSON(result); err != nil {
			fail(err.Error())
		}
	} else if !o.Apply && command != "verify" {
		fmt.Println("\nDry run: nothing was written. Add --apply to perform this operation.")
	}
}

func (e *Env) check() error {
	if _, err := os.Stat(e.roms()); err != nil {
		return fmt.Errorf("Roms not found at %s", e.roms())
	}
	games, err := e.scanROMs()
	if err != nil {
		return err
	}
	fmt.Printf("SD %s: %d games\n", e.SD, len(games))
	return nil
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "spm:", msg)
	os.Exit(1)
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(usage)
		return
	}
	env := newEnv()

	jsonOut := func(v any, err error) {
		if err != nil {
			fail(err.Error())
		}
		if err := writeJSON(v); err != nil {
			fail(err.Error())
		}
	}
	jsonFlag := func(rest []string) bool {
		return len(rest) > 0 && rest[len(rest)-1] == "--json"
	}

	switch args[0] {
	case "help", "--help", "-h":
		fmt.Println(usage)
	case "version", "-v", "--version":
		fmt.Println(version)
	case "--check", "check":
		if err := env.check(); err != nil {
			fail(err.Error())
		}
	case "list":
		rows, err := env.list()
		if err != nil {
			fail(err.Error())
		}
		if jsonFlag(args[1:]) {
			jsonOut(rows, nil)
			return
		}
		printList(rows)
	case "audit", "doctor":
		a, err := env.audit()
		if err != nil {
			fail(err.Error())
		}
		if jsonFlag(args[1:]) {
			jsonOut(a, nil)
			return
		}
		printAudit(a)
	case "clean":
		runPlan(env, "clean", args[1:])
	case "fix":
		if len(args) < 2 || args[1] != "names" {
			fail("fix needs: names")
		}
		runPlan(env, "fix names", args[2:])
	case "search":
		fail("ScreenScraper features removed")
	case "info":
		fail("ScreenScraper features removed")
	case "update", "cache":
		fail("ScreenScraper features removed")
	case "backup", "bk":
		runTransfer(env, "backup", args[1:])
	case "restore", "verify":
		runTransfer(env, args[0], args[1:])
	case "history":
		if len(args) < 2 {
			fail("history needs list, search or audit")
		}
		switch args[1] {
		case "list":
			rows, err := env.history()
			if err != nil {
				fail(err.Error())
			}
			if jsonFlag(args[2:]) {
				jsonOut(rows, nil)
				return
			}
			printHistory(rows)
		case "search":
			if len(args) < 3 {
				fail("history search needs a word")
			}
			rows, err := env.history()
			if err != nil {
				fail(err.Error())
			}
			printHistory(searchHistory(rows, args[2:]))
		case "audit":
			a, err := env.auditHistory()
			if err != nil {
				fail(err.Error())
			}
			printHistoryAudit(a)
		default:
			fail("unknown history command: " + args[1])
		}
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
}
