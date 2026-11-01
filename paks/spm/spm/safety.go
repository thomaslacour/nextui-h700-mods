package main

// Shared path/content guards for backup and restore. A manifest is untrusted
// input, not a list of arbitrary paths we are allowed to overwrite.
import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
)

const spaceReserve = uint64(256 * 1024 * 1024)

var stateSuffix = regexp.MustCompile(`(?i)\.(st[0-9]|state([0-9]+|\.auto)?)$`)

func validSavePath(rel string) bool {
	if !validRelative(rel) {
		return false
	}
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		if strings.HasPrefix(part, ".") && part != ".userdata" {
			return false
		}
	}
	ext := strings.ToLower(path.Ext(rel))
	if len(parts) >= 3 && parts[0] == "Saves" {
		return ext == ".sav" || ext == ".srm" || ext == ".rtc" || stateSuffix.MatchString(rel)
	}
	return len(parts) >= 4 && parts[0] == ".userdata" && parts[1] == "shared" &&
		strings.Contains(parts[2], "-") && stateSuffix.MatchString(rel)
}

func validRelative(rel string) bool {
	return rel != "" && rel != "." && !strings.ContainsAny(rel, "\\:") &&
		!strings.ContainsFunc(rel, func(r rune) bool { return r < 32 || r == 127 }) &&
		!path.IsAbs(rel) && path.Clean(rel) == rel && rel != ".." && !strings.HasPrefix(rel, "../")
}

// Resolve the root (mount aliases are legitimate), but refuse symlinks or
// special files in its children. Missing tails are allowed for restore.
func guardedPath(root, rel string) (string, error) {
	if !validRelative(rel) {
		return "", fmt.Errorf("unsafe relative path %q", rel)
	}
	return guardedLocalPath(root, filepath.FromSlash(rel))
}

// For paths obtained by our own directory scan, not from a backup manifest:
// preserve legitimate native filename bytes such as ':' that fix names is
// meant to report. Traversal/symlink/special-file guards are still identical.
func guardedLocalPath(root, rel string) (string, error) {
	if rel == "" || rel == "." || filepath.IsAbs(rel) || filepath.Clean(rel) != rel ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("unsafe local path %q", rel)
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return "", err
	}
	current := base
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlink: %s", current)
		}
		if (i < len(parts)-1 && !info.IsDir()) || (!info.IsDir() && !info.Mode().IsRegular()) {
			return "", fmt.Errorf("not a regular file/directory: %s", current)
		}
	}
	return current, nil
}

func hashFile(file string) (BackupFile, error) {
	before, err := os.Lstat(file)
	if err != nil {
		return BackupFile{}, err
	}
	if !before.Mode().IsRegular() {
		return BackupFile{}, fmt.Errorf("not a regular file: %s", file)
	}
	f, err := os.Open(file)
	if err != nil {
		return BackupFile{}, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return BackupFile{}, err
	}
	after, err := os.Lstat(file)
	if err != nil || !os.SameFile(before, after) || before.Size() != size ||
		after.Size() != size || !before.ModTime().Equal(after.ModTime()) {
		return BackupFile{}, fmt.Errorf("file changed while reading: %s", file)
	}
	return BackupFile{Size: size, Sha256: hex.EncodeToString(h.Sum(nil))}, nil
}

func verifyFile(root string, expected BackupFile) error {
	file, err := guardedPath(root, expected.Rel)
	if err != nil {
		return err
	}
	actual, err := hashFile(file)
	if err != nil {
		return err
	}
	if actual.Size != expected.Size || actual.Sha256 != expected.Sha256 {
		return fmt.Errorf("checksum/size mismatch for %s", expected.Rel)
	}
	return nil
}

func requireSpace(dir string, bytes uint64) (uint64, error) {
	free, err := diskFree(dir)
	if err != nil {
		return 0, fmt.Errorf("cannot determine free space: %w", err)
	}
	if free < spaceReserve || bytes > free-spaceReserve {
		return free, fmt.Errorf("not enough free space in %s (256 MiB reserve required)", dir)
	}
	return free, nil
}

var mountedAt = func(dir string) (bool, error) {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/self/mountinfo")
		if err != nil {
			return false, err
		}
		unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 4 && unescape.Replace(fields[4]) == dir {
				return true, nil
			}
		}
		return false, nil
	}
	// Darwin: an external volume has a different device from its parent.
	var info, parent syscall.Stat_t
	if err := syscall.Stat(dir, &info); err != nil {
		return false, err
	}
	if err := syscall.Stat(filepath.Dir(dir), &parent); err != nil {
		return false, err
	}
	return info.Dev != parent.Dev, nil
}

func requireMount(dest, mount string) error {
	if !filepath.IsAbs(mount) {
		return fmt.Errorf("required mount must be an absolute path")
	}
	root, err := filepath.EvalSymlinks(mount)
	if err != nil {
		return fmt.Errorf("required backup mount unavailable: %w", err)
	}
	ok, err := mountedAt(root)
	if err != nil || !ok {
		return fmt.Errorf("required backup mount is not mounted: %s", mount)
	}
	if !isWithin(root, dest) {
		return fmt.Errorf("backup folder must be inside the required mount %s", mount)
	}
	var a, b syscall.Stat_t
	if err := syscall.Stat(root, &a); err != nil {
		return err
	}
	if err := syscall.Stat(dest, &b); err != nil {
		return err
	}
	if a.Dev != b.Dev {
		return fmt.Errorf("backup folder is not on the required storage device")
	}
	return nil
}

// Never stop a process to obtain a snapshot. Refuse instead. Non-Linux hosts
// are used for offline cards/tests; on the console /proc must be readable.
var checkGameIdle = func() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return fmt.Errorf("cannot check running games: %w", err)
	}
	for _, p := range processes {
		comm, err := os.ReadFile(filepath.Join("/proc", p.Name(), "comm"))
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(comm)) {
		case "minarch.elf", "retroarch", "drastic", "pico8", "pico8_dyn", "fake-08":
			return fmt.Errorf("game running (%s): close it before backup/restore", strings.TrimSpace(string(comm)))
		}
	}
	return nil
}
