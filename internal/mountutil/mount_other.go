//go:build !linux

package mountutil

import (
	"fmt"
	"os/exec"
)

func BindMount(source string, target string, readOnly bool) error {
	if out, err := exec.Command("mount", "--bind", source, target).CombinedOutput(); err != nil {
		return fmt.Errorf("bind mount %s -> %s: %w\n%s", source, target, err, out)
	}
	if !readOnly {
		return nil
	}
	if out, err := exec.Command("mount", "-o", "remount,ro,bind", target).CombinedOutput(); err != nil {
		_ = exec.Command("umount", target).Run()
		return fmt.Errorf("remount ro %s: %w\n%s", target, err, out)
	}
	return nil
}

func OverlayMount(target string, lowerDir string, upperDir string, workDir string) error {
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lowerDir, upperDir, workDir)
	if out, err := exec.Command("mount", "-t", "overlay", "overlay", "-o", opts, target).CombinedOutput(); err != nil {
		return fmt.Errorf("overlay mount at %s: %w\n%s", target, err, out)
	}
	return nil
}

func Unmount(path string, flags int) error {
	_ = flags
	if out, err := exec.Command("umount", path).CombinedOutput(); err != nil {
		return fmt.Errorf("umount %s: %w\n%s", path, err, out)
	}
	return nil
}
