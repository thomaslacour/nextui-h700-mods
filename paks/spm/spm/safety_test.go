package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func savedFixture(t *testing.T) (*Env, string) {
	t.Helper()
	e := testEnv(t)
	write(t, filepath.Join(e.saves(), "GB", "A.gb.sav"), "original A")
	write(t, filepath.Join(e.saves(), "GB", "B.gb.sav"), "original B")
	from := t.TempDir()
	if _, err := e.backupApply(from); err != nil {
		t.Fatal(err)
	}
	return e, from
}

func contents(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRestoreRefusesEveryUnsafeManifestBeforeAnyWrite(t *testing.T) {
	for _, bad := range []string{"../outside.sav", "/Saves/GB/A.sav", "Saves/GB/../A.sav", `Saves\GB\A.sav`,
		".userdata/h700/.hooks/boot.d/A.sav", "Roms/Game (GB)/A.gb", "Saves/GB/.hidden.sav", "Saves/GB/A\n.sav"} {
		t.Run(bad, func(t *testing.T) {
			e, from := savedFixture(t)
			m, err := readManifest(from)
			if err != nil {
				t.Fatal(err)
			}
			m.Files[0].Rel = bad
			data, _ := json.Marshal(m)
			write(t, filepath.Join(from, "manifest.json"), string(data))
			if _, err := e.restoreApply(from); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
			if got := contents(t, filepath.Join(e.saves(), "GB", "A.gb.sav")); got != "original A" {
				t.Fatal(got)
			}
			if _, err := os.Stat(filepath.Join(from, ".spm-restore-backup")); !os.IsNotExist(err) {
				t.Fatal("wrote before validation")
			}
		})
	}
}

func TestManifestRejectsDuplicatesChecksumsSizesAndVersions(t *testing.T) {
	_, from := savedFixture(t)
	m, err := readManifest(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Manifest){
		func(m *Manifest) { m.Version = 99 },
		func(m *Manifest) { m.Files[0].Size = -1 },
		func(m *Manifest) { m.Files[0].Sha256 = "invalid" },
		func(m *Manifest) { m.Files = append(m.Files, m.Files[0]) },
		func(m *Manifest) { f := m.Files[0]; f.Rel = "saves/GB/A.gb.sav"; m.Files = append(m.Files, f) },
	} {
		copy := *m
		copy.Files = append([]BackupFile(nil), m.Files...)
		change(&copy)
		if err := validateManifest(copy); err == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
}

func TestRestorePreflightRejectsCorruptLaterFileWithoutChangingFirst(t *testing.T) {
	e, from := savedFixture(t)
	write(t, filepath.Join(e.saves(), "GB", "A.gb.sav"), "new A")
	write(t, filepath.Join(e.saves(), "GB", "B.gb.sav"), "new B")
	write(t, filepath.Join(from, "Saves", "GB", "B.gb.sav"), "corrupt")
	if _, err := e.restoreApply(from); err == nil {
		t.Fatal("corrupt backup restored")
	}
	for _, name := range []string{"A", "B"} {
		if got := contents(t, filepath.Join(e.saves(), "GB", name+".gb.sav")); got != "new "+name {
			t.Fatal(got)
		}
	}
}

func TestRestoreRejectsSourceAndTargetSymlinks(t *testing.T) {
	for _, source := range []bool{false, true} {
		t.Run(map[bool]string{false: "target", true: "source"}[source], func(t *testing.T) {
			e, from := savedFixture(t)
			outside := filepath.Join(t.TempDir(), "outside.sav")
			write(t, outside, "original A")
			root := e.SD
			if source {
				root = from
			}
			file := filepath.Join(root, "Saves", "GB", "A.gb.sav")
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, file); err != nil {
				t.Fatal(err)
			}
			if _, err := e.restoreApply(from); err == nil {
				t.Fatal("symlink accepted")
			}
			if got := contents(t, outside); got != "original A" {
				t.Fatal(got)
			}
		})
	}
}

func TestBackupDestinationCannotAliasSDOrOverwriteSnapshot(t *testing.T) {
	e, from := savedFixture(t)
	before := contents(t, filepath.Join(from, "manifest.json"))
	if _, err := e.backupApply(from); err == nil {
		t.Fatal("existing snapshot overwritten")
	}
	if contents(t, filepath.Join(from, "manifest.json")) != before {
		t.Fatal("manifest changed")
	}
	inside := filepath.Join(e.SD, "backup")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(inside, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := e.backupPlan(alias); err == nil {
		t.Fatal("aliased SD destination accepted")
	}
}

func TestBackupIncludesSrmStateAndRtcButNotLookalikes(t *testing.T) {
	e := testEnv(t)
	for _, rel := range []string{"Saves/GB/Game.srm", "Saves/GBC/Game.rtc", "Saves/GBA/Game.state",
		".userdata/shared/GB-core/Game.state1", ".userdata/shared/GB-core/Game.state.auto",
		".userdata/shared/GB-core/Game.st0notes", "Saves/GB/._Apple.sav"} {
		write(t, filepath.Join(e.SD, filepath.FromSlash(rel)), "data")
	}
	files, err := e.backupFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 5 {
		t.Fatalf("wrong coverage: %+v", files)
	}
}

func TestBackupRefusesUnknownFreeSpaceAndRunningGame(t *testing.T) {
	e := testEnv(t)
	write(t, filepath.Join(e.saves(), "GB", "Game.sav"), "save")
	dest := t.TempDir()
	oldFree, oldIdle := diskFree, checkGameIdle
	t.Cleanup(func() { diskFree, checkGameIdle = oldFree, oldIdle })
	diskFree = func(string) (uint64, error) { return 0, errors.New("unavailable") }
	if _, err := e.backupApply(dest); err == nil {
		t.Fatal("unknown free space accepted")
	}
	diskFree = oldFree
	checkGameIdle = func() error { return errors.New("game running") }
	if _, err := e.backupApply(dest); err == nil {
		t.Fatal("backup during game accepted")
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Fatal("wrote rejected backup")
	}
}

func TestRestoreKeepsVerifiedOriginalsAndJournal(t *testing.T) {
	e, from := savedFixture(t)
	write(t, filepath.Join(e.saves(), "GB", "A.gb.sav"), "new A")
	plan, err := e.restoreApply(from)
	if err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(plan.Rollback)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyManifest(plan.Rollback, *m); err != nil {
		t.Fatal(err)
	}
	if got := contents(t, filepath.Join(plan.Rollback, "Saves", "GB", "A.gb.sav")); got != "new A" {
		t.Fatal(got)
	}
	var journal struct {
		Done      bool
		Completed []string
	}
	if err := json.Unmarshal([]byte(contents(t, filepath.Join(plan.Rollback, "restore-progress.json"))), &journal); err != nil {
		t.Fatal(err)
	}
	if !journal.Done || len(journal.Completed) != 2 || !plan.Applied {
		t.Fatalf("bad journal: %+v", journal)
	}
}

func TestRestoreRefusesConcurrentTargetChange(t *testing.T) {
	e, from := savedFixture(t)
	old := checkGameIdle
	t.Cleanup(func() { checkGameIdle = old })
	calls := 0
	checkGameIdle = func() error {
		calls++
		if calls == 2 {
			write(t, filepath.Join(e.saves(), "GB", "A.gb.sav"), "concurrent progress")
		}
		return nil
	}
	if _, err := e.restoreApply(from); err == nil {
		t.Fatal("concurrent change overwritten")
	}
	if got := contents(t, filepath.Join(e.saves(), "GB", "A.gb.sav")); got != "concurrent progress" {
		t.Fatal(got)
	}
}

func TestTransferFlagsAreExplicitAndRejectConflicts(t *testing.T) {
	for _, args := range [][]string{{"--to", "/backup"}, {"-n", "--to=/backup"}, {"--to", "/backup", "--dry-run"}} {
		o, err := parseTransferOptions("backup", args)
		if err != nil || o.Apply {
			t.Fatalf("%v: %+v %v", args, o, err)
		}
	}
	o, err := parseTransferOptions("backup", []string{"--apply", "--to", "/backup", "--json"})
	if err != nil || !o.Apply || !o.JSON {
		t.Fatalf("%+v %v", o, err)
	}
	for _, args := range [][]string{{"--apply", "--dry-run", "--to", "/backup"}, {"--apply", "-n", "--to", "/backup"},
		{"--yes", "--to", "/backup"}, {"--aply", "--to", "/backup"}, {"--to", "/backup", "unexpected"}} {
		if _, err := parseTransferOptions("backup", args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := parseTransferOptions("verify", []string{"--from", "/backup", "--apply"}); err == nil {
		t.Fatal("verify must never accept apply")
	}
}

func TestRequiredMountNeverFallsBackToDirectory(t *testing.T) {
	e := testEnv(t)
	dest := t.TempDir()
	e.BackupMount = dest
	if err := e.checkDest(dest); err == nil {
		t.Fatal("plain directory counted as mounted storage")
	}
	old := mountedAt
	t.Cleanup(func() { mountedAt = old })
	mountedAt = func(string) (bool, error) { return true, nil }
	if err := e.checkDest(dest); err != nil {
		t.Fatal(err)
	}
	if err := e.checkDest(t.TempDir()); err == nil {
		t.Fatal("folder outside required mount accepted")
	}
}

func TestRestoreDryRunIsReadOnly(t *testing.T) {
	e, from := savedFixture(t)
	before, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.restorePlan(from); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(from)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("dry run wrote to the backup")
	}
	if _, err := os.Stat(filepath.Join(e.shared(), ".spm-restore.lock")); !os.IsNotExist(err) {
		t.Fatal("dry run created lock")
	}
}

func TestBackupDoesNotPublishManifestIfNewSaveAppears(t *testing.T) {
	e := testEnv(t)
	write(t, filepath.Join(e.saves(), "GB", "A.sav"), "original")
	old := checkGameIdle
	t.Cleanup(func() { checkGameIdle = old })
	calls := 0
	checkGameIdle = func() error {
		calls++
		if calls == 2 {
			write(t, filepath.Join(e.saves(), "GB", "New.sav"), "new save")
		}
		return nil
	}
	dest := t.TempDir()
	if _, err := e.backupApply(dest); err == nil {
		t.Fatal("changing inventory declared complete")
	}
	if _, err := os.Stat(filepath.Join(dest, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("completion marker published")
	}
}

func TestRestoreRespectsExistingDashboardLock(t *testing.T) {
	e, from := savedFixture(t)
	lock := filepath.Join(e.SD, ".userdata/h700/.browser-save-lock")
	if err := os.MkdirAll(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.restoreApply(from); err == nil {
		t.Fatal("dashboard lock ignored")
	}
	if info, err := os.Stat(lock); err != nil || !info.IsDir() {
		t.Fatal("another process's lock removed")
	}
	if contents(t, filepath.Join(e.saves(), "GB", "A.gb.sav")) != "original A" {
		t.Fatal("locked save changed")
	}
}

func TestCopyRechecksTargetAfterStaging(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "source"), filepath.Join(root, "target")
	write(t, src, "restored")
	write(t, dst, "current")
	expected, err := hashFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := copyVerified(src, dst, expected, true, func() error { return errors.New("target changed") }); err == nil {
		t.Fatal("commit guard ignored")
	}
	if contents(t, dst) != "current" {
		t.Fatal("target overwritten despite failed guard")
	}
	partials, _ := filepath.Glob(filepath.Join(root, ".spm-copy-*"))
	if len(partials) != 0 {
		t.Fatal("own temporary file not cleaned up")
	}
}

func TestRelativeSDRootCannotBypassDestinationGuard(t *testing.T) {
	e := testEnv(t)
	inside := filepath.Join(e.SD, "backup")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	e.SD, err = filepath.Rel(cwd, e.SD)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.checkDest(inside); err == nil {
		t.Fatal("relative source bypassed guard")
	}
}
