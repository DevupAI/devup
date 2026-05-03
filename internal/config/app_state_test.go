package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadDeleteAppState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manifestPath := filepath.Join(home, "proj", "devup.app.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &AppState{
		Name:      "sample",
		UpdatedAt: 42,
		Services: map[string]AppServiceState{
			"web": {
				JobID:     "abc123",
				Profile:   "interactive",
				Cmd:       []string{"sleep", "60"},
				StartedAt: 10,
			},
		},
	}
	if err := WriteAppState(manifestPath, state); err != nil {
		t.Fatalf("WriteAppState returned error: %v", err)
	}
	readBack, err := ReadAppState(manifestPath)
	if err != nil {
		t.Fatalf("ReadAppState returned error: %v", err)
	}
	if readBack.ManifestPath == "" {
		t.Fatal("expected manifest path to be filled in")
	}
	if readBack.Services["web"].JobID != "abc123" {
		t.Fatalf("unexpected job id %q", readBack.Services["web"].JobID)
	}
	if err := DeleteAppState(manifestPath); err != nil {
		t.Fatalf("DeleteAppState returned error: %v", err)
	}
	if HasAppState(manifestPath) {
		t.Fatal("expected app state file to be gone after delete")
	}
}
