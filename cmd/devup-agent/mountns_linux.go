//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func privateMountNamespacesAvailable() bool {
	return true
}

func maybeRunMountNamespaceExec(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "ns-exec" {
		return false, nil
	}
	if len(args) != 2 {
		return true, fmt.Errorf("usage: devup-agent ns-exec <spec-file>")
	}
	return true, runMountNamespaceExec(args[1])
}

func runMountNamespaceExec(specPath string) error {
	data, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("read mount namespace spec: %w", err)
	}

	var spec mountNamespaceExecSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("decode mount namespace spec: %w", err)
	}
	if len(spec.Cmd) == 0 {
		return fmt.Errorf("mount namespace spec missing cmd")
	}

	if err := syscall.Unshare(syscall.CLONE_NEWNS); err != nil {
		return fmt.Errorf("unshare mount namespace: %w", err)
	}
	if err := syscall.Mount("", "/", "", uintptr(syscall.MS_REC|syscall.MS_PRIVATE), ""); err != nil {
		return fmt.Errorf("set mount propagation private: %w", err)
	}

	if spec.Overlay {
		states, _, err := applyOverlayMounts(spec.JobID, spec.Mounts)
		if err != nil {
			return err
		}
		defer cleanupOverlays(states)
	} else {
		mountedPaths, err := applyMounts(spec.Mounts)
		if err != nil {
			return err
		}
		defer cleanupMounts(mountedPaths)
	}

	if spec.Cwd != "" {
		if err := os.Chdir(spec.Cwd); err != nil {
			return fmt.Errorf("chdir %s: %w", spec.Cwd, err)
		}
	}

	path, err := exec.LookPath(spec.Cmd[0])
	if err != nil {
		return fmt.Errorf("lookpath %s: %w", spec.Cmd[0], err)
	}
	return syscall.Exec(path, spec.Cmd, os.Environ())
}
