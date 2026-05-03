package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"devup/internal/logging"
)

const CgroupRoot = "/sys/fs/cgroup/devup"

var cgroupRoot = CgroupRoot

// Limits describes resource constraints for a single job.
type Limits struct {
	MemoryMaxBytes  int64 // memory.max; 0 = unlimited
	MemoryHighBytes int64 // memory.high; 0 = unlimited
	MemoryLowBytes  int64 // memory.low; 0 = no protection
	CPUQuotaUs      int   // cpu.max quota in microseconds; 0 = unlimited
	CPUPeriodUs     int   // cpu.max period in microseconds; default 100000
	PidsMax         int   // pids.max; 0 = unlimited
}

// MemoryEvents reports selected cgroup memory.events counters.
type MemoryEvents struct {
	High    uint64
	OOM     uint64
	OOMKill uint64
}

// Available reports whether cgroups v2 unified hierarchy is usable.
var Available bool

// Init creates the devup cgroup subtree and enables controllers.
// Called once at agent startup. If cgroups v2 is unavailable, sets
// Available=false and returns the error (non-fatal to the caller).
func Init() error {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		Available = false
		return fmt.Errorf("cgroups v2 not available: %w", err)
	}

	if err := os.MkdirAll(cgroupRoot, 0755); err != nil {
		Available = false
		return fmt.Errorf("mkdir %s: %w", cgroupRoot, err)
	}

	// Enable memory, cpu, pids controllers on the parent so children inherit them
	controllers := "+memory +cpu +pids"
	if err := os.WriteFile("/sys/fs/cgroup/cgroup.subtree_control", []byte(controllers), 0644); err != nil {
		logging.Error("enable root controllers (may already be set)", "err", err)
	}
	if err := os.WriteFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"), []byte(controllers), 0644); err != nil {
		logging.Error("enable devup controllers", "err", err)
	}

	Available = true
	return nil
}

// Create makes a cgroup directory for the job and writes limit files.
func Create(jobID string, l Limits) error {
	if !Available {
		return fmt.Errorf("cgroups not available")
	}
	dir := filepath.Join(cgroupRoot, jobID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir cgroup %s: %w", dir, err)
	}

	if err := writeMemoryValue(filepath.Join(dir, "memory.max"), l.MemoryMaxBytes, true); err != nil {
		return fmt.Errorf("write memory.max: %w", err)
	}
	if err := writeMemoryValue(filepath.Join(dir, "memory.high"), l.MemoryHighBytes, true); err != nil {
		return fmt.Errorf("write memory.high: %w", err)
	}
	if err := writeMemoryValue(filepath.Join(dir, "memory.low"), l.MemoryLowBytes, false); err != nil {
		return fmt.Errorf("write memory.low: %w", err)
	}

	if l.CPUQuotaUs > 0 {
		period := l.CPUPeriodUs
		if period <= 0 {
			period = 100000
		}
		val := fmt.Sprintf("%d %d", l.CPUQuotaUs, period)
		if err := os.WriteFile(filepath.Join(dir, "cpu.max"), []byte(val), 0644); err != nil {
			return fmt.Errorf("write cpu.max: %w", err)
		}
	}

	if l.PidsMax > 0 {
		if err := os.WriteFile(filepath.Join(dir, "pids.max"), []byte(strconv.Itoa(l.PidsMax)), 0644); err != nil {
			return fmt.Errorf("write pids.max: %w", err)
		}
	}

	return nil
}

// AddProcess writes a PID into the cgroup's cgroup.procs file.
func AddProcess(jobID string, pid int) error {
	if !Available {
		return fmt.Errorf("cgroups not available")
	}
	path := filepath.Join(cgroupRoot, jobID, "cgroup.procs")
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0644)
}

// SetMemoryLimit updates a job's hard memory limit in memory.max.
func SetMemoryLimit(jobID string, bytes int64) error {
	if !Available {
		return fmt.Errorf("cgroups not available")
	}
	return writeMemoryValue(filepath.Join(cgroupRoot, jobID, "memory.max"), bytes, true)
}

// SetMemoryHigh updates a job's soft pressure limit in memory.high.
func SetMemoryHigh(jobID string, bytes int64) error {
	if !Available {
		return fmt.Errorf("cgroups not available")
	}
	return writeMemoryValue(filepath.Join(cgroupRoot, jobID, "memory.high"), bytes, true)
}

// SetMemoryLow updates a job's protected floor in memory.low.
func SetMemoryLow(jobID string, bytes int64) error {
	if !Available {
		return fmt.Errorf("cgroups not available")
	}
	return writeMemoryValue(filepath.Join(cgroupRoot, jobID, "memory.low"), bytes, false)
}

// ReadMemoryCurrent returns a job's current cgroup memory usage in bytes.
func ReadMemoryCurrent(jobID string) (int64, error) {
	if !Available {
		return 0, fmt.Errorf("cgroups not available")
	}
	return readIntValue(filepath.Join(cgroupRoot, jobID, "memory.current"))
}

// ReadMemoryPeak returns the cgroup's memory.peak value in bytes.
func ReadMemoryPeak(jobID string) (int64, error) {
	if !Available {
		return 0, fmt.Errorf("cgroups not available")
	}
	return readIntValue(filepath.Join(cgroupRoot, jobID, "memory.peak"))
}

// ReadMemoryEvents returns selected memory.events counters.
func ReadMemoryEvents(jobID string) (MemoryEvents, error) {
	if !Available {
		return MemoryEvents{}, fmt.Errorf("cgroups not available")
	}
	data, err := os.ReadFile(filepath.Join(cgroupRoot, jobID, "memory.events"))
	if err != nil {
		return MemoryEvents{}, err
	}

	var events MemoryEvents
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "high":
			events.High = value
		case "oom":
			events.OOM = value
		case "oom_kill":
			events.OOMKill = value
		}
	}
	return events, nil
}

// Destroy removes a job's cgroup directory. The kernel requires all
// processes to have exited first. If processes remain, they are killed.
func Destroy(jobID string) error {
	if !Available {
		return nil
	}
	dir := filepath.Join(cgroupRoot, jobID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}

	// First attempt
	if err := os.Remove(dir); err == nil {
		return nil
	}

	// Kill remaining processes and retry
	killProcsInCgroup(dir)
	return os.Remove(dir)
}

// Reconcile prunes stale cgroup directories that no longer correspond
// to running jobs. Called at startup and periodically.
func Reconcile(activeJobIDs map[string]bool) {
	if !Available {
		return
	}
	entries, err := os.ReadDir(cgroupRoot)
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
		if err := Destroy(e.Name()); err != nil {
			logging.Error("cgroup reconcile: destroy failed", "job_id", e.Name(), "err", err)
		} else {
			logging.Info("cgroup reconcile: pruned stale cgroup", "job_id", e.Name())
		}
	}
}

func killProcsInCgroup(dir string) {
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func writeMemoryValue(path string, bytes int64, unlimited bool) error {
	value := "0"
	switch {
	case bytes > 0:
		value = strconv.FormatInt(bytes, 10)
	case unlimited:
		value = "max"
	}
	return os.WriteFile(path, []byte(value), 0644)
}

func readIntValue(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
}
