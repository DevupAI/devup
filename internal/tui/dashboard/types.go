package dashboard

import (
	"context"
	"devup/internal/api"
	"time"
)

// View enum
const (
	ViewJobsList   = 0
	ViewBenchmarks = 1
	ViewLogs       = 2
	ViewStartModal = 3
)

// Message types
type tickMsg struct {
	t time.Time
}

type vmStatusMsg struct {
	running bool
}

type psResultMsg struct {
	jobs []api.JobInfo
	err  error
}

type healthResultMsg struct {
	ok      bool
	latency time.Duration
	err     error
}

type benchResultMsg struct {
	summary *benchSummary
	err     error
}

type logChunkMsg struct {
	data []byte
}

type logResultMsg struct {
	content string
	err     error
}

type logStreamDoneMsg struct{}

type logStreamStartedMsg struct {
	cancel context.CancelFunc
}

type startResultMsg struct {
	jobID string
	err   error
}

type errorMsg struct {
	err error
}

// HealthState holds agent health info
type HealthState struct {
	OK      bool
	Latency time.Duration
	Err     error
}
