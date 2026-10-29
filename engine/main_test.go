package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChasmCloudRoundTrip(t *testing.T) {
	t.Setenv("SAVE_SYNC_TEST_ALLOW_ANY_PATH", "1")
	t.Setenv("SAVE_SYNC_TEST_SKIP_NET", "1")
	t.Setenv("SAVE_SYNC_TEST_SKIP_PROC", "1")
	base := t.TempDir()
	cloud := filepath.Join(base, "fake-cloud")
	local := filepath.Join(base, "local")
	t.Setenv("MOCK_CLOUD", cloud)
	for _, dir := range []string{cloud, local, filepath.Join(base, "scheduler")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	mock := filepath.Join(base, "aurelia-mock")
	script := `#!/bin/sh
set -eu
id=12345678901234567
mirror="$AURELIA_CONFIG_DIR/master_steam_prefix/pfx/drive_c/users/steamuser/Documents/Chasm/$id"
if [ "$2" = account ]; then
    printf '{"steam_id":"%s","email":"private@example.invalid"}\n' "$id"
elif [ "$3" = list ]; then
    printf '{"app_id":312200,"files":['
    sep=''
    for f in UserInfo.cfg slot0.sav; do
        if [ -f "$MOCK_CLOUD/$f" ]; then
            printf '%s{"filename":"%%WinMyDocuments%%Chasm/%s/%s"}' "$sep" "$id" "$f"
            sep=,
        fi
    done
    printf ']}\n'
elif [ "$5" = --down ]; then
    [ "$#" -eq 9 ] && [ "$8" = --resolve ] && [ "$9" = cloud ]
    mkdir -p "$mirror"
    for f in UserInfo.cfg slot0.sav; do
        [ ! -f "$MOCK_CLOUD/$f" ] || cp "$MOCK_CLOUD/$f" "$mirror/$f"
    done
    printf '{"status":"ok","downloaded":[],"uploaded":[]}\n'
elif [ "$5" = --up ]; then
    [ "$#" -eq 7 ]
    printf '{"status":"ok","uploaded":['
    sep=''
    for f in UserInfo.cfg slot0.sav; do
        if [ -f "$mirror/$f" ] && ! cmp -s "$mirror/$f" "$MOCK_CLOUD/$f"; then
            cp "$mirror/$f" "$MOCK_CLOUD/$f"
            printf '%s"%%WinMyDocuments%%Chasm/%s/%s"' "$sep" "$id" "$f"
            sep=,
        fi
    done
    printf ']}\n'
else exit 1
fi
`
	if err := os.WriteFile(mock, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "aurelia-chasm"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	put := func(dir, name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	get := func(dir, name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	put(local, "UserInfo.cfg", "progression")
	put(local, "slot0.sav", "console save")
	put(local, "config.ini", "machine settings")
	g := Game{Name: "chasm", AppID: "312200", LocalDir: local, Token: "WinMyDocuments", Subdir: "Chasm/{64BitSteamID}", Files: []string{"UserInfo.cfg", "slot0.sav"}}
	e := &Engine{base: base, aurelia: mock, logfile: filepath.Join(base, "scheduler/events.log")}
	if err := e.syncGame(g); err == nil || err.Error() != "FIRST_UPLOAD_MANUAL_ONLY" {
		t.Fatalf("first upload not protected: %v", err)
	}
	if pathExists(filepath.Join(cloud, "slot0.sav")) {
		t.Fatal("check uploaded a save")
	}
	e.apply = true
	e.preferGame = "chasm"
	e.prefer = "local"
	if err := e.syncGame(g); err != nil {
		t.Fatal(err)
	}
	if get(cloud, "slot0.sav") != "console save" {
		t.Fatal("first upload failed")
	}
	if pathExists(filepath.Join(cloud, "config.ini")) {
		t.Fatal("settings uploaded")
	}
	e.prefer = ""
	e.preferGame = ""
	put(cloud, "slot0.sav", "PC progressed")
	if err := e.syncGame(g); err != nil {
		t.Fatal(err)
	}
	if get(local, "slot0.sav") != "PC progressed" {
		t.Fatal("cloud update not restored")
	}
	put(local, "slot0.sav", "console progressed")
	if err := e.syncGame(g); err != nil {
		t.Fatal(err)
	}
	if get(cloud, "slot0.sav") != "console progressed" {
		t.Fatal("console update not uploaded")
	}
	put(local, "slot0.sav", "console conflict")
	put(cloud, "slot0.sav", "PC conflict")
	if err := e.syncGame(g); err == nil || err.Error() != "CONFLICT_BOTH_CHANGED" {
		t.Fatal("conflict not protected")
	}
	if get(local, "slot0.sav") != "console conflict" || get(cloud, "slot0.sav") != "PC conflict" {
		t.Fatal("conflict modified saves")
	}
	if get(local, "config.ini") != "machine settings" {
		t.Fatal("settings changed")
	}
	log := get(filepath.Join(base, "scheduler"), "events.log")
	if strings.Contains(log, "12345678901234567") || strings.Contains(log, "private@example.invalid") {
		t.Fatal("private account data logged")
	}
}

func TestAureliaClientIsolation(t *testing.T) {
	e := &Engine{aurelia: "/ss/bin/aurelia"}
	for _, g := range []Game{
		{Name: "celeste", AppID: "504230"}, {Name: "voidstranger", AppID: "2121980"},
		{Name: "anodyne", AppID: "234900"}, {Name: "gatoroboto", AppID: "916730"},
		{Name: "chasm-other", AppID: "312200"}, {Name: "chasm", AppID: "999"},
	} {
		if e.aureliaBinary(g) != e.aurelia {
			t.Fatal("existing client replaced")
		}
	}
	if e.aureliaBinary(Game{Name: "chasm", AppID: "312200"}) != "/ss/bin/aurelia-chasm" {
		t.Fatal("Chasm not isolated")
	}
}

func TestExistingClientArgumentsUnchanged(t *testing.T) {
	t.Setenv("SAVE_SYNC_TEST_ALLOW_ANY_PATH", "1")
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	t.Setenv("MOCK_CLOUD", argsFile)
	mock := filepath.Join(dir, "aurelia")
	script := `#!/bin/sh
set -eu
[ "$AURELIA_NO_DAEMON" = 1 ]
[ "$AURELIA_H700_GAMEINSTALL" = /staging ]
printf '%s\n' "$@" > "$MOCK_CLOUD"
printf '{"status":"ok"}\n'
`
	if err := os.WriteFile(mock, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	e := &Engine{base: dir, aurelia: mock}
	if _, err := e.aureliaCall(Game{Name: "celeste", AppID: "504230", Token: "GameInstall"}, "/staging", "down"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "--json\ncloud\nsync\n504230\n--down\n--path\n/staging/unmapped\n" {
		t.Fatal("existing CLI invocation changed")
	}
}

func TestDecide(t *testing.T) {
	const L, R, B = "local", "remote", "base"
	cases := []struct {
		name, lh, rh, bl, prefer, kind, err string
	}{
		{"identiques", L, L, "", "", "baseline", ""},
		{"absent en local", "", R, B, "", "down", ""},
		{"Cloud a changé", L, R, L, "", "down", ""},
		{"local a changé", L, R, R, "", "up", ""},
		{"les deux ont changé", L, R, B, "", "", "CONFLICT_BOTH_CHANGED"},
		{"première fois, écart", L, R, "", "", "", "INITIAL_MISMATCH_MANUAL_REVIEW"},
		{"Cloud vide", L, "", "", "", "", "FIRST_UPLOAD_MANUAL_ONLY"},
		{"supprimé du Cloud", L, "", B, "", "", "REMOTE_DELETED_MANUAL_REVIEW"},
		{"conflit, garder Cloud", L, R, B, "cloud", "down", ""},
		{"conflit, garder local", L, R, B, "local", "up", ""},
		{"première fois, garder Cloud", L, R, "", "cloud", "down", ""},
		{"Cloud vide, garder local", L, "", "", "local", "up", ""},
		{"Cloud vide, garder Cloud : jamais de suppression", L, "", "", "cloud", "", "FIRST_UPLOAD_MANUAL_ONLY"},
		{"identiques malgré la préférence", L, L, B, "cloud", "baseline", ""},
	}
	for _, c := range cases {
		kind, err := decide(c.lh, c.rh, c.bl, c.prefer)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if kind != c.kind || got != c.err {
			t.Errorf("%s : obtenu (%q, %q), attendu (%q, %q)", c.name, kind, got, c.kind, c.err)
		}
	}
}

func TestParseArgs(t *testing.T) {
	ok := [][]string{
		{"--check"},
		{"--apply"},
		{"--apply", "--prefer", "cloud", "--game", "celeste"},
		{"--game", "voidstranger", "--prefer", "local", "--apply"},
	}
	for _, a := range ok {
		if _, err := parseArgs(a); err != nil {
			t.Errorf("%v refusé : %v", a, err)
		}
	}
	bad := [][]string{
		{},
		{"--check", "--apply"},
		{"--check", "--prefer", "cloud", "--game", "celeste"}, // préférence sans --apply
		{"--apply", "--prefer", "cloud"},                      // sans jeu
		{"--apply", "--prefer", "both", "--game", "celeste"},
		{"--apply", "--prefer", "cloud", "--game", "../x"},
		{"--apply", "--game"},
	}
	for _, a := range bad {
		if _, err := parseArgs(a); err == nil {
			t.Errorf("%v accepté", a)
		}
	}
}

func TestParseGamesTokens(t *testing.T) {
	t.Setenv("SAVE_SYNC_TEST_ALLOW_ANY_PATH", "1")
	line := func(token string) string {
		return "anodyne|234900|/tmp/anodyne/Saves|" + token + "|AnodyneFNA/Saves|Save_1.dat,Save_2.dat|1"
	}
	for _, token := range []string{"GameInstall", "WinAppDataLocal", "WinAppDataRoaming", "WinMyDocuments"} {
		games, err := parseGames(line(token))
		if err != nil || len(games) != 1 || games[0].Token != token {
			t.Errorf("%s refusé : %v", token, err)
		}
	}
	if _, err := parseGames(line("WinSavedGames")); err == nil || err.Error() != "CONFIG_ERROR token" {
		t.Errorf("jeton non géré accepté : %v", err)
	}
}

func TestRemoteRoot(t *testing.T) {
	e := &Engine{base: "/ss"}
	prefix := filepath.Join("/ss", "config", "master_steam_prefix", "pfx", "drive_c", "users", "steamuser", "AppData")
	cases := map[string]string{
		"GameInstall":       "/staging",
		"WinAppDataLocal":   "/staging",
		"WinAppDataRoaming": filepath.Join(prefix, "Roaming"),
		"WinMyDocuments":    filepath.Join(prefix, "..", "Documents"),
	}
	for token, want := range cases {
		if got := e.remoteRoot(Game{Token: token}, "/staging"); got != want {
			t.Errorf("%s : %q, attendu %q", token, got, want)
		}
	}
	g := Game{Token: "WinAppDataRoaming", Subdir: "AnodyneFNA/Saves"}
	if got := e.remoteFile(g, "/staging", "Save_1.dat"); got != filepath.Join(prefix, "Roaming", "AnodyneFNA/Saves", "Save_1.dat") {
		t.Errorf("fichier Cloud Anodyne : %q", got)
	}
}

func TestChasmAccount(t *testing.T) {
	g := Game{Token: "WinMyDocuments", Subdir: "Chasm/{64BitSteamID}", Files: []string{"slot0.sav", "UserInfo.cfg"}}
	// Synthetic IDs only. Errors and reports must never include either value.
	const a, b = "12345678901234567", "23456789012345678"
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"one account", []string{"%WinMyDocuments%Chasm/" + a + "/slot0.sav", "%WinMyDocuments%Chasm/" + a + "/UserInfo.cfg"}, ""},
		{"empty cloud", nil, "CLOUD_ACCOUNT_PATH_MISSING"},
		{"settings not tracked", []string{"%WinMyDocuments%Chasm/" + a + "/config.ini"}, "CLOUD_ACCOUNT_PATH_MISSING"},
		{"wrong token", []string{"%GameInstall%Chasm/" + a + "/slot0.sav"}, "CLOUD_ACCOUNT_PATH_MISSING"},
		{"path traversal", []string{"%WinMyDocuments%Chasm/../slot0.sav"}, "CLOUD_ACCOUNT_PATH_MISSING"},
		{"invalid account", []string{"%WinMyDocuments%Chasm/1234567890123456x/slot0.sav"}, "CLOUD_ACCOUNT_PATH_MISSING"},
		{"extra directory", []string{"%WinMyDocuments%Chasm/" + a + "/Saves/slot0.sav"}, "CLOUD_ACCOUNT_PATH_MISSING"},
		{"ambiguous", []string{"%WinMyDocuments%Chasm/" + a + "/slot0.sav", "%WinMyDocuments%Chasm/" + b + "/slot0.sav"}, "UNSAFE_MAPPING"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resolved, err := resolveChasmAccount(g, c.files)
			if c.want == "" {
				if err != nil || resolved.Subdir != "Chasm/"+a {
					t.Fatal("incorrect mapping")
				}
			} else if err == nil || err.Error() != c.want || safeReason(err) != c.want {
				t.Fatal("unsafe or unredacted error")
			}
		})
	}
}

func TestChasmConfig(t *testing.T) {
	t.Setenv("SAVE_SYNC_TEST_ALLOW_ANY_PATH", "1")
	base := "chasm|312200|/tmp/chasm/Saves|"
	for _, tail := range []string{
		"WinMyDocuments|Chasm/{64BitSteamID}|slot0.sav,UserInfo.cfg|0",
		"WinMyDocuments|Chasm/12345678901234567|slot0.sav|1",
	} {
		if _, err := parseGames(base + tail); err != nil {
			t.Fatal(err)
		}
	}
	for _, tail := range []string{
		"GameInstall|Chasm/{64BitSteamID}|slot0.sav|1",
		"WinMyDocuments|Chasm/{account}|slot0.sav|1",
		"WinMyDocuments|Chasm/{64BitSteamID}/Saves|slot0.sav|1",
	} {
		if _, err := parseGames(base + tail); err == nil {
			t.Fatal("unsafe template accepted")
		}
	}
}

func TestEmptyChasmAccount(t *testing.T) {
	g := Game{Token: "WinMyDocuments", Subdir: "Chasm/{64BitSteamID}", Files: []string{"UserInfo.cfg"}}
	for _, data := range []string{
		`{"steam_id":12345678901234567,"email":"not-logged@example.invalid"}`,
		`{"steam_id":"12345678901234567"}`,
	} {
		resolved, err := resolveEmptyChasmAccount(g, []byte(data))
		if err != nil || resolved.Subdir != "Chasm/12345678901234567" {
			t.Fatal("identity not resolved")
		}
	}
	for _, data := range []string{`{}`, `{"steam_id":null}`, `{"steam_id":"../escape"}`, `{"steam_id":1}`, `not-json`} {
		if _, err := resolveEmptyChasmAccount(g, []byte(data)); err == nil || err.Error() != "STEAM_RESPONSE_INVALID" {
			t.Fatal("unsafe identity accepted")
		}
	}
}
