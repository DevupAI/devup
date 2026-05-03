package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"devup/internal/api"
	"devup/internal/cgroup"
	"devup/internal/logging"
	"devup/internal/memoryctrl"
	"devup/internal/sysinfo"
)

const adaptiveMemoryTick = 5 * time.Second

func applyJobCgroup(jobID string, profile string, limits *api.ResourceLimits, pid int, host sysinfo.Stats) (*api.MemoryStatus, error) {
	if !cgroup.Available {
		return nil, nil
	}

	hardMaxBytes := int64(0)
	cpuPercent := 0
	pidsMax := 0
	if limits != nil {
		hardMaxBytes = int64(limits.MemoryMB) * mib
		cpuPercent = limits.CPUPercent
		pidsMax = limits.PidsMax
	}

	controller := memoryctrl.New()
	initial := controller.Observe(memoryctrl.Sample{
		Profile:            profile,
		MaxBytes:           hardMaxBytes,
		HostAvailableBytes: int64(host.MemFreeMB) * mib,
		HostTotalBytes:     int64(host.MemTotalMB) * mib,
	})

	cgLimits := cgroup.Limits{
		MemoryMaxBytes:  hardMaxBytes,
		MemoryHighBytes: initial.HighBytes,
		MemoryLowBytes:  initial.LowBytes,
		CPUQuotaUs:      cpuPercent * 1000,
		CPUPeriodUs:     100000,
		PidsMax:         pidsMax,
	}
	if err := cgroup.Create(jobID, cgLimits); err != nil {
		return nil, fmt.Errorf("cgroup create: %w", err)
	}
	if err := cgroup.AddProcess(jobID, pid); err != nil {
		cgroup.Destroy(jobID)
		return nil, fmt.Errorf("cgroup add: %w", err)
	}

	return &api.MemoryStatus{
		Adaptive:      true,
		LowMB:         bytesToMB(initial.LowBytes),
		HighMB:        bytesToMB(initial.HighBytes),
		MaxMB:         bytesToMB(hardMaxBytes),
		ReclaimableMB: bytesToMB(initial.ReclaimableBytes),
	}, nil
}

func startAdaptiveMemoryController(j *job, jobID string, profile string, hardMaxBytes int64) {
	if j == nil || !cgroup.Available {
		return
	}

	done := make(chan struct{})
	j.memoryControllerDone = done

	go func() {
		defer close(done)

		ctrl := memoryctrl.New()
		ticker := time.NewTicker(adaptiveMemoryTick)
		defer ticker.Stop()

		for {
			select {
			case <-j.done:
				return
			case <-ticker.C:
			}

			current, err := cgroup.ReadMemoryCurrent(jobID)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) || waitForDone(j.done, 0) {
					return
				}
				logging.Error("memory controller read current failed", "job_id", jobID, "err", err)
				continue
			}

			stats := sysinfo.Read()
			limits := ctrl.Observe(memoryctrl.Sample{
				Profile:            profile,
				CurrentBytes:       current,
				MaxBytes:           hardMaxBytes,
				HostAvailableBytes: int64(stats.MemFreeMB) * mib,
				HostTotalBytes:     int64(stats.MemTotalMB) * mib,
			})

			if err := cgroup.SetMemoryLow(jobID, limits.LowBytes); err != nil {
				if errors.Is(err, os.ErrNotExist) || waitForDone(j.done, 0) {
					return
				}
				logging.Error("memory controller set low failed", "job_id", jobID, "err", err)
				continue
			}
			if err := cgroup.SetMemoryHigh(jobID, limits.HighBytes); err != nil {
				if errors.Is(err, os.ErrNotExist) || waitForDone(j.done, 0) {
					return
				}
				logging.Error("memory controller set high failed", "job_id", jobID, "err", err)
				continue
			}

			peak, peakErr := cgroup.ReadMemoryPeak(jobID)
			if peakErr != nil && !errors.Is(peakErr, os.ErrNotExist) {
				logging.Error("memory controller read peak failed", "job_id", jobID, "err", peakErr)
			}
			events, eventsErr := cgroup.ReadMemoryEvents(jobID)
			if eventsErr != nil && !errors.Is(eventsErr, os.ErrNotExist) {
				logging.Error("memory controller read events failed", "job_id", jobID, "err", eventsErr)
			}

			j.mu.Lock()
			if j.info.Memory == nil {
				j.info.Memory = &api.MemoryStatus{}
			}
			j.info.Memory.Adaptive = true
			j.info.Memory.CurrentMB = bytesToMB(current)
			j.info.Memory.PeakMB = bytesToMB(peak)
			j.info.Memory.LowMB = bytesToMB(limits.LowBytes)
			j.info.Memory.HighMB = bytesToMB(limits.HighBytes)
			j.info.Memory.MaxMB = bytesToMB(hardMaxBytes)
			j.info.Memory.ReclaimableMB = bytesToMB(limits.ReclaimableBytes)
			j.info.Memory.Events = memoryEventStatus(events)
			j.mu.Unlock()
		}
	}()
}

func memoryEventStatus(events cgroup.MemoryEvents) *api.MemoryEventStatus {
	if events.High == 0 && events.OOM == 0 && events.OOMKill == 0 {
		return nil
	}
	return &api.MemoryEventStatus{
		High:    events.High,
		OOM:     events.OOM,
		OOMKill: events.OOMKill,
	}
}

func bytesToMB(bytes int64) int {
	if bytes <= 0 {
		return 0
	}
	return int(bytes / mib)
}
