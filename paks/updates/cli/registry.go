package main

// Installed paks. Our own registry (<userdata>/pak/installed.json) is the
// reference; rows of the Pak Store database are imported when we do not know
// the pak yet (installs made with the Pak Store before it was hidden), and a
// higher version found there or in the pak's pak.json wins. A pak of our own
// catalogues found on the card (installed by the Mac) is adopted, so `pak
// upgrade` manages it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type installed struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	RepoURL      string `json:"repo_url"`
	Type         string `json:"type"`
	Version      string `json:"version"`
	CanUninstall bool   `json:"can_uninstall"`
	Source       string `json:"source"` // "pak" or "pak-store"
}

type registry struct {
	paks []*installed
}

func (e *env) registryFile() string { return filepath.Join(e.home, "installed.json") }
func (e *env) storeDB() string {
	return filepath.Join(e.userdata, "nextui-pak-store", "pak-store.db")
}

func (e *env) loadRegistry() *registry {
	r := &registry{}
	if data, err := os.ReadFile(e.registryFile()); err == nil {
		if err := json.Unmarshal(data, &r.paks); err != nil {
			e.warn("%s is damaged (%v): rebuilt from the Pak Store database", e.registryFile(), err)
			r.paks = nil
		}
	}
	for _, row := range e.storeRows() {
		if _, _, ok := e.pakPath(row.Type, row.Name); !ok {
			continue // removed since (or never there): do not resurrect it
		}
		if have := r.match(row); have != nil {
			if cmpVersion(row.Version, have.Version) > 0 {
				have.Version = row.Version
			}
			if have.ID == "" {
				have.ID = row.ID
			}
			continue
		}
		row.Source = "pak-store"
		r.paks = append(r.paks, row)
	}
	if c, err := e.cachedCatalog(); err == nil {
		for _, p := range c.paks {
			if !p.Own || p.isPakz() {
				continue
			}
			if have := r.byID(p.ID); have != nil {
				r.dropOthers(have) // one entry per folder: ours
				continue
			}
			dir, _, ok := e.pakPath(p.Type, p.Name)
			if !ok {
				continue
			}
			x := &installed{ID: p.ID, Name: p.Name, DisplayName: p.title(), RepoURL: p.RepoURL,
				Type: p.Type, Version: diskVersion(dir), CanUninstall: true, Source: "local"}
			// Our catalogue owns this folder: it replaces another entry for it
			// (e.g. a Pak Store pak of the same name installed long ago).
			if old := r.byFolder(p.Type, p.Name); old != nil {
				*old = *x
				continue
			}
			r.paks = append(r.paks, x)
		}
	}
	return r
}

// storeRows reads the Pak Store database; errors only matter to `pak doctor`.
func (e *env) storeRows() []*installed {
	db, err := openSQLite(e.storeDB())
	if err != nil {
		return nil
	}
	rows, err := db.tableRows("installed_paks")
	if err != nil {
		return nil
	}
	str := func(v any) string { s, _ := v.(string); return s }
	var out []*installed
	for _, row := range rows {
		can, _ := row["can_uninstall"].(int64)
		out = append(out, &installed{
			ID: str(row["pak_id"]), Name: str(row["name"]), DisplayName: str(row["display_name"]),
			RepoURL: str(row["repo_url"]), Type: str(row["type"]), Version: str(row["version"]),
			CanUninstall: can != 0,
		})
	}
	return out
}

func (r *registry) match(x *installed) *installed {
	for _, p := range r.paks {
		if x.ID != "" && p.ID == x.ID {
			return p
		}
		if x.ID == "" || p.ID == "" {
			if x.RepoURL != "" && p.RepoURL == x.RepoURL {
				return p
			}
		}
	}
	return nil
}

func (r *registry) byID(id string) *installed {
	for _, p := range r.paks {
		if id != "" && p.ID == id {
			return p
		}
	}
	return nil
}

func (r *registry) byFolder(typ, name string) *installed {
	for _, p := range r.paks {
		if strings.EqualFold(p.Name, name) && strings.EqualFold(p.Type, typ) {
			return p
		}
	}
	return nil
}

// dropOthers removes the other entries for x's folder.
func (r *registry) dropOthers(x *installed) {
	out := r.paks[:0]
	for _, p := range r.paks {
		if p == x || !strings.EqualFold(p.Name, x.Name) || !strings.EqualFold(p.Type, x.Type) {
			out = append(out, p)
		}
	}
	r.paks = out
}

func (r *registry) put(x *installed) {
	if have := r.match(x); have != nil {
		*have = *x
		return
	}
	r.paks = append(r.paks, x)
}

func (r *registry) remove(x *installed) {
	out := r.paks[:0]
	for _, p := range r.paks {
		if p != x {
			out = append(out, p)
		}
	}
	r.paks = out
}

func (e *env) saveRegistry(r *registry) error {
	sort.Slice(r.paks, func(i, j int) bool {
		return strings.ToLower(r.paks[i].Name) < strings.ToLower(r.paks[j].Name)
	})
	data, err := json.MarshalIndent(r.paks, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(e.registryFile(), append(data, '\n'))
}

// entry finds the catalogue entry of an installed pak (id, then repository).
func (c *catalog) entry(x *installed) (catPak, bool) {
	if p, ok := c.byID(x.ID); ok {
		return p, true
	}
	if x.RepoURL == "" {
		return catPak{}, false
	}
	for _, p := range c.paks {
		if p.RepoURL == x.RepoURL {
			return p, true
		}
		for _, u := range p.PrevRepoURLs {
			if u == x.RepoURL {
				return p, true
			}
		}
	}
	return catPak{}, false
}

// pakPath locates a pak folder: <Tools|Emus>/<platform>/<name>.pak, or its
// hidden form <name>.pak.disabled. It returns the path, whether it is the
// hidden one, and whether it exists (when not, the visible path).
func (e *env) pakPath(typ, name string) (string, bool, bool) {
	root := e.tools
	if strings.EqualFold(typ, "EMU") {
		root = e.emus
	}
	p := filepath.Join(root, name+".pak")
	if isDir(p) {
		return p, false, true
	}
	if isDir(p + ".disabled") {
		return p + ".disabled", true, true
	}
	return p, false, false
}

// diskVersion reads "version" from the pak's own pak.json.
func diskVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "pak.json"))
	if err != nil {
		return ""
	}
	var v struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &v) != nil {
		return ""
	}
	return v.Version
}

// version is the best known installed version: registry or pak.json.
func (e *env) version(x *installed) string {
	v := x.Version
	if dir, _, ok := e.pakPath(x.Type, x.Name); ok {
		if d := diskVersion(dir); d != "" && cmpVersion(d, v) > 0 {
			v = d
		}
	}
	return v
}
