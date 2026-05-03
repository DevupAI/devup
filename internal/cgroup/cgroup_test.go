package cgroup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateWritesMemoryKnobs(t *testing.T) {
	root := t.TempDir()
	prevRoot := cgroupRoot
	prevAvailable := Available
	cgroupRoot = root
	Available = true
	t.Cleanup(func() {
		cgroupRoot = prevRoot
		Available = prevAvailable
	})

	if err := Create("job-1", Limits{
		MemoryMaxBytes:  1024,
		MemoryHighBytes: 768,
		MemoryLowBytes:  256,
		CPUQuotaUs:      5000,
		CPUPeriodUs:     100000,
		PidsMax:         32,
	}); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	assertFileValue(t, filepath.Join(root, "job-1", "memory.max"), "1024")
	assertFileValue(t, filepath.Join(root, "job-1", "memory.high"), "768")
	assertFileValue(t, filepath.Join(root, "job-1", "memory.low"), "256")
	assertFileValue(t, filepath.Join(root, "job-1", "cpu.max"), "5000 100000")
	assertFileValue(t, filepath.Join(root, "job-1", "pids.max"), "32")
}

func TestSetMemoryKnobsAndReadCurrent(t *testing.T) {
	root := t.TempDir()
	prevRoot := cgroupRoot
	prevAvailable := Available
	cgroupRoot = root
	Available = true
	t.Cleanup(func() {
		cgroupRoot = prevRoot
		Available = prevAvailable
	})

	jobDir := filepath.Join(root, "job-2")
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "memory.current"), []byte("2048\n"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "memory.peak"), []byte("4096\n"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "memory.events"), []byte("high 7\noom 2\noom_kill 1\n"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	if err := SetMemoryLimit("job-2", 4096); err != nil {
		t.Fatalf("SetMemoryLimit returned error: %v", err)
	}
	if err := SetMemoryHigh("job-2", 3072); err != nil {
		t.Fatalf("SetMemoryHigh returned error: %v", err)
	}
	if err := SetMemoryLow("job-2", 1024); err != nil {
		t.Fatalf("SetMemoryLow returned error: %v", err)
	}

	current, err := ReadMemoryCurrent("job-2")
	if err != nil {
		t.Fatalf("ReadMemoryCurrent returned error: %v", err)
	}
	if current != 2048 {
		t.Fatalf("expected current=2048, got %d", current)
	}
	peak, err := ReadMemoryPeak("job-2")
	if err != nil {
		t.Fatalf("ReadMemoryPeak returned error: %v", err)
	}
	if peak != 4096 {
		t.Fatalf("expected peak=4096, got %d", peak)
	}
	events, err := ReadMemoryEvents("job-2")
	if err != nil {
		t.Fatalf("ReadMemoryEvents returned error: %v", err)
	}
	if events.High != 7 || events.OOM != 2 || events.OOMKill != 1 {
		t.Fatalf("unexpected events %+v", events)
	}

	assertFileValue(t, filepath.Join(jobDir, "memory.max"), "4096")
	assertFileValue(t, filepath.Join(jobDir, "memory.high"), "3072")
	assertFileValue(t, filepath.Join(jobDir, "memory.low"), "1024")
}

func assertFileValue(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", path, err)
	}
	if got := string(data); got != want {
		t.Fatalf("expected %s to contain %q, got %q", path, want, got)
	}
}
