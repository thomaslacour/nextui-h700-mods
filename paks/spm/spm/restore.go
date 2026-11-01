package main

// restore.go — put a `spm backup` back onto the SD card. Dry-run by default;
// --apply first keeps any file it would overwrite in <from>/.spm-restore-backup/.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func readManifest(from string) (*Manifest, error) {
	file, err := guardedPath(from, "manifest.json")
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		return nil, fmt.Errorf("manifest: unreadable or too large")
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: invalid JSON")
	}
	if err := validateManifest(m); err != nil {
		return nil, err
	}
	return &m, nil
}

func validateManifest(m Manifest) error {
	if m.Version != 0 && m.Version != 1 {
		return fmt.Errorf("manifest: unsupported version %d", m.Version)
	}
	if len(m.Files) == 0 || len(m.Files) > 100000 {
		return fmt.Errorf("manifest: empty or excessive file list")
	}
	seen := map[string]bool{}
	var total uint64
	for _, f := range m.Files {
		key := strings.ToLower(f.Rel) // FAT/exFAT filenames are case-insensitive.
		if !validSavePath(f.Rel) || seen[key] {
			return fmt.Errorf("manifest: unsafe/duplicate save path %q", f.Rel)
		}
		seen[key] = true
		digest, err := hex.DecodeString(f.Sha256)
		if err != nil || len(digest) != 32 || f.Sha256 != strings.ToLower(f.Sha256) || f.Size < 0 {
			return fmt.Errorf("manifest: invalid size/checksum for %s", f.Rel)
		}
		total += uint64(f.Size)
		if total > uint64(1<<63-1) {
			return fmt.Errorf("manifest: total size overflow")
		}
	}
	return nil
}

func atomicMetadata(root, rel string, data []byte, replace bool) error {
	dest, err := guardedPath(root, rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil && !replace {
		return fmt.Errorf("metadata already exists: %s", dest)
	}
	f, err := os.CreateTemp(filepath.Dir(dest), ".spm-metadata-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), dest)
}

type restorePlan struct {
	Applied    bool         `json:"applied"`
	From       string       `json:"from"`
	Files      []BackupFile `json:"files"`
	Overwrites []string     `json:"overwrites"`
	Rollback   string       `json:"rollback,omitempty"`
	current    map[string]BackupFile
}

func (e *Env) restorePlan(from string) (*restorePlan, error) {
	if err := e.checkDest(from); err != nil {
		return nil, err
	}
	m, err := readManifest(from)
	if err != nil {
		return nil, err
	}
	// Validate EVERY source before permitting even the first SD write.
	if err := verifyManifest(from, *m); err != nil {
		return nil, err
	}
	plan := &restorePlan{From: from, Files: m.Files, current: map[string]BackupFile{}}
	for _, f := range m.Files {
		dest, err := guardedPath(e.SD, f.Rel)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(dest); err == nil {
			stamp, err := hashFile(dest)
			if err != nil {
				return nil, err
			}
			stamp.Rel = f.Rel
			plan.current[f.Rel] = stamp
			plan.Overwrites = append(plan.Overwrites, f.Rel)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	sort.Strings(plan.Overwrites)
	return plan, nil
}

func (e *Env) restoreApply(from string) (result *restorePlan, retErr error) {
	if err := checkGameIdle(); err != nil {
		return nil, err
	}
	plan, err := e.restorePlan(from)
	if err != nil {
		return nil, err
	}
	var bytes, oldBytes uint64
	for _, f := range plan.Files {
		bytes += uint64(f.Size)
	}
	for _, old := range plan.current {
		oldBytes += uint64(old.Size)
	}
	if _, err := requireSpace(e.SD, bytes); err != nil {
		return nil, err
	}
	if _, err := requireSpace(from, oldBytes); err != nil {
		return nil, err
	}
	lock, err := guardedPath(e.SD, ".userdata/shared/.spm-restore.lock")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		return nil, err
	}
	if err := os.Mkdir(lock, 0o700); err != nil {
		return nil, fmt.Errorf("restore locked; inspect any interrupted restore before retrying: %w", err)
	}
	defer os.Remove(lock)
	// Share the dashboard's device-side commit lock too. Its host-side Python
	// lock alone cannot protect a restore started from the console terminal.
	browserLock, err := guardedPath(e.SD, ".userdata/h700/.browser-save-lock")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(browserLock), 0o755); err != nil {
		return nil, err
	}
	if err := os.Mkdir(browserLock, 0o700); err != nil {
		return nil, fmt.Errorf("dashboard save transfer in progress or interrupted: %w", err)
	}
	defer os.Remove(browserLock)
	backupRoot, err := guardedPath(from, ".spm-restore-backup")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		return nil, err
	}
	backup, err := os.MkdirTemp(backupRoot, time.Now().Format("20060102-150405")+"-*")
	if err != nil {
		return nil, err
	}
	plan.Rollback = backup
	defer func() {
		if retErr != nil {
			retErr = fmt.Errorf("%w; recovery directory: %s", retErr, backup)
		}
	}()
	originals := Manifest{Version: 1, Source: e.SD, CreatedAt: time.Now().Format(time.RFC3339)}
	for _, rel := range plan.Overwrites {
		if err := e.checkDest(from); err != nil {
			return nil, err
		}
		old := plan.current[rel]
		src, err := guardedPath(e.SD, rel)
		if err != nil {
			return nil, err
		}
		dst, err := guardedPath(backup, rel)
		if err != nil {
			return nil, err
		}
		if err := copyVerified(src, dst, old, false); err != nil {
			return nil, fmt.Errorf("keeping %s before restore: %w", rel, err)
		}
		originals.Files = append(originals.Files, old)
	}
	if len(originals.Files) > 0 {
		if err := verifyManifest(backup, originals); err != nil {
			return nil, err
		}
	}
	if err := writeManifest(backup, originals); err != nil {
		return nil, err
	}
	progress := struct {
		Files     []BackupFile `json:"files"`
		Completed []string     `json:"completed"`
		Next      string       `json:"next,omitempty"`
		Done      bool         `json:"done"`
	}{Files: plan.Files}
	journal := func() error {
		data, err := json.MarshalIndent(progress, "", "  ")
		if err != nil {
			return err
		}
		return atomicMetadata(backup, "restore-progress.json", data, true)
	}
	for _, f := range plan.Files {
		if err := e.checkDest(from); err != nil {
			return nil, err
		}
		if err := checkGameIdle(); err != nil {
			return nil, err
		}
		src, err := guardedPath(from, f.Rel)
		if err != nil {
			return nil, err
		}
		dst, err := guardedPath(e.SD, f.Rel)
		if err != nil {
			return nil, err
		}
		old, replacing := plan.current[f.Rel]
		checkTarget := func() error {
			if err := e.checkDest(from); err != nil {
				return err
			}
			if err := checkGameIdle(); err != nil {
				return err
			}
			if _, err := guardedPath(e.SD, f.Rel); err != nil {
				return err
			}
			if replacing {
				if err := verifyFile(e.SD, old); err != nil {
					return fmt.Errorf("current save changed since planning: %w", err)
				}
			} else if _, err := os.Lstat(dst); !os.IsNotExist(err) {
				return fmt.Errorf("target appeared since planning: %s", f.Rel)
			}
			return nil
		}
		if err := checkTarget(); err != nil {
			return nil, err
		}
		progress.Next = f.Rel
		if err := journal(); err != nil {
			return nil, err
		}
		if err := copyVerified(src, dst, f, replacing, checkTarget); err != nil {
			return nil, fmt.Errorf("restoring %s: %w", f.Rel, err)
		}
		if err := verifyFile(e.SD, f); err != nil {
			return nil, err
		}
		progress.Completed = append(progress.Completed, f.Rel)
	}
	progress.Next, progress.Done = "", true
	if err := journal(); err != nil {
		return nil, err
	}
	plan.Applied = true
	return plan, nil
}
