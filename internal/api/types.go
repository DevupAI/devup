package api

// Mount describes a host->guest bind mount for workspace access
type Mount struct {
	HostPath  string `json:"host_path"`            // VM-visible path (e.g. /mnt/host/...)
	GuestPath string `json:"guest_path"`           // Where to bind inside VM (e.g. /workspace)
	ReadOnly  bool   `json:"read_only,omitempty"` // If true, mount read-only
}

// RunRequest is the JSON body for POST /run
type RunRequest struct {
	RequestID string            `json:"request_id"`
	Cmd       []string          `json:"cmd"`
	Env       map[string]string `json:"env"`
	Cwd       string            `json:"cwd"`
	Limits    Limits            `json:"limits"`
	Mounts    []Mount           `json:"mounts,omitempty"`
}

// Limits holds optional resource limits (for future container runtime)
type Limits struct {
	CPU string `json:"cpu"`
	Mem string `json:"mem"`
}

// HealthResponse is the JSON response for GET /health
type HealthResponse struct {
	Status    string  `json:"status"`
	UptimeSec float64 `json:"uptime_sec"`
	Version   string  `json:"version"`
}

// StartRequest is the JSON body for POST /start
type StartRequest struct {
	RequestID string            `json:"request_id"`
	Cmd       []string          `json:"cmd"`
	Env       map[string]string `json:"env,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Limits    Limits            `json:"limits,omitempty"`
	Mounts    []Mount           `json:"mounts,omitempty"`
}

// StartResponse is the JSON response for POST /start
type StartResponse struct {
	JobID string `json:"job_id"`
}

// JobInfo describes a job for /ps and job state
type JobInfo struct {
	JobID      string   `json:"job_id"`
	Cmd        []string `json:"cmd"`
	Status     string   `json:"status"` // running|exited|stopped|failed
	ExitCode   int      `json:"exit_code,omitempty"`
	StartedAt  int64    `json:"started_at_unix"`
	FinishedAt int64    `json:"finished_at_unix,omitempty"`
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
