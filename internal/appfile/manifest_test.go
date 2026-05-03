package appfile

import (
	"os"
	"path/filepath"
	"testing"

	"devup/internal/api"
)

func TestResolveSupportsScalarCommandsAndProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(project, "devup.app.yaml")
	data := []byte(`
name: sample
services:
  web:
    command: echo hello
    profile: interactive
    workdir: frontend
    mounts:
      - .:/workspace
    memory_mb: 256
`)
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(manifestPath)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	web := got.Services["web"]
	if web.Profile != api.ProfileInteractive {
		t.Fatalf("expected interactive profile, got %q", web.Profile)
	}
	if len(web.Cmd) != 3 || web.Cmd[0] != "sh" || web.Cmd[1] != "-lc" || web.Cmd[2] != "echo hello" {
		t.Fatalf("unexpected command: %#v", web.Cmd)
	}
	if web.Workdir != "/workspace/frontend" {
		t.Fatalf("unexpected workdir %q", web.Workdir)
	}
	if web.Limits == nil || web.Limits.MemoryMB != 256 {
		t.Fatalf("expected memory limit 256MB, got %#v", web.Limits)
	}
	if len(web.Mounts) != 1 || web.Mounts[0].GuestPath != "/workspace" {
		t.Fatalf("unexpected mounts %#v", web.Mounts)
	}
}

func TestResolveShadowImpliesOverlay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(project, "devup.app.yaml")
	data := []byte(`
services:
  web:
    command: ["node", "server.js"]
    shadow: true
    mounts:
      - .:/workspace
`)
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(manifestPath)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	web := got.Services["web"]
	if !web.Shadow {
		t.Fatal("expected shadow mode to be enabled")
	}
	if !web.Overlay {
		t.Fatal("expected shadow mode to imply overlay")
	}
}

func TestStartOrderIncludesDependencies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(project, "devup.app.yaml")
	data := []byte(`
services:
  api:
    command: ["sleep", "60"]
  web:
    command: ["sleep", "60"]
    depends_on: ["api"]
`)
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(manifestPath)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	order, err := got.StartOrder([]string{"web"})
	if err != nil {
		t.Fatalf("StartOrder returned error: %v", err)
	}
	if len(order) != 2 {
		t.Fatalf("expected 2 services in start order, got %d", len(order))
	}
	if order[0].Name != "api" || order[1].Name != "web" {
		t.Fatalf("unexpected start order: %s -> %s", order[0].Name, order[1].Name)
	}
}

func TestResolveRejectsCycles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(project, "devup.app.yaml")
	data := []byte(`
services:
  api:
    command: ["sleep", "60"]
    depends_on: ["web"]
  web:
    command: ["sleep", "60"]
    depends_on: ["api"]
`)
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(manifestPath); err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}
