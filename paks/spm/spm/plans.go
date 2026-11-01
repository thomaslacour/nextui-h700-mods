package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

type PlannedChange struct {
	Kind       string `json:"kind"`
	From       string `json:"from"`
	To         string `json:"to"`
	Size       int64  `json:"size"`
	ModifiedNS int64  `json:"modified_unix_ns"`
}

type ReferenceEdit struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

type MutationPlan struct {
	Version        int             `json:"version"`
	ID             string          `json:"id"`
	Command        string          `json:"command"`
	SD             string          `json:"sd"`
	DryRun         bool            `json:"dry_run"`
	ApplyAvailable bool            `json:"apply_available"`
	Changes        []PlannedChange `json:"changes"`
	Fixes          []Fix           `json:"fixes,omitempty"`
	Blockers       []PlanIssue     `json:"blockers,omitempty"`
	Notes          []string        `json:"notes"`
}

func (e *Env) localRel(file string) string {
	rel, err := filepath.Rel(e.SD, file)
	if err != nil {
		return file
	}
	return filepath.ToSlash(rel)
}

func (e *Env) change(kind, from, to string) (PlannedChange, error) {
	rel := e.localRel(from)
	checked, err := guardedLocalPath(e.SD, filepath.FromSlash(rel))
	if err != nil {
		return PlannedChange{}, err
	}
	info, err := os.Lstat(checked)
	if err != nil {
		return PlannedChange{}, err
	}
	if !info.Mode().IsRegular() {
		return PlannedChange{}, fmt.Errorf("not a regular file: %s", from)
	}
	return PlannedChange{Kind: kind, From: rel, To: e.localRel(to), Size: info.Size(), ModifiedNS: info.ModTime().UnixNano()}, nil
}

func (e *Env) nameFixes(inv *Inventory, graph ReferenceGraph) []Fix {
	candidates := append([]*Game(nil), inv.Games...)
	seen := map[string]bool{}
	for _, g := range candidates {
		seen[g.Path] = true
	}
	var targets []string
	for path := range graph.Incoming {
		if !seen[path] {
			targets = append(targets, path)
		}
	}
	sort.Strings(targets)
	for _, path := range targets {
		candidates = append(candidates, &Game{Path: path, Kind: "disc_asset"})
	}
	var fixes []Fix
	for _, g := range candidates {
		base := filepath.Base(g.Path)
		clean := sanitizeFileName(base)
		if clean == base {
			continue
		}
		fix := Fix{Path: e.localRel(g.Path), New: clean, Changes: []PlannedChange{}}
		block := func(code, path, message string) {
			fix.Blockers = append(fix.Blockers, PlanIssue{code, e.localRel(path), message})
		}
		add := func(kind, from, to string, optional bool) {
			if from == to {
				return
			}
			if _, err := os.Lstat(from); optional && os.IsNotExist(err) {
				return
			}
			change, err := e.change(kind, from, to)
			if err != nil {
				block("dependency_unreadable", from, err.Error())
				return
			}
			fix.Changes = append(fix.Changes, change)
		}
		if g.Kind == "port" {
			block("port_launcher", g.Path, "port launchers require port-specific script review")
		}
		if invalidNameRe.MatchString(clean) {
			block("invalid_extension", g.Path, "the extension itself needs manual review")
		}
		add("rom", g.Path, filepath.Join(filepath.Dir(g.Path), clean), false)
		if g.Kind == "rom" {
			oldSave := filepath.Join(e.saves(), g.Tag, base)
			newSave := filepath.Join(e.saves(), g.Tag, clean)
			for _, ext := range []string{".sav", ".srm", ".rtc"} {
				add("save", oldSave+ext, newSave+ext, true)
			}
			legacy := filepath.Join(e.saves(), g.Tag, stem(base)+".srm")
			if legacy != oldSave+".srm" {
				add("legacy_save", legacy, filepath.Join(e.saves(), g.Tag, stem(clean)+".srm"), true)
			}
			for _, state := range g.States {
				suffix := strings.TrimPrefix(filepath.Base(state.Path), base)
				add("state", state.Path, filepath.Join(filepath.Dir(state.Path), clean+suffix), false)
				preview := filepath.Join(e.shared(), ".minui", g.Tag, fmt.Sprintf("%s.%d.bmp", base, state.Slot))
				add("preview", preview, filepath.Join(filepath.Dir(preview), fmt.Sprintf("%s.%d.bmp", clean, state.Slot)), true)
			}
			if g.Art != "" {
				add("boxart", g.Art, filepath.Join(filepath.Dir(g.Art), stem(clean)+filepath.Ext(g.Art)), false)
				for _, other := range inv.Games {
					if other.Path != g.Path && other.Art == g.Art {
						block("shared_boxart", g.Art, "box art also belongs to another ROM")
					}
				}
			}
		}
		for _, ext := range []string{".sbi", ".sub"} {
			add("disc_sidecar", strings.TrimSuffix(g.Path, filepath.Ext(g.Path))+ext, filepath.Join(filepath.Dir(g.Path), stem(clean)+ext), true)
		}
		if strings.EqualFold(filepath.Ext(g.Path), ".ccd") {
			add("disc_image", strings.TrimSuffix(g.Path, filepath.Ext(g.Path))+".img", filepath.Join(filepath.Dir(g.Path), stem(clean)+".img"), false)
		}
		for _, change := range fix.Changes {
			for _, ref := range graph.Incoming[filepath.Join(e.SD, filepath.FromSlash(change.From))] {
				newPath := filepath.Join(e.SD, filepath.FromSlash(change.To))
				value := newPath
				if !filepath.IsAbs(ref.Value) {
					value, _ = filepath.Rel(filepath.Dir(ref.File), newPath)
				}
				value = filepath.ToSlash(value)
				if strings.Contains(ref.Value, `\`) {
					value = strings.ReplaceAll(value, "/", `\`)
				}
				fix.References = append(fix.References, ReferenceEdit{e.localRel(ref.File), ref.Line, ref.Value, value})
			}
			if err := e.targetCollision(change); err != nil {
				block("destination_collision", filepath.Join(e.SD, filepath.FromSlash(change.To)), err.Error())
			}
		}
		if len(fix.References) > 0 {
			block("descriptor_rewrite_required", g.Path, "playlist/CUE edits must be reviewed together with the rename")
		}
		if _, err := os.Stat(e.gameLogs()); err == nil {
			block("history_update_required", e.gameLogs(), "rom.file_path and related tracker references must be updated transactionally")
		} else if !os.IsNotExist(err) {
			block("history_unreadable", e.gameLogs(), err.Error())
		}
		for _, metadata := range []string{"map.txt", "gamelist.xml"} {
			file := filepath.Join(filepath.Dir(g.Path), metadata)
			if _, err := os.Lstat(file); err == nil {
				block("metadata_review", file, "title/metadata references may use the old filename")
			}
		}
		fixes = append(fixes, fix)
	}
	sort.Slice(fixes, func(i, j int) bool { return fixes[i].Path < fixes[j].Path })
	return fixes
}

func (e *Env) targetCollision(change PlannedChange) error {
	to := filepath.Join(e.SD, filepath.FromSlash(change.To))
	entries, err := inventoryDir(filepath.Dir(to), true)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), filepath.Base(to)) {
			return fmt.Errorf("destination already exists (including FAT/exFAT case variants)")
		}
	}
	return nil
}

func (e *Env) mutationPlan(command string) (*MutationPlan, error) {
	inv, err := e.inventory()
	if err != nil {
		return nil, err
	}
	graph := e.references(inv.Games)
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	plan := &MutationPlan{Version: 1, ID: hex.EncodeToString(token[:]), Command: command, SD: e.SD, DryRun: true,
		Changes: []PlannedChange{}, Blockers: graph.Issues, Notes: []string{"Plan only; application is not implemented.", "File observations are not authorization to apply a stale plan."}}
	if command == "fix names" {
		plan.Fixes = e.nameFixes(inv, graph)
		from, to := map[string]bool{}, map[string]bool{}
		for i := range plan.Fixes {
			fix := &plan.Fixes[i]
			for _, change := range fix.Changes {
				if from[change.From] || to[strings.ToLower(change.To)] {
					fix.Blockers = append(fix.Blockers, PlanIssue{"planned_collision", change.From, "two rename groups share a source or destination"})
				}
				from[change.From], to[strings.ToLower(change.To)] = true, true
				plan.Changes = append(plan.Changes, change)
			}
			plan.Blockers = append(plan.Blockers, fix.Blockers...)
		}
		plan.Notes = append(plan.Notes, "Accents and valid Unicode are preserved; no ASCII transliteration.", "Browser-host metadata and existing recovery backups are not rewritten.")
		return plan, nil
	}
	audit := auditInventory(inv)
	if len(inv.Games) == 0 {
		plan.Blockers = append(plan.Blockers, PlanIssue{"no_roms", "Roms", "empty inventory cannot prove that all saves are disposable"})
	}
	for _, group := range []struct {
		kind  string
		files []Orphan
	}{{"save", audit.OrphanSaves}, {"state", audit.OrphanStates}, {"boxart", audit.OrphanArt}} {
		for _, orphan := range group.files {
			if orphan.Tag == "PORTS" {
				plan.Blockers = append(plan.Blockers, PlanIssue{"port_save", e.localRel(orphan.Path), "port saves require port-specific ownership checks"})
				continue
			}
			to := filepath.Join(e.SD, ".userdata/h700/spm/quarantine", plan.ID, filepath.FromSlash(e.localRel(orphan.Path)))
			change, err := e.change(group.kind, orphan.Path, to)
			if err != nil {
				return nil, err
			}
			plan.Changes = append(plan.Changes, change)
			known := orphan.Tag == ""
			for _, game := range inv.Games {
				if game.Tag == orphan.Tag {
					known = true
				}
				if orphan.Name != "" && strings.EqualFold(filepath.Base(game.Path), orphan.Name) {
					plan.Blockers = append(plan.Blockers, PlanIssue{"possible_relocated_rom", change.From, "a ROM with the same filename exists elsewhere; manual association required"})
				}
			}
			if !known {
				plan.Blockers = append(plan.Blockers, PlanIssue{"unknown_system", change.From, "system folder absent; source may be moved or unavailable"})
			}
		}
	}
	plan.Notes = append(plan.Notes, "Only native NextUI .sav/.st slots and .media PNGs are clean candidates; RTC/legacy formats are left untouched.")
	return plan, nil
}

func runPlan(e *Env, command string, args []string) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var apply, dry, jsonOut bool
	flags.BoolVar(&apply, "apply", false, "not available")
	flags.BoolVar(&dry, "dry-run", false, "plan only")
	flags.BoolVar(&dry, "n", false, "plan only")
	flags.BoolVar(&jsonOut, "json", false, "export the plan to stdout")
	if err := flags.Parse(args); err == flag.ErrHelp {
		fmt.Println(usage)
		return
	} else if err != nil {
		fail(err.Error())
	}
	if flags.NArg() != 0 {
		fail("unexpected plan arguments")
	}
	if apply {
		fail("application is unavailable: this command is dry-run only")
	}
	plan, err := e.mutationPlan(command)
	if err != nil {
		fail(err.Error())
	}
	if jsonOut {
		if err := writeJSON(plan); err != nil {
			fail(err.Error())
		}
		return
	}
	if command == "fix names" {
		printFixPlan(plan.Fixes)
	} else {
		fmt.Printf("would move to quarantine: %d file(s)\n", len(plan.Changes))
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		for _, c := range plan.Changes {
			fmt.Fprintf(w, "%s\t%q\t→ %q\n", c.Kind, c.From, c.To)
		}
		w.Flush()
	}
	for _, issue := range plan.Blockers {
		fmt.Printf("blocked [%s] %q: %s\n", issue.Code, issue.Path, issue.Message)
	}
	fmt.Println("\nDry run: nothing was changed. Application is not available.")
}
