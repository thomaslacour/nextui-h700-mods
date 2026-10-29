// Save Sync H700: conservative Steam Cloud <-> PortMaster bridge.
// Uses Aurelia's patched root tokens, but maintains its own per-file baseline.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const defaultBase = "/mnt/SDCARD/.userdata/h700/save-sync"

type Game struct {
	Name     string
	AppID    string
	LocalDir string
	Token    string
	Subdir   string
	Files    []string
	Enabled  bool
}
type Result struct {
	Status            string            `json:"status"`
	Downloaded        []string          `json:"downloaded"`
	Uploaded          []string          `json:"uploaded"`
	Failed            []json.RawMessage `json:"failed"`
	Conflicts         []json.RawMessage `json:"conflicts"`
	Skipped           []json.RawMessage `json:"skipped"`
	SkippedRootTokens []json.RawMessage `json:"skipped_root_tokens"`
}
type Engine struct {
	base, aurelia   string
	apply           bool
	logfile, report string
	lines           []string
	errorCount      int
	// Résolution manuelle d'un conflit : seul le jeu preferGame suit la
	// préférence ("cloud" ou "local") ; les autres jeux suivent les règles.
	preferGame, prefer string
}

var idRx = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
var nameRx = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

const usage = "Usage: save-sync-engine --check|--apply [--prefer cloud|local --game NAME]"

func main() {
	e, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err := e.run(); err != nil {
		fmt.Fprintln(os.Stderr, "Save Sync:", err)
		os.Exit(1)
	}
}

func parseArgs(args []string) (*Engine, error) {
	base := os.Getenv("SAVE_SYNC_BASE")
	if base == "" {
		base = defaultBase
	}
	e := &Engine{base: base, aurelia: filepath.Join(base, "bin/aurelia"), logfile: filepath.Join(base, "scheduler/events.log"), report: filepath.Join(base, "reports/07-last-check.txt")}
	mode := ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--check", "--apply":
			if mode != "" {
				return nil, errors.New("mode")
			}
			mode = a
		case "--game", "--prefer":
			if i+1 >= len(args) {
				return nil, errors.New("value")
			}
			i++
			if a == "--game" {
				e.preferGame = args[i]
			} else {
				e.prefer = args[i]
			}
		default:
			return nil, errors.New("arg")
		}
	}
	e.apply = mode == "--apply"
	if mode == "" || (e.prefer == "") != (e.preferGame == "") {
		return nil, errors.New("args")
	}
	if e.prefer != "" && (!e.apply || (e.prefer != "cloud" && e.prefer != "local") || !idRx.MatchString(e.preferGame)) {
		return nil, errors.New("prefer")
	}
	return e, nil
}

// lock prend le verrou d'exécution. Un verrou laissé par un processus mort
// (coupure, crash) est repris au lieu de bloquer toutes les synchros suivantes.
func (e *Engine) lock() (func(), error) {
	path := filepath.Join(e.base, "scheduler/live-sync.lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		b, _ := os.ReadFile(path)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 && syscall.Kill(pid, 0) == nil {
			break
		}
		os.RemoveAll(path) // verrou orphelin (ou ancien verrou-répertoire)
	}
	return nil, errors.New("ALREADY_RUNNING")
}

func (e *Engine) run() error {
	for _, sub := range []string{"scheduler", "reports", "state", "staging", "backups"} {
		if err := os.MkdirAll(filepath.Join(e.base, sub), 0700); err != nil {
			return err
		}
	}
	unlock, err := e.lock()
	if err != nil {
		return err
	}
	defer unlock()
	// Keep scheduled logs bounded on small SD cards.
	if st, err := os.Stat(e.logfile); err == nil && st.Size() > 262144 {
		os.Rename(e.logfile, e.logfile+".1")
	}
	stamp := time.Now().Format("2006-01-02T15:04:05-0700")
	mode := "CHECK ONLY"
	if e.apply {
		mode = "SYNC ENABLED"
	}
	if e.prefer != "" {
		mode += " / KEEP " + strings.ToUpper(e.prefer) + " FOR " + e.preferGame
	}
	e.lines = append(e.lines, "SAVE SYNC / LAST SCHEDULED CHECK", "Time: "+stamp, "Mode: "+mode, "")
	e.log("scheduler", "START", strings.ReplaceAll(mode, " ", "_"))
	data, err := os.ReadFile(filepath.Join(e.base, "configuration/games-sync.conf"))
	if err != nil {
		e.note("scheduler", "FAIL", "GAMES_CONFIG_MISSING")
		e.finish()
		return err
	}
	games, err := parseGames(string(data))
	if err != nil {
		e.note("scheduler", "FAIL", "GAMES_CONFIG_INVALID")
		e.finish()
		return err
	}
	for _, g := range games {
		if !g.Enabled {
			e.note(g.Name, "SKIP", "DISABLED_IN_CONFIG")
			continue
		}
		if err := e.syncGame(g); err != nil {
			e.errorCount++
			e.note(g.Name, "FAIL", safeReason(err))
		}
	}
	e.finish()
	if e.errorCount > 0 {
		return fmt.Errorf("%d game(s) require attention; see %s", e.errorCount, e.report)
	}
	return nil
}
func safeReason(err error) string {
	s := err.Error()
	if s == "CLOUD_ACCOUNT_PATH_MISSING" {
		return s
	}
	// Never print arbitrary Steam responses or paths to logs.
	for _, code := range []string{"WIFI_DISCONNECTED", "NO_DEFAULT_ROUTE", "AURELIA_MISSING", "LOCAL_DIR_MISSING", "GAME_RUNNING", "STEAM_TIMEOUT", "STEAM_AUTH_REQUIRED", "STEAM_NETWORK_ERROR", "STEAM_RESPONSE_INVALID", "STEAM_TRANSFER_FAILED", "STEAM_VERIFY_FAILED", "REMOTE_FILE_MISSING", "STORAGE_ERROR", "STAGE_ERROR", "UNSAFE_MAPPING", "CONFLICT_BOTH_CHANGED", "INITIAL_MISMATCH_MANUAL_REVIEW", "FIRST_UPLOAD_MANUAL_ONLY", "REMOTE_DELETED_MANUAL_REVIEW", "MISSING_BASELINE", "CONFIG_ERROR"} {
		if strings.Contains(s, code) {
			return code
		}
	}
	return "UNKNOWN_ERROR_CHECK_MAC_TERMINAL"
}
func (e *Engine) note(game, status, msg string) {
	when := time.Now().Format("2006-01-02T15:04:05-0700")
	line := fmt.Sprintf("%s | %s | %s | %s\n", when, game, status, msg)
	f, err := os.OpenFile(e.logfile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err == nil {
		f.WriteString(line)
		f.Close()
	}
	e.lines = append(e.lines, game+": "+status+" "+msg)
	fmt.Print(game, ": ", status, " ", msg, "\n")
}
func (e *Engine) log(g, st, msg string) {
	f, err := os.OpenFile(e.logfile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err == nil {
		fmt.Fprintf(f, "%s | %s | %s | %s\n", time.Now().Format("2006-01-02T15:04:05-0700"), g, st, msg)
		f.Close()
	}
}
func (e *Engine) finish() {
	e.log("scheduler", "FINISH", fmt.Sprintf("ERRORS_%d", e.errorCount))
	if f, err := os.OpenFile(e.report+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600); err == nil {
		f.WriteString(strings.Join(e.lines, "\n") + "\n")
		f.Close()
		os.Rename(e.report+".tmp", e.report)
	}
}
func parseGames(raw string) ([]Game, error) {
	var out []Game
	for idx, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "|")
		if len(cols) != 7 {
			return nil, fmt.Errorf("CONFIG_ERROR line %d", idx+1)
		}
		for i := range cols {
			cols[i] = strings.TrimSpace(cols[i])
		}
		g := Game{Name: cols[0], AppID: cols[1], LocalDir: cols[2], Token: cols[3], Subdir: cols[4], Files: strings.Split(cols[5], ","), Enabled: cols[6] == "1"}
		if !idRx.MatchString(g.Name) {
			return nil, errors.New("CONFIG_ERROR game name")
		}
		if _, err := strconv.ParseUint(g.AppID, 10, 32); err != nil {
			return nil, errors.New("CONFIG_ERROR appID")
		}
		if g.Token != "GameInstall" && g.Token != "WinAppDataLocal" && g.Token != "WinAppDataRoaming" && g.Token != "WinMyDocuments" {
			return nil, errors.New("CONFIG_ERROR token")
		}
		if g.Subdir == "" || filepath.IsAbs(g.Subdir) || strings.Contains(g.Subdir, "\\") || filepath.Clean(g.Subdir) != g.Subdir || strings.HasPrefix(g.Subdir, "..") {
			return nil, errors.New("CONFIG_ERROR subdir")
		}
		if strings.ContainsAny(g.Subdir, "{}") && (g.Token != "WinMyDocuments" || g.Subdir != "Chasm/{64BitSteamID}") {
			return nil, errors.New("CONFIG_ERROR account template")
		}
		if filepath.Clean(g.LocalDir) != g.LocalDir || !filepath.IsAbs(g.LocalDir) {
			return nil, errors.New("CONFIG_ERROR local dir")
		}
		if os.Getenv("SAVE_SYNC_TEST_ALLOW_ANY_PATH") != "1" && !strings.HasPrefix(g.LocalDir, "/mnt/SDCARD/Roms/Ports (PORTS)/.ports/") {
			return nil, errors.New("CONFIG_ERROR unsafe local path")
		}
		if len(g.Files) == 0 {
			return nil, errors.New("CONFIG_ERROR files")
		}
		unique := map[string]bool{}
		for i, f := range g.Files {
			f = strings.TrimSpace(f)
			if !nameRx.MatchString(f) || strings.HasPrefix(f, ".") || unique[f] {
				return nil, errors.New("CONFIG_ERROR file name")
			}
			unique[f] = true
			g.Files[i] = f
		}
		out = append(out, g)
	}
	return out, nil
}
func pathExists(f string) bool { s, err := os.Stat(f); return err == nil && s.Mode().IsRegular() }
func hash(f string) (string, error) {
	h := sha256.New()
	fd, err := os.Open(f)
	if err != nil {
		return "", err
	}
	defer fd.Close()
	if _, err = io.Copy(h, fd); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func safeCopy(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	temp := dst + ".tmp." + strconv.Itoa(os.Getpid())
	os.Remove(temp)
	out, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, f)
	if syncerr := out.Sync(); err == nil {
		err = syncerr
	}
	if closeerr := out.Close(); err == nil {
		err = closeerr
	}
	if err != nil {
		os.Remove(temp)
		return err
	}
	orig, _ := hash(src)
	copyhash, _ := hash(temp)
	if orig == "" || orig != copyhash {
		os.Remove(temp)
		return errors.New("STORAGE_ERROR hash mismatch")
	}
	if st, err := os.Stat(src); err == nil {
		os.Chtimes(temp, st.ModTime(), st.ModTime())
	}
	if err = os.Rename(temp, dst); err != nil {
		os.Remove(temp)
		return err
	}
	return nil
}
func hasInternet() string {
	if os.Getenv("SAVE_SYNC_TEST_SKIP_NET") == "1" {
		return ""
	}
	i, err := net.InterfaceByName("wlan0")
	if err != nil || i.Flags&net.FlagUp == 0 {
		return "WIFI_DISCONNECTED"
	}
	addrs, err := i.Addrs()
	if err != nil || len(addrs) == 0 {
		return "WIFI_DISCONNECTED"
	}
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "NO_DEFAULT_ROUTE"
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[0] == "wlan0" && f[1] == "00000000" {
			return ""
		}
	}
	return "NO_DEFAULT_ROUTE"
}
func anyGameRunning() bool {
	if os.Getenv("SAVE_SYNC_TEST_SKIP_PROC") == "1" {
		return false
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	for _, ent := range entries {
		if _, err := strconv.Atoi(ent.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + ent.Name() + "/comm")
		if err != nil {
			continue
		}
		n := strings.ToLower(strings.TrimSpace(string(b)))
		if strings.Contains(n, "celeste") || strings.Contains(n, "voidstranger") || strings.Contains(n, "gmloader") || n == "mono" || n == "mono-sgen" {
			return true
		}
	}
	return false
}
func (e *Engine) aureliaCall(game Game, root string, cmd string) (Result, error) {
	var out Result
	args := []string{"--json", "cloud", "sync", game.AppID, "--" + cmd, "--path", filepath.Join(root, "unmapped")}
	// Documents is Aurelia's mirror, not the game's local save directory. An
	// aborted first upload may leave stale files here. Refresh only this mirror
	// from Cloud; the engine still resolves conflicts against real local saves.
	if game.Name == "chasm" && game.AppID == "312200" && cmd == "down" {
		args = append(args, "--resolve", "cloud")
	}
	buf, err := e.aureliaOutput(game, root, args)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(buf, &out); err != nil || out.Status != "ok" {
		return out, errors.New("STEAM_RESPONSE_INVALID")
	}
	if len(out.Failed) > 0 || len(out.Conflicts) > 0 || len(out.SkippedRootTokens) > 0 {
		return out, errors.New("STEAM_TRANSFER_FAILED")
	}
	return out, nil
}

func (e *Engine) aureliaBinary(g Game) string {
	if g.Name == "chasm" && g.AppID == "312200" {
		return filepath.Join(filepath.Dir(e.aurelia), "aurelia-chasm")
	}
	return e.aurelia
}

// Keep all Steam calls inside the scheduler's engine. Never log raw responses:
// Cloud filenames may contain the account's SteamID.
func (e *Engine) aureliaOutput(game Game, root string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 85*time.Second)
	defer cancel()
	call := exec.CommandContext(ctx, e.aureliaBinary(game), args...)
	// AURELIA_NO_DAEMON: otherwise the call is served by a background
	// `aurelia daemon` that kept the environment of the first call after boot
	// (its AURELIA_H700_GAMEINSTALL), so every GameInstall game would be
	// downloaded into that first game's staging directory (verified on device
	// 22/10/2026: Super Meat Boy landed in staging/live-celeste/down).
	env := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=/root", "AURELIA_NO_DAEMON=1", "AURELIA_CONFIG_DIR=" + filepath.Join(e.base, "config")}
	if game.Token == "GameInstall" {
		env = append(env, "AURELIA_H700_GAMEINSTALL="+root)
	} else {
		env = append(env, "AURELIA_H700_WINAPPDATALOCAL="+root)
	}
	if os.Getenv("SAVE_SYNC_TEST_ALLOW_ANY_PATH") == "1" {
		env = append(env, "MOCK_CLOUD="+os.Getenv("MOCK_CLOUD"))
	}
	call.Env = env
	buf, err := call.Output()
	if ctx.Err() != nil {
		return nil, errors.New("STEAM_TIMEOUT")
	}
	if err != nil {
		if ex, ok := err.(*exec.ExitError); ok {
			s := strings.ToLower(string(ex.Stderr))
			if strings.Contains(s, "auth") || strings.Contains(s, "login") || strings.Contains(s, "session") {
				return nil, errors.New("STEAM_AUTH_REQUIRED")
			}
			if strings.Contains(s, "network") || strings.Contains(s, "resolve") || strings.Contains(s, "connection") {
				return nil, errors.New("STEAM_NETWORK_ERROR")
			}
		}
		return nil, errors.New("STEAM_TRANSFER_FAILED")
	}
	return buf, nil
}

// Chasm's Steam build stores saves directly under Chasm/<SteamID>, whereas
// its DRM-free PortMaster build uses Chasm/Saves. Resolve the private directory
// from the authenticated Cloud listing, never from session.json or a stale
// Wine mirror. Refuse absent or ambiguous mappings before any sync/transfer.
func resolveChasmAccount(g Game, names []string) (Game, error) {
	if g.Subdir != "Chasm/{64BitSteamID}" || g.Token != "WinMyDocuments" {
		return g, errors.New("UNSAFE_MAPPING")
	}
	prefix := "%WinMyDocuments%Chasm/"
	account := ""
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(name, prefix), "/")
		if len(parts) != 2 || len(parts[0]) != 17 || strings.Trim(parts[0], "0123456789") != "" {
			continue
		}
		tracked := false
		for _, f := range g.Files {
			tracked = tracked || parts[1] == f
		}
		if !tracked {
			continue
		}
		if account != "" && account != parts[0] {
			return g, errors.New("UNSAFE_MAPPING")
		}
		account = parts[0]
	}
	if account == "" {
		return g, errors.New("CLOUD_ACCOUNT_PATH_MISSING")
	}
	g.Subdir = "Chasm/" + account
	return g, nil
}

func (e *Engine) resolveAccount(g Game) (Game, error) {
	buf, err := e.aureliaOutput(g, "", []string{"--json", "cloud", "list", g.AppID})
	if err != nil {
		return g, err
	}
	var listing struct {
		AppID json.Number `json:"app_id"`
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	if json.Unmarshal(buf, &listing) != nil || listing.AppID.String() != g.AppID {
		return g, errors.New("STEAM_RESPONSE_INVALID")
	}
	// A first upload has no Cloud filename to identify the account. Ask Aurelia
	// for the authenticated identity, consume ONLY steam_id in memory, and never
	// print/cache the account response (it also contains personal information).
	if len(listing.Files) == 0 {
		buf, err = e.aureliaOutput(g, "", []string{"--json", "account"})
		if err != nil {
			return g, err
		}
		return resolveEmptyChasmAccount(g, buf)
	}
	var names []string
	for _, f := range listing.Files {
		names = append(names, f.Filename)
	}
	return resolveChasmAccount(g, names)
}

func resolveEmptyChasmAccount(g Game, buf []byte) (Game, error) {
	var identity struct {
		SteamID json.RawMessage `json:"steam_id"`
	}
	if json.Unmarshal(buf, &identity) != nil {
		return g, errors.New("STEAM_RESPONSE_INVALID")
	}
	id := string(identity.SteamID)
	if strings.HasPrefix(id, "\"") {
		if json.Unmarshal(identity.SteamID, &id) != nil {
			return g, errors.New("STEAM_RESPONSE_INVALID")
		}
	}
	if len(id) != 17 || strings.Trim(id, "0123456789") != "" {
		return g, errors.New("STEAM_RESPONSE_INVALID")
	}
	return resolveChasmAccount(g, []string{"%WinMyDocuments%Chasm/" + id + "/UserInfo.cfg"})
}

// Without the daemon (AURELIA_NO_DAEMON, see aureliaCall) Aurelia honors
// AURELIA_H700_GAMEINSTALL and AURELIA_H700_WINAPPDATALOCAL, so %GameInstall%
// and %WinAppDataLocal% files land in the per-run staging root (verified on
// device 22/10/2026: Gato Roboto, Super Meat Boy). %WinAppDataRoaming% has no
// override: Aurelia materializes it inside its shared Wine prefix, which the
// engine treats as the remote mirror (Anodyne, verified 21/10 and 22/10/2026).
// The earlier "Local is ignored" observation came from the daemon serving
// calls with the environment of the first call after boot.
func (e *Engine) appDataRoot(sub string) string {
	return filepath.Join(e.base, "config", "master_steam_prefix", "pfx",
		"drive_c", "users", "steamuser", "AppData", sub)
}

// remoteRoot is the directory Aurelia reads cloud files from / writes them to,
// given the staging root the caller would ideally use.
func (e *Engine) remoteRoot(g Game, stagingRoot string) string {
	switch g.Token {
	case "WinAppDataRoaming":
		return e.appDataRoot("Roaming")
	case "WinMyDocuments":
		return filepath.Join(e.base, "config", "master_steam_prefix", "pfx", "drive_c", "users", "steamuser", "Documents")
	}
	return stagingRoot
}

// remoteFile is Aurelia's mirrored copy of cloud file f, wherever it lands.
func (e *Engine) remoteFile(g Game, stagingRoot, f string) string {
	return filepath.Join(e.remoteRoot(g, stagingRoot), g.Subdir, f)
}

// decide choisit l'action pour un fichier suivi, à partir des empreintes
// locale (lh), Cloud (rh) et de la dernière référence commune (bl) ; une
// empreinte vide = fichier absent / pas de référence. Sans préférence, tout cas
// ambigu est refusé. Avec prefer ("cloud" ou "local", choix explicite de
// l'utilisateur pour un jeu), le côté choisi gagne — jamais de suppression :
// garder un côté absent reste refusé.
func decide(lh, rh, bl, prefer string) (string, error) {
	switch {
	case lh != "" && lh == rh:
		return "baseline", nil
	case lh == "":
		return "down", nil
	case rh == "" && prefer == "local":
		return "up", nil
	case rh == "" && bl != "":
		return "", errors.New("REMOTE_DELETED_MANUAL_REVIEW")
	case rh == "":
		return "", errors.New("FIRST_UPLOAD_MANUAL_ONLY")
	case prefer == "cloud":
		return "down", nil
	case prefer == "local":
		return "up", nil
	case bl == "":
		return "", errors.New("INITIAL_MISMATCH_MANUAL_REVIEW")
	case lh == bl:
		return "down", nil
	case rh == bl:
		return "up", nil
	}
	return "", errors.New("CONFLICT_BOTH_CHANGED")
}

func (e *Engine) syncGame(g Game) error {
	if code := hasInternet(); code != "" {
		return errors.New(code)
	}
	if !pathExists(e.aureliaBinary(g)) {
		return errors.New("AURELIA_MISSING")
	}
	if anyGameRunning() {
		return errors.New("GAME_RUNNING")
	}
	if fi, err := os.Stat(g.LocalDir); err != nil || !fi.IsDir() {
		return errors.New("LOCAL_DIR_MISSING")
	}
	if strings.Contains(g.Subdir, "{64BitSteamID}") {
		var err error
		g, err = e.resolveAccount(g)
		if err != nil {
			return err
		}
	}
	work := filepath.Join(e.base, "staging", "live-"+g.Name)
	// This directory is dedicated to our script, and never stores user-owned game data.
	if err := os.RemoveAll(work); err != nil {
		return errors.New("STAGE_ERROR")
	}
	defer os.RemoveAll(work)
	downRoot := filepath.Join(work, "down")
	if err := os.MkdirAll(downRoot, 0700); err != nil {
		return errors.New("STAGE_ERROR")
	}
	if _, err := e.aureliaCall(g, downRoot, "down"); err != nil {
		return err
	}
	// Import only explicit allowlisted save names from the temporary download.
	stateDir := filepath.Join(e.base, "state", g.Name)
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return errors.New("STORAGE_ERROR")
	}
	type action struct{ file, kind, local, remote, localSHA, remoteSHA string }
	var actions []action
	for _, f := range g.Files {
		lp := filepath.Join(g.LocalDir, f)
		rp := e.remoteFile(g, downRoot, f)
		localExists, remoteExists := pathExists(lp), pathExists(rp)
		if !localExists && !remoteExists {
			continue
		}
		var lh, rh string
		var err error
		if localExists {
			lh, err = hash(lp)
			if err != nil {
				return errors.New("STORAGE_ERROR")
			}
		}
		if remoteExists {
			rh, err = hash(rp)
			if err != nil {
				return errors.New("STORAGE_ERROR")
			}
		}
		baselinePath := filepath.Join(stateDir, f+".sha256")
		bas, _ := os.ReadFile(baselinePath)
		bl := strings.TrimSpace(string(bas))
		if len(bl) != 64 {
			bl = ""
		}
		prefer := ""
		if g.Name == e.preferGame {
			prefer = e.prefer
		}
		kind, err := decide(lh, rh, bl, prefer)
		if err != nil {
			return err
		}
		actions = append(actions, action{file: f, kind: kind, local: lp, remote: rp, localSHA: lh, remoteSHA: rh})
	}
	if len(actions) == 0 {
		e.note(g.Name, "OK", "NO_TRACKED_FILES")
		return nil
	}
	uploadCount, downCount := 0, 0
	for _, a := range actions {
		if a.kind == "up" {
			uploadCount++
		}
		if a.kind == "down" {
			downCount++
		}
	}
	if !e.apply {
		if downCount == 0 && uploadCount == 0 {
			e.note(g.Name, "OK", "IN_SYNC")
			return nil
		}
		e.note(g.Name, "OK", fmt.Sprintf("DRY_RUN_DOWN_%d_UP_%d", downCount, uploadCount))
		return nil
	}
	snap := time.Now().Format("20060102-150405") + "-" + strconv.Itoa(os.Getpid())
	bdir := filepath.Join(e.base, "backups", g.Name, snap)
	// For every potentially changing file, preserve both local and last known remote versions.
	for _, a := range actions {
		if a.kind == "baseline" {
			continue
		}
		if err := os.MkdirAll(bdir, 0700); err != nil {
			return errors.New("STORAGE_ERROR")
		}
		if pathExists(a.local) {
			if err := safeCopy(a.local, filepath.Join(bdir, "local-"+a.file)); err != nil {
				return errors.New("STORAGE_ERROR")
			}
		}
		if pathExists(a.remote) {
			if err := safeCopy(a.remote, filepath.Join(bdir, "cloud-"+a.file)); err != nil {
				return errors.New("STORAGE_ERROR")
			}
		}
	}
	// Once all changes were checked for conflicts and safety copies made, apply them.
	for _, a := range actions {
		stateFile := filepath.Join(stateDir, a.file+".sha256")
		switch a.kind {
		case "baseline":
			if err := writeBaseline(stateFile, a.localSHA); err != nil {
				return errors.New("STORAGE_ERROR")
			}
		case "down":
			if err := safeCopy(a.remote, a.local); err != nil {
				return errors.New("STORAGE_ERROR")
			}
			if err := writeBaseline(stateFile, a.remoteSHA); err != nil {
				return errors.New("STORAGE_ERROR")
			}
			e.note(g.Name, "OK", "DOWN_"+a.file)
		case "up":
			if code := hasInternet(); code != "" {
				return errors.New(code)
			}
			// Refetch Cloud just before upload; detect another device's concurrent edit.
			beforeRoot := filepath.Join(work, "before-upload-"+a.file)
			if err := os.MkdirAll(beforeRoot, 0700); err != nil {
				return errors.New("STAGE_ERROR")
			}
			if _, err := e.aureliaCall(g, beforeRoot, "down"); err != nil {
				return err
			}
			fresh := e.remoteFile(g, beforeRoot, a.file)
			hh := "" // absent du Cloud : attendu pour un premier envoi
			if pathExists(fresh) {
				h, err := hash(fresh)
				if err != nil {
					return errors.New("STORAGE_ERROR")
				}
				hh = h
			}
			if hh != a.remoteSHA {
				return errors.New("CONFLICT_BOTH_CHANGED")
			}
			current, err := hash(a.local)
			if err != nil || current != a.localSHA {
				return errors.New("GAME_RUNNING")
			}
			upRoot := filepath.Join(work, "upload-"+a.file)
			if err := safeCopy(a.local, e.remoteFile(g, upRoot, a.file)); err != nil {
				return errors.New("STORAGE_ERROR")
			}
			outcome, err := e.aureliaCall(g, upRoot, "up")
			if err != nil {
				return err
			}
			remoteName := "%" + g.Token + "%" + g.Subdir + "/" + a.file
			found := false
			for _, n := range outcome.Uploaded {
				if n == remoteName {
					found = true
				} else {
					return errors.New("STEAM_VERIFY_FAILED")
				}
			}
			if !found {
				return errors.New("STEAM_VERIFY_FAILED")
			}
			verifyRoot := filepath.Join(work, "verify-"+a.file)
			if err := os.MkdirAll(verifyRoot, 0700); err != nil {
				return errors.New("STAGE_ERROR")
			}
			if _, err := e.aureliaCall(g, verifyRoot, "down"); err != nil {
				return err
			}
			check, err := hash(e.remoteFile(g, verifyRoot, a.file))
			if err != nil || check != a.localSHA {
				return errors.New("STEAM_VERIFY_FAILED")
			}
			if err := writeBaseline(stateFile, a.localSHA); err != nil {
				return errors.New("STORAGE_ERROR")
			}
			e.note(g.Name, "OK", "UP_"+a.file)
		}
	}
	if downCount == 0 && uploadCount == 0 {
		e.note(g.Name, "OK", "IN_SYNC")
	}
	return nil
}
func writeBaseline(dst, sha string) error {
	if len(sha) != 64 {
		return errors.New("invalid sha")
	}
	temp := dst + ".tmp"
	if err := os.WriteFile(temp, []byte(sha+"\n"), 0600); err != nil {
		return err
	}
	return os.Rename(temp, dst)
}
