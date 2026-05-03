package admission

import (
	"fmt"
	"sort"

	"devup/internal/api"
	"devup/internal/memoryctrl"
)

const mib = int64(1024 * 1024)

// RunningJob is the subset of live job state used by the admission planner.
type RunningJob struct {
	JobID        string
	Profile      string
	CurrentBytes int64
	LowBytes     int64
	HighBytes    int64
}

// Request describes a new job that wants to start.
type Request struct {
	Profile            string
	DemandBytes        int64
	HostAvailableBytes int64
	HostTotalBytes     int64
}

// Adjustment lowers a running job's memory.high limit to make room.
type Adjustment struct {
	JobID        string
	NewHighBytes int64
}

// Decision is the result of admission planning.
type Decision struct {
	Admit         bool
	Reason        string
	Adjustments   []Adjustment
	ReservedBytes int64
	NeededBytes   int64
}

// Plan decides whether a new job should be admitted. When host memory is tight
// it proposes memory.high reductions for the cheapest running jobs first.
func Plan(req Request, running []RunningJob) Decision {
	reserve := memoryctrl.SafetyReserveBytes(req.HostTotalBytes)
	availableForNewWork := maxInt64(0, req.HostAvailableBytes-reserve)

	if req.DemandBytes <= 0 {
		return Decision{
			Admit:         true,
			ReservedBytes: reserve,
			Reason:        "no demand estimate",
		}
	}
	if availableForNewWork >= req.DemandBytes {
		return Decision{
			Admit:         true,
			ReservedBytes: reserve,
		}
	}

	need := req.DemandBytes - availableForNewWork
	candidates := reclaimCandidates(running)

	var adjustments []Adjustment
	for _, candidate := range candidates {
		if need <= 0 {
			break
		}
		take := minInt64(candidate.reclaimableBytes, need)
		if take <= 0 {
			continue
		}
		adjustments = append(adjustments, Adjustment{
			JobID:        candidate.JobID,
			NewHighBytes: candidate.HighBytes - take,
		})
		need -= take
	}

	if need > 0 {
		return Decision{
			Admit:         false,
			ReservedBytes: reserve,
			NeededBytes:   need,
			Reason:        fmt.Sprintf("insufficient memory headroom: need %d MiB more", bytesToMiB(need)),
		}
	}

	return Decision{
		Admit:         true,
		Adjustments:   adjustments,
		ReservedBytes: reserve,
	}
}

// SlotsFree estimates how many additional service-profile jobs this node can
// accept before it should prefer another peer.
func SlotsFree(hostAvailableBytes, hostTotalBytes int64, runningJobs int, maxJobs int) int {
	remainingByCount := maxJobs - runningJobs
	if remainingByCount <= 0 {
		return 0
	}

	demandBytes := memoryctrl.EstimatedDemandBytes(api.ProfileService, 0, hostTotalBytes)
	if demandBytes <= 0 {
		return remainingByCount
	}
	available := maxInt64(0, hostAvailableBytes-memoryctrl.SafetyReserveBytes(hostTotalBytes))
	remainingByMemory := int(available / demandBytes)
	if remainingByMemory < 0 {
		return 0
	}
	if remainingByMemory < remainingByCount {
		return remainingByMemory
	}
	return remainingByCount
}

type reclaimCandidate struct {
	RunningJob
	reclaimableBytes int64
	priority         int
}

func reclaimCandidates(running []RunningJob) []reclaimCandidate {
	candidates := make([]reclaimCandidate, 0, len(running))
	for _, job := range running {
		profile := api.NormalizeProfile(job.Profile)
		minHigh := job.LowBytes + memoryctrl.MinHighHeadroomBytes(profile)
		if minHigh < job.LowBytes {
			minHigh = job.LowBytes
		}
		reclaimable := maxInt64(0, job.HighBytes-minHigh)
		if reclaimable == 0 {
			continue
		}
		candidates = append(candidates, reclaimCandidate{
			RunningJob:       job,
			reclaimableBytes: reclaimable,
			priority:         memoryctrl.ReclaimPriority(profile),
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		if candidates[i].reclaimableBytes != candidates[j].reclaimableBytes {
			return candidates[i].reclaimableBytes > candidates[j].reclaimableBytes
		}
		return candidates[i].JobID < candidates[j].JobID
	})
	return candidates
}

func bytesToMiB(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	return bytes / mib
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
