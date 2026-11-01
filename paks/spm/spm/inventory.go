package main

// inventory.go — walk the SD card once and cross-reference ROMs, in-game
// saves, save states and box art by tag + file name. Read-only: nothing here
// writes to the card.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// SaveFile is one in-game save (SRAM), Save/<TAG>/<rom>.sav.
type SaveFile struct {
	Path string
	Size int64
}

// State is one save state, .userdata/shared/<TAG>-<core>/<rom>.st<n>.
type State struct {
	Slot int
	Path string
	Size int64
}

// Game is one ROM entry plus the saves and box art that match it by name.
type Game struct {
	Path   string // absolute ROM path
	System string // "Game Boy"
	Tag    string // "GB"
	Name   string // display name
	Kind   string // "rom" or "port"
	Save   *SaveFile
	States []State
	Art    string // absolute box-art path, "" if none
}

// key joins a tag and a file name the way saves/states/art are looked up.
func key(tag, name string) string { return tag + "/" + name }

// stem is the file name without its final extension.
func stem(path string) string {
	base := filepath.Base(path)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		return base[:i]
	}
	return base
}

func inventoryDir(path string, optional bool) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if optional && os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("inventory path is not a real directory: %s", path)
	}
	return os.ReadDir(path)
}

// scanROMs lists every game NextUI shows: files right inside Roms/<folder>/.
// Hidden files and folders (.media, .discs), non-game files and launchers are
// left out, matching the dashboard's own game list.
func (e *Env) scanROMs() ([]*Game, error) {
	root := e.roms()
	dirs, err := inventoryDir(root, false)
	if err != nil {
		return nil, err
	}
	var games []*Game
	for _, de := range dirs {
		if !de.IsDir() || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		folder := de.Name()
		tag := systemTag(folder)
		sys := systemName(folder)
		files, err := inventoryDir(filepath.Join(root, folder), false)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.Type()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("ROM inventory contains a symlink: %s", filepath.Join(root, folder, f.Name()))
			}
			if f.IsDir() || strings.HasPrefix(f.Name(), ".") {
				continue
			}
			name := f.Name()
			if skipExt[strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))] {
				continue
			}
			if isLauncher(folder, name) {
				continue
			}
			kind := "rom"
			if tag == "PORTS" || sys == "Ports" {
				kind = "port"
			}
			games = append(games, &Game{Path: filepath.Join(root, folder, name),
				System: sys, Tag: tag, Name: displayName(name), Kind: kind})
		}
	}
	sort.Slice(games, func(i, j int) bool { return strings.ToLower(games[i].Name) < strings.ToLower(games[j].Name) })
	return games, nil
}

// scanSaves returns every in-game save, keyed by tag + ROM file name.
func (e *Env) scanSaves() (map[string]*SaveFile, error) {
	saves := map[string]*SaveFile{}
	dirs, err := inventoryDir(e.saves(), true)
	if err != nil {
		if os.IsNotExist(err) {
			return saves, nil
		}
		return nil, err
	}
	for _, tagDir := range dirs {
		if tagDir.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("save folder is a symlink: %s", tagDir.Name())
		}
		if !tagDir.IsDir() {
			continue
		}
		files, err := inventoryDir(filepath.Join(e.saves(), tagDir.Name()), false)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.Type()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("save is a symlink: %s", f.Name())
			}
			if f.IsDir() || strings.HasPrefix(f.Name(), "._") || !strings.HasSuffix(f.Name(), ".sav") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				return nil, err
			}
			rom := strings.TrimSuffix(f.Name(), ".sav")
			saves[key(tagDir.Name(), rom)] = &SaveFile{
				Path: filepath.Join(e.saves(), tagDir.Name(), f.Name()), Size: info.Size()}
		}
	}
	return saves, nil
}

// scanStates returns save states grouped by tag + ROM file name. The core in
// "<TAG>-<core>" varies per system; only the TAG prefix identifies the ROM.
func (e *Env) scanStates() (map[string][]State, error) {
	states := map[string][]State{}
	dirs, err := inventoryDir(e.shared(), true)
	if err != nil {
		if os.IsNotExist(err) {
			return states, nil
		}
		return nil, err
	}
	for _, coreDir := range dirs {
		if coreDir.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("state folder is a symlink: %s", coreDir.Name())
		}
		if !coreDir.IsDir() {
			continue
		}
		if strings.HasPrefix(coreDir.Name(), ".") || !strings.Contains(coreDir.Name(), "-") {
			continue
		}
		tag := strings.SplitN(coreDir.Name(), "-", 2)[0]
		files, err := inventoryDir(filepath.Join(e.shared(), coreDir.Name()), false)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.Type()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("state is a symlink: %s", f.Name())
			}
			name := f.Name()
			if f.IsDir() || strings.HasPrefix(name, "._") {
				continue
			}
			i := strings.LastIndex(name, ".st")
			if i < 0 {
				continue
			}
			slot, err := strconv.Atoi(name[i+3:])
			if err != nil || slot < 0 || slot > 9 {
				continue
			}
			info, err := f.Info()
			if err != nil {
				return nil, err
			}
			rom := name[:i]
			states[key(tag, rom)] = append(states[key(tag, rom)], State{
				Slot: slot, Path: filepath.Join(e.shared(), coreDir.Name(), name), Size: info.Size()})
		}
	}
	for k := range states {
		sort.Slice(states[k], func(i, j int) bool { return states[k][i].Slot < states[k][j].Slot })
	}
	return states, nil
}

// scanArt returns box art by folder + ROM stem: Roms/<folder>/.media/<stem>.png.
func (e *Env) scanArt() (map[string]string, error) {
	art := map[string]string{}
	dirs, err := inventoryDir(e.roms(), false)
	if err != nil {
		return nil, err
	}
	for _, de := range dirs {
		if !de.IsDir() || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		files, err := inventoryDir(filepath.Join(e.roms(), de.Name(), ".media"), true)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, f := range files {
			if f.Type()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("box art is a symlink: %s", f.Name())
			}
			if f.IsDir() || !strings.EqualFold(filepath.Ext(f.Name()), ".png") {
				continue
			}
			k := key(de.Name(), stem(f.Name()))
			if art[k] != "" {
				return nil, fmt.Errorf("ambiguous box art: %s", k)
			}
			art[k] = filepath.Join(e.roms(), de.Name(), ".media", f.Name())
		}
	}
	return art, nil
}

type Inventory struct {
	Games  []*Game
	Saves  map[string]*SaveFile
	States map[string][]State
	Art    map[string]string
}

// Read each source once. An incomplete scan is an error, not proof of orphans.
func (e *Env) inventory() (*Inventory, error) {
	games, err := e.scanROMs()
	if err != nil {
		return nil, err
	}
	saves, err := e.scanSaves()
	if err != nil {
		return nil, err
	}
	states, err := e.scanStates()
	if err != nil {
		return nil, err
	}
	art, err := e.scanArt()
	if err != nil {
		return nil, err
	}
	owners := map[string]string{}
	for _, g := range games {
		k := key(g.Tag, filepath.Base(g.Path))
		if old := owners[k]; old != "" {
			return nil, fmt.Errorf("ambiguous ROM/save identity %s: %s and %s", k, old, g.Path)
		}
		owners[k] = g.Path
		g.Save = saves[k]
		g.States = states[k]
		g.Art = art[key(filepath.Base(filepath.Dir(g.Path)), stem(g.Path))]
	}
	return &Inventory{Games: games, Saves: saves, States: states, Art: art}, nil
}

func (e *Env) collect() ([]*Game, error) {
	inv, err := e.inventory()
	if err != nil {
		return nil, err
	}
	return inv.Games, nil
}
