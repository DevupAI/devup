package shadow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeCopiesUpdatesAndPrunes(t *testing.T) {
	base := t.TempDir()
	oldDataDir := DataDir
	oldHostMountRoot := HostMountRoot
	DataDir = filepath.Join(base, "shadow")
	HostMountRoot = filepath.Join(base, "mnt", "host")
	t.Cleanup(func() {
		DataDir = oldDataDir
		HostMountRoot = oldHostMountRoot
	})

	source := filepath.Join(base, "mnt", "host", "demo")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "app.txt"), []byte("one"), 0o644); err != nil {
		t.Fatalf("WriteFile source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, ".gitignore"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("WriteFile gitignore: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, ".git", "HEAD"), []byte("ref"), 0o644); err != nil {
		t.Fatalf("WriteFile HEAD: %v", err)
	}

	dest, err := Materialize(filepath.Join(base, "mnt", "host", "demo"))
	if err != nil {
		t.Fatalf("Materialize initial: %v", err)
	}
	checkFile(t, filepath.Join(dest, "nested", "app.txt"), "one")
	if _, err := os.Stat(filepath.Join(dest, ".git", "HEAD")); !os.IsNotExist(err) {
		t.Fatalf("expected .git to be excluded, got err=%v", err)
	}

	if err := os.Remove(filepath.Join(source, "nested", "app.txt")); err != nil {
		t.Fatalf("Remove source file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "new.txt"), []byte("two"), 0o600); err != nil {
		t.Fatalf("WriteFile new: %v", err)
	}

	dest, err = Materialize(filepath.Join(base, "mnt", "host", "demo"))
	if err != nil {
		t.Fatalf("Materialize update: %v", err)
	}
	checkFile(t, filepath.Join(dest, "nested", "new.txt"), "two")
	if _, err := os.Stat(filepath.Join(dest, "nested", "app.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected deleted source file to be pruned, got err=%v", err)
	}
}

func TestIsManagedPath(t *testing.T) {
	oldDataDir := DataDir
	oldHostMountRoot := HostMountRoot
	DataDir = "/tmp/devup-shadow-test"
	HostMountRoot = "/mnt/host"
	t.Cleanup(func() {
		DataDir = oldDataDir
		HostMountRoot = oldHostMountRoot
	})
	if !IsManagedPath("/tmp/devup-shadow-test/demo/root") {
		t.Fatal("expected path under shadow root to be managed")
	}
	if IsManagedPath("/mnt/host/demo") {
		t.Fatal("expected host path to not be managed")
	}
}

func checkFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if got := string(data); got != want {
		t.Fatalf("unexpected file contents %q, want %q", got, want)
	}
}
