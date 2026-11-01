package main

// backup.go — copy every in-game save, RTC sidecar and save state to an
// external folder with a SHA-256 manifest. Like clean/fix, `spm backup` is a
// dry run by default; --apply performs it. The destination is never allowed
// inside the SD card, and free space is checked before the first write.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var diskFree = statfsFree // overridden in tests

// BackupFile is one save file in the manifest, relative to the SD root.
type BackupFile struct {
	Rel    string `json:"rel"`
	Size   int64  `json:"size"`
	Sha256 string `json:"sha256"`
}

// Manifest is what `spm backup` writes and `spm restore` reads back.
type Manifest struct {
	Version   int          `json:"version"`
	CreatedAt string       `json:"created_at"`
	Source    string       `json:"source"`
	Files     []BackupFile `json:"files"`
}

// isWithin reports whether path is base itself or inside it.
func isWithin(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// checkDest rejects empty, relative or self-referential destinations.
func (e *Env) checkDest(dest string) error {
	if dest == "" {
		return errors.New("a destination is required (--to / --from)")
	}
	if !filepath.IsAbs(dest) {
		return errors.New("destination must be an absolute path")
	}
	sd, err := filepath.Abs(e.SD)
	if err != nil {
		return err
	}
	sd, err = filepath.EvalSymlinks(sd)
	if err != nil {
		return fmt.Errorf("SD root: %w", err)
	}
	realDest, err := filepath.EvalSymlinks(dest)
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	if isWithin(sd, realDest) || isWithin(realDest, sd) {
		return errors.New("destination must be outside the SD card")
	}
	info, err := os.Stat(dest)
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	if !info.IsDir() {
		return errors.New("destination is not a directory")
	}
	if e.BackupMount != "" {
		if err := requireMount(realDest, e.BackupMount); err != nil {
			return err
		}
	}
	return nil
}

// backupFiles uses the same allowlist as restore. Missing optional roots are
// normal; read failures are not silently turned into a successful empty backup.
func (e *Env) backupFiles() ([]BackupFile, error) {
	var files []BackupFile
	walk := func(root string) error {
		info, err := os.Lstat(root)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("save root is not a real directory: %s", root)
		}
		return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing symlink in save tree: %s", p)
			}
			if d.IsDir() {
				if p != root && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(e.SD, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !validSavePath(rel) {
				return nil
			}
			file, err := guardedPath(e.SD, rel)
			if err != nil {
				return err
			}
			stamp, err := hashFile(file)
			if err != nil {
				return err
			}
			stamp.Rel = rel
			files = append(files, stamp)
			return nil
		})
	}
	if err := walk(e.saves()); err != nil {
		return nil, err
	}
	if err := walk(e.shared()); err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return files, nil
}

// backupPlan checks the destination and lists what would be copied.
type backupPlan struct {
	Applied    bool         `json:"applied"`
	Dest       string       `json:"dest"`
	Files      []BackupFile `json:"files"`
	TotalBytes int64        `json:"total_bytes"`
	FreeBytes  uint64       `json:"free_bytes"`
	Enough     bool         `json:"enough_space"`
}

func (e *Env) backupPlan(dest string) (*backupPlan, error) {
	if err := e.checkDest(dest); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return nil, err
	}
	if len(entries) != 0 {
		return nil, errors.New("backup destination must be empty; use a new snapshot directory")
	}
	files, err := e.backupFiles()
	if err != nil {
		return nil, err
	}
	if len(files) > 0 {
		if err := validateManifest(Manifest{Version: 1, Files: files}); err != nil {
			return nil, err
		}
	}
	plan := &backupPlan{Dest: dest, Files: files}
	for _, f := range files {
		plan.TotalBytes += f.Size
		if plan.TotalBytes < 0 {
			return nil, errors.New("save size overflow")
		}
	}
	free, err := diskFree(dest)
	if err != nil {
		return nil, fmt.Errorf("cannot determine free space: %w", err)
	}
	plan.FreeBytes = free
	plan.Enough = free >= spaceReserve && uint64(plan.TotalBytes) <= free-spaceReserve
	return plan, nil
}

// copyVerified streams into a unique temporary file, verifies its size/hash
// against the plan and checks commit guards before replacing the destination.
func copyVerified(src, dst string, expected BackupFile, replace bool, beforeCommit ...func() error) error {
	if info, err := os.Lstat(dst); err == nil {
		if !replace || !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to overwrite %s", dst)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), ".spm-copy-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	actual, err := hashFile(tmp)
	if err != nil {
		return err
	}
	if actual.Size != expected.Size || actual.Sha256 != expected.Sha256 {
		return fmt.Errorf("source changed or copy corrupted: %s", src)
	}
	if _, err := os.Lstat(dst); err == nil && !replace {
		return fmt.Errorf("destination appeared during copy: %s", dst)
	}
	for _, check := range beforeCommit {
		if err := check(); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dst)
}

// backupApply copies every save and writes the manifest, then verifies it.
func (e *Env) backupApply(dest string) (*backupPlan, error) {
	if err := checkGameIdle(); err != nil {
		return nil, err
	}
	plan, err := e.backupPlan(dest)
	if err != nil {
		return nil, err
	}
	if !plan.Enough {
		return nil, fmt.Errorf("not enough free space: need %s, %s free", humanBytes(plan.TotalBytes), humanBytes(int64(plan.FreeBytes)))
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return nil, err
	}
	if len(entries) != 0 {
		return nil, errors.New("backup destination must be empty; use a new snapshot directory")
	}
	if len(plan.Files) == 0 {
		return nil, errors.New("no save files to back up")
	}
	lock := filepath.Join(dest, ".spm-backup.lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		return nil, fmt.Errorf("backup already in progress: %w", err)
	}
	defer os.Remove(lock)
	manifest := Manifest{Version: 1, CreatedAt: time.Now().Format(time.RFC3339), Source: e.SD}
	for _, f := range plan.Files {
		if err := e.checkDest(dest); err != nil {
			return nil, err
		}
		src, err := guardedPath(e.SD, f.Rel)
		if err != nil {
			return nil, err
		}
		dst, err := guardedPath(dest, f.Rel)
		if err != nil {
			return nil, err
		}
		if err := copyVerified(src, dst, f, false); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Rel, err)
		}
		manifest.Files = append(manifest.Files, f)
	}
	if err := checkGameIdle(); err != nil {
		return nil, err
	}
	// Recheck originals before publishing the completion marker.
	current, err := e.backupFiles()
	if err != nil {
		return nil, err
	}
	if len(current) != len(plan.Files) {
		return nil, errors.New("save inventory changed during backup")
	}
	for i, file := range current {
		if file != plan.Files[i] {
			return nil, fmt.Errorf("save inventory/content changed during backup: %s", file.Rel)
		}
	}
	if err := verifyManifest(dest, manifest); err != nil {
		return nil, err
	}
	if err := checkGameIdle(); err != nil {
		return nil, err
	}
	if err := writeManifest(dest, manifest); err != nil {
		return nil, err
	}
	plan.Applied = true
	return plan, nil
}

func writeManifest(dest string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return errors.New("backup manifest too large")
	}
	return atomicMetadata(dest, "manifest.json", append(data, '\n'), false)
}

// verifyManifest re-hashes every copied file and compares it to the manifest.
func verifyManifest(dest string, m Manifest) error {
	if err := validateManifest(m); err != nil {
		return err
	}
	for _, f := range m.Files {
		if err := verifyFile(dest, f); err != nil {
			return fmt.Errorf("verifying %s: %w", f.Rel, err)
		}
	}
	return nil
}
