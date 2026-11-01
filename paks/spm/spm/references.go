package main

// Descriptor references are observed, never rewritten here. This graph makes
// name-fix plans show which playlists/track sheets would also need changing.
import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

type PlanIssue struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type FileReference struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Value  string `json:"value"`
	Target string `json:"target"`
}

type ReferenceGraph struct {
	Incoming map[string][]FileReference
	Issues   []PlanIssue
}

var cueFileLine = regexp.MustCompile(`(?i)^FILE\s+(?:"([^"]+)"|(\S+))\s+\S+\s*$`)

func (e *Env) references(games []*Game) ReferenceGraph {
	graph := ReferenceGraph{Incoming: map[string][]FileReference{}}
	visited := map[string]int{}
	var visit func(string, string, int)
	issue := func(code, file, message string) { graph.Issues = append(graph.Issues, PlanIssue{code, file, message}) }
	visit = func(file, root string, depth int) {
		ext := strings.ToLower(filepath.Ext(file))
		if ext != ".m3u" && ext != ".cue" {
			return
		}
		if visited[file] == 1 {
			issue("reference_cycle", file, "cyclic disc descriptors")
			return
		}
		if visited[file] == 2 {
			return
		}
		if depth > 32 || len(visited) >= 4096 {
			issue("reference_limit", file, "descriptor graph exceeds safety limits")
			return
		}
		visited[file] = 1
		defer func() { visited[file] = 2 }()
		rel, err := filepath.Rel(e.SD, file)
		if err != nil {
			issue("descriptor_path", file, err.Error())
			return
		}
		checked, err := guardedLocalPath(e.SD, rel)
		if err != nil {
			issue("descriptor_path", file, err.Error())
			return
		}
		f, err := os.Open(checked)
		if err != nil {
			issue("descriptor_read", file, err.Error())
			return
		}
		data, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
		f.Close()
		if err != nil || len(data) > 256<<10 {
			issue("descriptor_read", file, "unreadable or oversized descriptor")
			return
		}
		if !utf8.Valid(data) {
			issue("descriptor_encoding", file, "non-UTF-8 descriptor; manual review required")
			return
		}
		text := strings.TrimPrefix(string(data), "\ufeff")
		for i, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			ref := line
			if ext == ".cue" {
				m := cueFileLine.FindStringSubmatch(line)
				if m == nil {
					if strings.HasPrefix(strings.ToUpper(line), "FILE") {
						issue("cue_syntax", file, fmt.Sprintf("unsupported FILE line %d", i+1))
					}
					continue
				}
				ref = m[1]
				if ref == "" {
					ref = m[2]
				}
			}
			if strings.ContainsAny(ref, "|\x00\r\n") || strings.Contains(ref, "://") {
				issue("reference_syntax", file, fmt.Sprintf("reference on line %d needs manual review", i+1))
				continue
			}
			native := filepath.FromSlash(strings.ReplaceAll(ref, `\`, "/"))
			target := native
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(file), target)
			}
			target = filepath.Clean(target)
			if !isWithin(root, target) {
				issue("reference_outside_system", file, "disc reference escapes the system folder")
				continue
			}
			targetRel, err := filepath.Rel(e.SD, target)
			if err != nil {
				issue("reference_path", file, err.Error())
				continue
			}
			checked, err := guardedLocalPath(e.SD, targetRel)
			if err != nil {
				issue("reference_path", file, err.Error())
				continue
			}
			info, err := os.Stat(checked)
			if err != nil || !info.Mode().IsRegular() {
				issue("reference_missing", file, fmt.Sprintf("missing/invalid reference on line %d: %s", i+1, ref))
				continue
			}
			graph.Incoming[target] = append(graph.Incoming[target], FileReference{File: file, Line: i + 1, Value: ref, Target: target})
			visit(target, root, depth+1)
		}
	}
	for _, game := range games {
		visit(game.Path, filepath.Dir(game.Path), 0)
	}
	return graph
}
