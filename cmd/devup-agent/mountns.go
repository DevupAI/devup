package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"devup/internal/api"
	"devup/internal/logging"
	"devup/internal/overlay"
)

type mountNamespaceExecSpec struct {
	JobID   string      `json:"job_id"`
	Cmd     []string    `json:"cmd"`
	Cwd     string      `json:"cwd,omitempty"`
	Mounts  []api.Mount `json:"mounts,omitempty"`
	Overlay bool        `json:"overlay,omitempty"`
}

func buildMountNamespaceCommand(spec mountNamespaceExecSpec, env []string) (*exec.Cmd, string, error) {
	if len(spec.Cmd) == 0 {
		return nil, "", fmt.Errorf("cmd required")
	}

	specFile, err := os.CreateTemp("", "devup-mountns-*.json")
	if err != nil {
		return nil, "", fmt.Errorf("create mount namespace spec: %w", err)
	}
	specPath := specFile.Name()
	enc := json.NewEncoder(specFile)
	enc.SetIndent("", "  ")
	if err := enc.Encode(spec); err != nil {
		specFile.Close()
		_ = os.Remove(specPath)
		return nil, "", fmt.Errorf("write mount namespace spec: %w", err)
	}
	if err := specFile.Close(); err != nil {
		_ = os.Remove(specPath)
		return nil, "", fmt.Errorf("close mount namespace spec: %w", err)
	}

	exePath, err := os.Executable()
	if err != nil {
		_ = os.Remove(specPath)
		return nil, "", fmt.Errorf("resolve agent executable: %w", err)
	}

	cmd := exec.Command(exePath, "ns-exec", specPath)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, specPath, nil
}

func cleanupMountNamespaceArtifacts(specPath string, overlayDirs []string) {
	if specPath != "" {
		_ = os.Remove(specPath)
	}
	for _, dir := range overlayDirs {
		if dir == "" {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			logging.Error("overlay state cleanup failed", "dir", dir, "err", err)
		}
	}
}

func overlaySubID(jobID string, idx int, total int) string {
	if total <= 1 {
		return jobID
	}
	return fmt.Sprintf("%s-%d", jobID, idx)
}

func overlayStateDirs(jobID string, mounts []api.Mount, useOverlay bool) []string {
	if !useOverlay || len(mounts) == 0 {
		return nil
	}
	dirs := make([]string, 0, len(mounts))
	for i := range mounts {
		dirs = append(dirs, filepath.Join(overlay.DataDir, overlaySubID(jobID, i, len(mounts))))
	}
	return dirs
}

func resolveExecutionWorkdir(mounts []api.Mount, cwd string) string {
	cwd = filepath.Clean(cwd)
	if cwd == "." || cwd == "" {
		return cwd
	}
	for _, m := range mounts {
		guestPath := filepath.Clean(m.GuestPath)
		hostPath := filepath.Clean(m.HostPath)
		if cwd == guestPath {
			return hostPath
		}
		if strings.HasPrefix(cwd, guestPath+string(os.PathSeparator)) {
			rel, err := filepath.Rel(guestPath, cwd)
			if err != nil {
				return cwd
			}
			return filepath.Join(hostPath, rel)
		}
	}
	return cwd
}
