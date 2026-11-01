package main

// audit.go — cross-reference ROMs, in-game saves, save states and box art and
// report what no longer matches. Read-only: it only reports, never changes
// the card. `clean` will later turn these findings into a quarantine plan.

import (
	"path/filepath"
	"sort"
	"strings"
)

// Orphan is a save, state or box-art file with no matching game.
type Orphan struct {
	Path string
	Tag  string
	Name string
}

// Audit is the result of the correspondence check.
type Audit struct {
	Games        int
	WithSave     int
	WithoutSave  int
	OrphanSaves  []Orphan
	OrphanStates []Orphan
	OrphanArt    []Orphan
}

func splitKey(k string) (tag, name string) {
	if i := strings.IndexByte(k, '/'); i >= 0 {
		return k[:i], k[i+1:]
	}
	return "", k
}

func (e *Env) audit() (*Audit, error) {
	inv, err := e.inventory()
	if err != nil {
		return nil, err
	}
	return auditInventory(inv), nil
}

func auditInventory(inv *Inventory) *Audit {
	games := inv.Games
	a := &Audit{Games: len(games)}

	romKeys := map[string]bool{} // tag + ROM file name
	artKeys := map[string]bool{} // folder + ROM stem
	for _, g := range games {
		artKeys[key(filepath.Base(filepath.Dir(g.Path)), stem(g.Path))] = true
		if g.Kind == "port" {
			continue // PortMaster keeps its own saves outside Saves/
		}
		romKeys[key(g.Tag, filepath.Base(g.Path))] = true
		if g.Save != nil {
			a.WithSave++
		} else {
			a.WithoutSave++
		}
	}

	for k, s := range inv.Saves {
		if !romKeys[k] {
			tag, name := splitKey(k)
			a.OrphanSaves = append(a.OrphanSaves, Orphan{Path: s.Path, Tag: tag, Name: name})
		}
	}
	for k, sts := range inv.States {
		if !romKeys[k] {
			tag, name := splitKey(k)
			for _, st := range sts {
				a.OrphanStates = append(a.OrphanStates, Orphan{Path: st.Path, Tag: tag, Name: name})
			}
		}
	}
	for k, p := range inv.Art {
		if !artKeys[k] {
			a.OrphanArt = append(a.OrphanArt, Orphan{Path: p})
		}
	}

	byPath := func(o []Orphan) {
		sort.Slice(o, func(i, j int) bool { return o[i].Path < o[j].Path })
	}
	byPath(a.OrphanSaves)
	byPath(a.OrphanStates)
	byPath(a.OrphanArt)
	return a
}
