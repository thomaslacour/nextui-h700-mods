package main

// mutations.go — the commands that would change the card (clean, fix names).
// They are dry-run only for now: every one produces a plan and refuses to
// apply. The shared rule, like rsync --dry-run or terraform plan, is that a
// mutation happens only behind an explicit --apply that is not implemented yet.

import (
	"fmt"
	"os"
	"text/tabwriter"
)

// Fix is one proposed rename, ROM path -> new file name.
type Fix struct {
	Path       string          `json:"path"`
	New        string          `json:"new_name"`
	Changes    []PlannedChange `json:"changes"`
	References []ReferenceEdit `json:"references,omitempty"`
	Blockers   []PlanIssue     `json:"blockers,omitempty"`
}

// fixNames lists ROM files whose name is unsafe on the SD card or for
// emulators/scrapers, and proposes the sanitized name. Saves, states and box
// art for those games would be renamed together when applying (not yet).
func (e *Env) fixNames() ([]Fix, error) {
	inv, err := e.inventory()
	if err != nil {
		return nil, err
	}
	return e.nameFixes(inv, e.references(inv.Games)), nil
}

func printFixPlan(fixes []Fix) {
	if len(fixes) == 0 {
		fmt.Println("no ROM needs renaming")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ROM\tRENAME TO\tDEPENDENCIES\tSTATUS")
	for _, f := range fixes {
		status := "review"
		if len(f.Blockers) > 0 {
			status = "blocked"
		}
		fmt.Fprintf(w, "%q\t%q\t%d file(s), %d reference(s)\t%s\n", f.Path, f.New, len(f.Changes), len(f.References), status)
	}
	w.Flush()
	fmt.Println("\nPlan only: associated saves, states, art, playlists and history must be reviewed before any rename can be applied.")
}

// hasFlag reports whether any of the given flags appears in args.
func hasFlag(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n {
				return true
			}
		}
	}
	return false
}
