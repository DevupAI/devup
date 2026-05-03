package overlay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"devup/internal/logging"
	"devup/internal/mountutil"
)

const DataDir = "/var/lib/devup/overlay"

// State tracks the kernel objects for a single OverlayFS mount.
// The caller stores this on the job struct for cleanup.
type State struct {
	JobID     string
	LowerDir  string // read-only source (e.g. /mnt/host/Users/.../project)
	MergedDir string // where the process sees the combined view (e.g. /workspace)
	Dir       string // DataDir/<jobID> -- contains upper/ and work/
}

// Init creates the base data directory at agent startup.
func Init() error {
	return os.MkdirAll(DataDir, 0755)
}

// Mount creates an OverlayFS mount that presents lowerdir as a writable
// filesystem at mergeddir. Writes land in an ephemeral upperdir that is
// discarded on Unmount. The user's host files are never modified.
//
// Layout:
//
//	/var/lib/devup/overlay/<jobID>/upper  -- captures all writes
//	/var/lib/devup/overlay/<jobID>/work   -- OverlayFS internal metadata
//	<mergeddir>                           -- the combined view given to the process
func Mount(jobID, lowerdir, mergeddir string) (*State, error) {
	base := filepath.Join(DataDir, jobID)
	upper := filepath.Join(base, "upper")
	work := filepath.Join(base, "work")

	for _, d := range []string{upper, work, mergeddir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	if err := mountutil.OverlayMount(mergeddir, lowerdir, upper, work); err != nil {
		os.RemoveAll(base)
		return nil, fmt.Errorf("mount overlay at %s: %w", mergeddir, err)
	}

	return &State{
		JobID:     jobID,
		LowerDir:  lowerdir,
		MergedDir: mergeddir,
		Dir:       base,
	}, nil
}

// Unmount tears down the overlay mount and removes the ephemeral dirs.
// Uses MNT_DETACH for robustness against busy file handles.
func Unmount(s *State) error {
	if s == nil {
		return nil
	}
	// 0x2 = MNT_DETACH: lazy unmount, safe even if fds are still open
	if err := mountutil.Unmount(s.MergedDir, 0x2); err != nil {
		logging.Error("overlay unmount failed", "merged", s.MergedDir, "err", err)
	}
	if err := os.RemoveAll(s.Dir); err != nil {
		logging.Error("overlay cleanup failed", "dir", s.Dir, "err", err)
		return err
	}
	return nil
}

// Reconcile prunes overlay state dirs whose jobID is not in activeJobIDs.
// Any leftover overlay mount is force-unmounted first.
func Reconcile(activeJobIDs map[string]bool) {
	entries, err := os.ReadDir(DataDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if activeJobIDs[e.Name()] {
			continue
		}
		staleDir := filepath.Join(DataDir, e.Name())
		// Try to unmount any lingering overlay -- the merged path may vary,
		// so scan /proc/mounts for overlay mounts referencing this upperdir.
		pruneOverlayMounts(staleDir)
		if err := os.RemoveAll(staleDir); err != nil {
			logging.Error("overlay reconcile: cleanup failed", "dir", staleDir, "err", err)
		} else {
			logging.Info("overlay reconcile: pruned stale overlay", "job_id", e.Name())
		}
	}
}

func pruneOverlayMounts(overlayDir string) {
	// Find and unmount any overlay that references this directory
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return
	}
	upperOpt := "upperdir=" + filepath.Join(overlayDir, "upper")
	for _, line := range splitLines(data) {
		fields := splitFields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[2] != "overlay" {
			continue
		}
		if containsOpt(fields[3], upperOpt) {
			mountpoint := fields[1]
			_ = mountutil.Unmount(mountpoint, 0x2)
			os.Remove(mountpoint)
		}
	}
}

func splitLines(data []byte) []string {
	var lines []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, string(data[start:i]))
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, string(data[start:]))
	}
	return lines
}

func splitFields(s string) []string {
	return strings.Fields(s)
}

func containsOpt(optString, target string) bool {
	for _, opt := range splitByComma(optString) {
		if opt == target {
			return true
		}
	}
	return false
}

func splitByComma(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
