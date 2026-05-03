//go:build linux

package mountutil

import (
	"fmt"
	"syscall"
)

func BindMount(source string, target string, readOnly bool) error {
	if err := syscall.Mount(source, target, "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind mount %s -> %s: %w", source, target, err)
	}
	if !readOnly {
		return nil
	}
	if err := syscall.Mount("", target, "", uintptr(syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY), ""); err != nil {
		_ = syscall.Unmount(target, 0)
		return fmt.Errorf("remount ro %s: %w", target, err)
	}
	return nil
}

func OverlayMount(target string, lowerDir string, upperDir string, workDir string) error {
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lowerDir, upperDir, workDir)
	if err := syscall.Mount("overlay", target, "overlay", 0, opts); err != nil {
		return fmt.Errorf("overlay mount at %s: %w", target, err)
	}
	return nil
}

func Unmount(path string, flags int) error {
	return syscall.Unmount(path, flags)
}
