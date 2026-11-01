package main

// history.go — read NextUI's play tracker (game_logs.sqlite) for list, search
// and audit. Read-only, from a copy made by the caller if the console is busy.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// HistoryGame is one ROM's aggregated play history.
type HistoryGame struct {
	Path     string // Roms-relative file_path, "" when unknown
	Name     string
	System   string
	Time     int64 // total seconds
	Sessions int
	Last     int64 // latest session start/update, Unix
	First    int64
}

func asInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

// history reads game_logs.sqlite and aggregates play_activity per ROM.
func (e *Env) history() ([]HistoryGame, error) {
	db, err := openSQLite(e.gameLogs())
	if err != nil {
		return nil, err
	}
	roms, err := db.tableRows("rom")
	if err != nil {
		return nil, fmt.Errorf("rom table: %w", err)
	}
	acts, err := db.tableRows("play_activity")
	if err != nil {
		return nil, fmt.Errorf("play_activity table: %w", err)
	}
	type agg struct{ time, sessions, last, first int64 }
	byID := map[int64]*agg{}
	for _, a := range acts {
		id := asInt(a["rom_id"])
		cur := byID[id]
		if cur == nil {
			cur = &agg{}
			byID[id] = cur
		}
		t := asInt(a["play_time"])
		created, updated := asInt(a["created_at"]), asInt(a["updated_at"])
		when := updated
		if when == 0 {
			when = created
		}
		cur.time += t
		cur.sessions++
		if cur.last == 0 || when > cur.last {
			cur.last = when
		}
		if cur.first == 0 || (created != 0 && created < cur.first) {
			cur.first = created
		}
	}
	out := make([]HistoryGame, 0, len(roms))
	for _, r := range roms {
		// rom.id is INTEGER PRIMARY KEY: its value is the rowid.
		id := asInt(r["rowid"])
		cur := byID[id]
		if cur == nil {
			continue // tracked but never played (or all sessions purged)
		}
		fp, _ := r["file_path"].(string)
		tracked, _ := r["name"].(string)
		if tracked == "" {
			tracked = displayName(fp)
		}
		out = append(out, HistoryGame{
			Path: fp, Name: tracked, System: systemName(systemFolder(fp)),
			Time: cur.time, Sessions: int(cur.sessions), Last: cur.last, First: cur.first})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last > out[j].Last })
	return out, nil
}

// systemFolder is the "Game Boy (GB)" part of a file_path.
func systemFolder(fp string) string {
	dir := filepath.Dir(fp)
	if dir == "." || dir == "" {
		return ""
	}
	return filepath.Base(dir)
}

// historyAudit finds tracker rows whose ROM no longer exists, plus a pending
// WAL that means uncommitted sessions would be missed.
type historyAudit struct {
	Games   int
	Missing []HistoryGame
	WAL     bool
}

func (e *Env) auditHistory() (*historyAudit, error) {
	rows, err := e.history()
	if err != nil {
		return nil, err
	}
	res := &historyAudit{Games: len(rows)}
	for _, h := range rows {
		if h.Path == "" {
			res.Missing = append(res.Missing, h)
			continue
		}
		if _, err := os.Stat(filepath.Join(e.roms(), h.Path)); os.IsNotExist(err) {
			res.Missing = append(res.Missing, h)
		}
	}
	if info, err := os.Stat(e.gameLogs() + "-wal"); err == nil && info.Size() > 0 {
		res.WAL = true
	}
	return res, nil
}

func duration(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", h, m)
}

func unixLabel(t int64) string {
	if t == 0 {
		return "—"
	}
	return time.Unix(t, 0).Format("2006-01-02 15:04")
}

func searchHistory(rows []HistoryGame, words []string) []HistoryGame {
	var out []HistoryGame
	for _, h := range rows {
		hay := strings.ToLower(h.Name + " " + h.System + " " + h.Path)
		match := true
		for _, w := range words {
			if !strings.Contains(hay, strings.ToLower(w)) {
				match = false
				break
			}
		}
		if match {
			out = append(out, h)
		}
	}
	return out
}
