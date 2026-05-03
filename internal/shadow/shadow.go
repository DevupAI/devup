package shadow

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"devup/internal/workspace"
)

var DataDir = "/var/lib/devup/shadow"
var HostMountRoot = "/mnt/host"

var (
	lockMu sync.Mutex
	locks  = map[string]*sync.Mutex{}
)

// Init ensures the shadow workspace root exists.
func Init() error {
	return os.MkdirAll(DataDir, 0o755)
}

// IsManagedPath reports whether the given path belongs to the shadow store.
func IsManagedPath(path string) bool {
	clean := filepath.Clean(path)
	return clean == DataDir || strings.HasPrefix(clean, DataDir+string(os.PathSeparator))
}

// Materialize incrementally mirrors a host-shared workspace into a VM-local
// cache directory. The returned path is suitable for bind or overlay mounts.
func Materialize(source string) (string, error) {
	source = filepath.Clean(source)
	if !isHostMountPath(source) {
		return "", fmt.Errorf("shadow source %s must be under %s", source, HostMountRoot)
	}

	key := cacheKey(source)
	unlock := lockFor(key)
	defer unlock()

	root := filepath.Join(DataDir, key, "root")
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return "", err
	}
	if err := syncTree(source, root); err != nil {
		return "", err
	}
	return root, nil
}

func isHostMountPath(path string) bool {
	root := filepath.Clean(HostMountRoot)
	path = filepath.Clean(path)
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

func cacheKey(source string) string {
	return fmt.Sprintf("%x", sum64(source))
}

func sum64(source string) uint64 {
	var sum uint64 = 1469598103934665603
	for i := 0; i < len(source); i++ {
		sum ^= uint64(source[i])
		sum *= 1099511628211
	}
	return sum
}

func lockFor(key string) func() {
	lockMu.Lock()
	mu := locks[key]
	if mu == nil {
		mu = &sync.Mutex{}
		locks[key] = mu
	}
	lockMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func syncTree(source, dest string) error {
	seen := map[string]struct{}{".": {}}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.IsDir() && workspace.IsExcluded(entry.Name(), shadowExcludes) {
			return filepath.SkipDir
		}
		target := filepath.Join(dest, rel)
		seen[rel] = struct{}{}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		switch {
		case entry.Type()&os.ModeSymlink != 0:
			return syncSymlink(path, target)
		case info.IsDir():
			if err := ensureDir(target, info.Mode().Perm()); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := syncFile(path, target, info); err != nil {
				return err
			}
		default:
			return nil
		}

		return nil
	})
	if err != nil {
		return err
	}

	return pruneDeleted(dest, seen)
}

var shadowExcludes = []string{
	".git",
	".devup",
	"__pycache__",
}

func ensureDir(path string, mode fs.FileMode) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		} else if info.Mode().Perm() == mode {
			return nil
		}
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func syncSymlink(source, dest string) error {
	link, err := os.Readlink(source)
	if err != nil {
		return err
	}
	if existing, err := os.Readlink(dest); err == nil && existing == link {
		return nil
	}
	if err := os.RemoveAll(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.Symlink(link, dest)
}

func syncFile(source, dest string, info os.FileInfo) error {
	if sameFile(dest, info) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".devup-shadow-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	src, err := os.Open(source)
	if err != nil {
		tmp.Close()
		return err
	}
	_, copyErr := io.Copy(tmp, src)
	closeErr := tmp.Close()
	src.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Chmod(tmpPath, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chtimes(tmpPath, time.Now(), info.ModTime()); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmpPath, dest)
}

func sameFile(dest string, info os.FileInfo) bool {
	existing, err := os.Stat(dest)
	if err != nil {
		return false
	}
	if !existing.Mode().IsRegular() {
		return false
	}
	if existing.Size() != info.Size() {
		return false
	}
	if existing.Mode().Perm() != info.Mode().Perm() {
		return false
	}
	return existing.ModTime().UnixNano() == info.ModTime().UnixNano()
}

func pruneDeleted(dest string, seen map[string]struct{}) error {
	var stale []string
	err := filepath.WalkDir(dest, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dest, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if _, ok := seen[rel]; ok {
			return nil
		}
		stale = append(stale, path)
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(stale, func(i, j int) bool {
		return len(stale[i]) > len(stale[j])
	})
	for _, path := range stale {
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
