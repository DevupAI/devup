package dashboard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBenchSummaryBuildsWinners(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.json")
	data := `{
  "timestamp": "2026-05-02T21:00:00-0400",
  "host": {"platform": "darwin"},
  "ephemeral": {
    "python-http": {
      "devup": {"mean_ms": 70},
      "devup-shadow": {"mean_ms": 80},
      "docker": {"mean_ms": 300}
    }
  },
  "service": {
    "python-http": {
      "devup": {"ready_ms": 320, "idle_memory_mb": 6},
      "devup-shadow": {"ready_ms": 290, "idle_memory_mb": 7},
      "docker": {"ready_ms": 700, "idle_memory_mb": 18}
    }
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	summary, err := loadBenchSummary(path)
	if err != nil {
		t.Fatalf("loadBenchSummary: %v", err)
	}
	if summary == nil || len(summary.Workloads) != 1 {
		t.Fatalf("unexpected summary %#v", summary)
	}
	workload := summary.Workloads[0]
	if workload.BestEphemeral != "devup" {
		t.Fatalf("expected devup to win ephemeral, got %q", workload.BestEphemeral)
	}
	if workload.BestReady != "devup-shadow" {
		t.Fatalf("expected devup-shadow to win ready time, got %q", workload.BestReady)
	}
	if workload.LowestMemory != "devup" {
		t.Fatalf("expected devup to win memory, got %q", workload.LowestMemory)
	}
}
