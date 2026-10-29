package main

// What the pak and port commands share: one binary, the mode is picked from
// the name it is called by (Updates.pak/pak, .userdata/h700/ports/port).
// Output, log, lock, HTTP and name lists live in env.go; this file adds the
// command line, tables, held lists and the doctor checks both run.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// mode is the command a binary name stands for: "port" for port, port.exe…,
// anything else is pak (the historical name).
func mode(arg0 string) string {
	base := strings.ToLower(filepath.Base(arg0))
	if base == "port" || strings.HasPrefix(base, "port.") {
		return "port"
	}
	return "pak"
}

func (e *env) userAgent() string { return "nextui-" + e.tool + "-cli" }

// parseArgs splits "-x"/"--name" flags from names. "--" ends the flags.
func parseArgs(args []string) (map[string]bool, []string) {
	flags := map[string]bool{}
	var names []string
	for i, a := range args {
		if a == "--" {
			names = append(names, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags[strings.TrimLeft(a, "-")] = true
		} else {
			names = append(names, a)
		}
	}
	return flags, names
}

// table prints aligned columns; the first row is the header.
func (e *env) table(rows [][]string) {
	w := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if n := len([]rune(c)); n > w[i] {
				w[i] = n
			}
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			if i == len(r)-1 {
				b.WriteString(c)
			} else {
				b.WriteString(c + strings.Repeat(" ", w[i]-len([]rune(c))+2))
			}
		}
		e.say("%s", strings.TrimRight(b.String(), " "))
	}
}

// cmdHold edits or prints <home>/held: what `<tool> upgrade` never touches.
func (e *env) cmdHold(hold bool, names []string) error {
	if hold && len(names) == 0 {
		for _, n := range readList(e.heldFile()) {
			e.say("%s", n)
		}
		return nil
	}
	header := fmt.Sprintf("# %ss never upgraded by `%s upgrade` (name or id, one per line)",
		strings.ToUpper(e.tool[:1])+e.tool[1:], e.tool)
	changed, err := editList(e.heldFile(), header, hold, names...)
	if err == nil && changed {
		e.log("%s: %s", map[bool]string{true: "hold", false: "unhold"}[hold], strings.Join(names, ", "))
	}
	return err
}

// matchAll: every word appears (case-insensitive) in one of the fields.
// The search of pak and port.
func matchAll(words []string, fields ...string) bool {
	hay := strings.ToLower(strings.Join(fields, "\n"))
	for _, w := range words {
		if !strings.Contains(hay, strings.ToLower(w)) {
			return false
		}
	}
	return true
}

func mb(n int64) string {
	if n <= 0 {
		return ""
	}
	if n < 1<<20 {
		return "<1 MB"
	}
	return fmt.Sprintf("%d MB", n>>20)
}

// --- doctor -----------------------------------------------------------------

// checker prints doctor lines (ok, warn, FAIL, note) and counts failures.
type checker struct {
	e     *env
	fails int
}

func (c *checker) ok(f string, a ...any)   { c.e.say("ok    "+f, a...) }
func (c *checker) warn(f string, a ...any) { c.e.say("warn  "+f, a...) }
func (c *checker) note(f string, a ...any) { c.e.say("note  "+f, a...) }
func (c *checker) bad(f string, a ...any)  { c.e.say("FAIL  "+f, a...); c.fails++ }

// done prints the summary; false when something failed.
func (c *checker) done() bool {
	if c.fails > 0 {
		c.e.say("%d problem(s) found", c.fails)
		return false
	}
	c.e.say("no problem found")
	return true
}

// writable checks that each folder exists (created if needed) and takes a file.
func (c *checker) writable(dirs ...string) {
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			c.bad("%s: %v", d, err)
			continue
		}
		probe := filepath.Join(d, "."+c.e.tool+"-write-test")
		if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
			c.bad("cannot write to %s: %v", d, err)
		} else {
			os.Remove(probe)
			c.ok("writable: %s", d)
		}
	}
}

// basics: free space on the SD card and a clock HTTPS can trust.
func (c *checker) basics() {
	if free, err := freeBytes(c.e.sd); err == nil {
		if free < 200<<20 {
			c.warn("only %d MB free on the SD card", free>>20)
		} else {
			c.ok("free space: %d MB", free>>20)
		}
	}
	if y := time.Now().Year(); y < 2025 {
		c.bad("the clock says %d: HTTPS will fail (set the date, or connect Wi-Fi for NTP)", y)
	}
}

// github checks the GitHub API (it does not count against the rate limit).
func (c *checker) github(okMsg, why string) bool {
	if _, err := c.e.get(c.e.githubAPI+"/rate_limit", 1); err != nil {
		c.warn("GitHub API unreachable (%v): %s", err, why)
		return false
	}
	c.ok("%s", okMsg)
	return true
}
