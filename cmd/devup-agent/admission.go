package main

import (
	"errors"
	"os"

	"devup/internal/admission"
	"devup/internal/api"
	"devup/internal/cgroup"
	"devup/internal/logging"
	"devup/internal/memoryctrl"
	"devup/internal/sysinfo"
)

func availableSlots(stats sysinfo.Stats, activeJobs int) int {
	return admission.SlotsFree(int64(stats.MemFreeMB)*mib, int64(stats.MemTotalMB)*mib, activeJobs, maxConcurrentJobs)
}

func activeJobCount() int {
	return len(activeJobIDs())
}

func normalizedProfile(profile string, fallback string) string {
	if profile == "" {
		profile = fallback
	}
	return api.NormalizeProfile(profile)
}

func estimatedDemandBytes(profile string, limits *api.ResourceLimits, hostTotalBytes int64) int64 {
	if limits != nil && limits.MemoryMB > 0 {
		return int64(limits.MemoryMB) * mib
	}
	return memoryctrl.EstimatedDemandBytes(profile, 0, hostTotalBytes)
}

var pendingAdmissions = make(map[string]int64)

func reserveStartAdmission(ticket string, profile string, limits *api.ResourceLimits, host sysinfo.Stats) error {
	if !cgroup.Available {
		return nil
	}

	admissionMu.Lock()
	defer admissionMu.Unlock()

	pendingBytes := int64(0)
	for _, reserved := range pendingAdmissions {
		pendingBytes += reserved
	}
	hostAvailableBytes := int64(host.MemFreeMB)*mib - pendingBytes
	if hostAvailableBytes < 0 {
		hostAvailableBytes = 0
	}

	demandBytes := estimatedDemandBytes(profile, limits, int64(host.MemTotalMB)*mib)
	decision := admission.Plan(admission.Request{
		Profile:            profile,
		DemandBytes:        demandBytes,
		HostAvailableBytes: hostAvailableBytes,
		HostTotalBytes:     int64(host.MemTotalMB) * mib,
	}, runningJobsForAdmission())
	if !decision.Admit {
		return errors.New(decision.Reason)
	}
	if len(decision.Adjustments) > 0 {
		applyAdmissionAdjustments(decision.Adjustments)
	}
	if ticket != "" && demandBytes > 0 {
		pendingAdmissions[ticket] = demandBytes
	}
	return nil
}

func releaseStartAdmission(ticket string) {
	if ticket == "" {
		return
	}
	admissionMu.Lock()
	delete(pendingAdmissions, ticket)
	admissionMu.Unlock()
}

func runningJobsForAdmission() []admission.RunningJob {
	jobsMu.RLock()
	defer jobsMu.RUnlock()

	running := make([]admission.RunningJob, 0, len(jobs))
	for id, j := range jobs {
		j.mu.RLock()
		if j.info.Status != "running" {
			j.mu.RUnlock()
			continue
		}
		var currentBytes, lowBytes, highBytes int64
		if j.info.Memory != nil {
			currentBytes = int64(j.info.Memory.CurrentMB) * mib
			lowBytes = int64(j.info.Memory.LowMB) * mib
			highBytes = int64(j.info.Memory.HighMB) * mib
		}
		running = append(running, admission.RunningJob{
			JobID:        id,
			Profile:      api.NormalizeProfile(j.info.Profile),
			CurrentBytes: currentBytes,
			LowBytes:     lowBytes,
			HighBytes:    highBytes,
		})
		j.mu.RUnlock()
	}
	return running
}

func applyAdmissionAdjustments(adjustments []admission.Adjustment) {
	for _, adjustment := range adjustments {
		if err := cgroup.SetMemoryHigh(adjustment.JobID, adjustment.NewHighBytes); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			logging.Error("admission reclaim failed", "job_id", adjustment.JobID, "err", err)
			continue
		}

		jobsMu.RLock()
		j := jobs[adjustment.JobID]
		jobsMu.RUnlock()
		if j == nil {
			continue
		}
		j.mu.Lock()
		if j.info.Memory != nil {
			j.info.Memory.HighMB = bytesToMB(adjustment.NewHighBytes)
			j.info.Memory.ReclaimableMB = maxInt(bytesToMB(adjustment.NewHighBytes)-j.info.Memory.LowMB, 0)
		}
		j.mu.Unlock()
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
