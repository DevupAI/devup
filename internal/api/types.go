package api

import "strings"

const (
	ProfileBatch       = "batch"
	ProfileService     = "service"
	ProfileInteractive = "interactive"
)

// NormalizeProfile coerces arbitrary user input into a supported profile.
// Unknown values fall back to service-style behavior.
func NormalizeProfile(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "", ProfileService:
		return ProfileService
	case ProfileBatch:
		return ProfileBatch
	case ProfileInteractive:
		return ProfileInteractive
	default:
		return ProfileService
	}
}

// Mount describes a host->guest bind mount for workspace access
type Mount struct {
	HostPath  string `json:"host_path"`           // VM-visible path (e.g. /mnt/host/...)
	GuestPath string `json:"guest_path"`          // Where to bind inside VM (e.g. /workspace)
	ReadOnly  bool   `json:"read_only,omitempty"` // If true, mount read-only
}

// ResourceLimits specifies cgroup v2 resource constraints for a job.
type ResourceLimits struct {
	MemoryMB   int `json:"memory_mb,omitempty"`   // 0 = unlimited hard ceiling; agent may tune softer memory knobs below this
	CPUPercent int `json:"cpu_percent,omitempty"` // 1-100 of one core; maps to cpu.max
	PidsMax    int `json:"pids_max,omitempty"`    // 0 = unlimited
}

// MemoryEventStatus reports selected memory.events counters for a cgroup.
type MemoryEventStatus struct {
	High    uint64 `json:"high,omitempty"`
	OOM     uint64 `json:"oom,omitempty"`
	OOMKill uint64 `json:"oom_kill,omitempty"`
}

// MemoryStatus reports the live cgroup memory state for a running job.
type MemoryStatus struct {
	Adaptive      bool               `json:"adaptive,omitempty"`       // true when the agent is tuning memory.high/memory.low
	CurrentMB     int                `json:"current_mb,omitempty"`     // current memory.current
	PeakMB        int                `json:"peak_mb,omitempty"`        // memory.peak
	LowMB         int                `json:"low_mb,omitempty"`         // memory.low protection floor
	HighMB        int                `json:"high_mb,omitempty"`        // memory.high soft limit
	MaxMB         int                `json:"max_mb,omitempty"`         // memory.max hard limit (0 means unlimited)
	ReclaimableMB int                `json:"reclaimable_mb,omitempty"` // immediately reclaimable headroom under current policy
	Events        *MemoryEventStatus `json:"events,omitempty"`
}

// RunRequest is the JSON body for POST /run
type RunRequest struct {
	RequestID  string            `json:"request_id"`
	Cmd        []string          `json:"cmd"`
	Env        map[string]string `json:"env"`
	Cwd        string            `json:"cwd"`
	Profile    string            `json:"profile,omitempty"`
	Mounts     []Mount           `json:"mounts,omitempty"`
	Limits     *ResourceLimits   `json:"limits,omitempty"`
	Overlay    bool              `json:"overlay,omitempty"`
	Shadow     bool              `json:"shadow,omitempty"`
	NetIsolate bool              `json:"net_isolate,omitempty"`
}

// HealthResponse is the JSON response for GET /health
type HealthResponse struct {
	Status      string `json:"status"`
	Version     string `json:"version"`
	DefaultHome string `json:"default_home,omitempty"`
}

// StartRequest is the JSON body for POST /start
type StartRequest struct {
	RequestID  string            `json:"request_id"`
	Cmd        []string          `json:"cmd"`
	Env        map[string]string `json:"env,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	Profile    string            `json:"profile,omitempty"`
	Mounts     []Mount           `json:"mounts,omitempty"`
	Limits     *ResourceLimits   `json:"limits,omitempty"`
	Overlay    bool              `json:"overlay,omitempty"`
	Shadow     bool              `json:"shadow,omitempty"`
	NetIsolate bool              `json:"net_isolate,omitempty"`
}

// StartResponse is the JSON response for POST /start
type StartResponse struct {
	JobID string `json:"job_id"`
}

// JobInfo describes a job for /ps and job state
type JobInfo struct {
	JobID      string          `json:"job_id"`
	Cmd        []string        `json:"cmd"`
	Profile    string          `json:"profile,omitempty"`
	Status     string          `json:"status"` // running|exited|stopped|failed
	ExitCode   int             `json:"exit_code,omitempty"`
	StartedAt  int64           `json:"started_at_unix"`
	FinishedAt int64           `json:"finished_at_unix,omitempty"`
	Limits     *ResourceLimits `json:"limits,omitempty"`
	Memory     *MemoryStatus   `json:"memory,omitempty"`
}

// PsResponse is the JSON response for GET /ps
type PsResponse struct {
	Jobs []JobInfo `json:"jobs"`
}

// StopResponse is the JSON response for POST /stop
type StopResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// ToolInfo describes a single tool's version check result
type ToolInfo struct {
	Status  string `json:"status"`  // "ok" or "missing"
	Version string `json:"version"` // version string or "-"
}

// SystemInfoResponse is the JSON response for GET /system/info
type SystemInfoResponse struct {
	Tools map[string]ToolInfo `json:"tools"`
}

// PeerInfo describes a discovered node in the cluster.
type PeerInfo struct {
	NodeID     string  `json:"node_id"`
	Addr       string  `json:"addr"`
	Port       int     `json:"port"`
	SlotsFree  int     `json:"slots_free"`
	Version    string  `json:"version"`
	Status     string  `json:"status"`    // "online" or "local"
	LastSeen   int64   `json:"last_seen"` // unix timestamp
	ActiveJobs int     `json:"active_jobs"`
	MemTotalMB int     `json:"mem_total_mb,omitempty"`
	MemFreeMB  int     `json:"mem_free_mb,omitempty"`
	LoadAvg1   float64 `json:"load_avg_1,omitempty"`
}

// ClusterResponse is the JSON response for GET /cluster
type ClusterResponse struct {
	Peers []PeerInfo `json:"peers"`
}

// UploadResponse is the JSON response for POST /upload
type UploadResponse struct {
	WorkspacePath string `json:"workspace_path"`
}
