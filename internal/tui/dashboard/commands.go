package dashboard

import (
	"context"
	"os"
	"strings"
	"time"

	"devup/internal/api"
	"devup/internal/client"
	"devup/internal/mounts"
	"devup/internal/util"
	"devup/internal/vm"

	tea "github.com/charmbracelet/bubbletea"
)

// tickCmd sends tickMsg every interval
func tickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return tickMsg{t: t}
	})
}

// fetchVmStatusCmd runs vm.IsRunning() in a goroutine (async, doesn't block UI)
func fetchVmStatusCmd() tea.Cmd {
	return func() tea.Msg {
		return vmStatusMsg{running: vm.IsRunning()}
	}
}

// fetchPsCmd fetches job list
func fetchPsCmd(c *client.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		jobs, err := c.Ps(ctx)
		return psResultMsg{jobs: jobs, err: err}
	}
}

// fetchHealthCmd fetches agent health and measures latency
func fetchHealthCmd(c *client.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		start := time.Now()
		_, err := c.Health(ctx)
		latency := time.Since(start)
		return healthResultMsg{ok: err == nil, latency: latency, err: err}
	}
}

func fetchBenchCmd(path string) tea.Cmd {
	return func() tea.Msg {
		summary, err := loadBenchSummary(path)
		return benchResultMsg{summary: summary, err: err}
	}
}

// fetchLogsCmd fetches logs without follow
func fetchLogsCmd(c *client.Client, jobID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var buf strings.Builder
		err := c.Logs(ctx, jobID, false, &buf)
		return logResultMsg{content: buf.String(), err: err}
	}
}

// streamLogsCmd starts a goroutine that streams logs and sends chunks via send.
// Returns logStreamStartedMsg with cancel func so caller can stop streaming.
func streamLogsCmd(c *client.Client, jobID string, send func(tea.Msg)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			defer func() { send(logStreamDoneMsg{}) }()
			chunkWriter := &logChunkWriter{send: send}
			_ = c.Logs(ctx, jobID, true, chunkWriter)
		}()
		return logStreamStartedMsg{cancel: cancel}
	}
}

// logChunkWriter implements io.Writer; copies bytes before Send to avoid mutation
type logChunkWriter struct {
	send func(tea.Msg)
}

func (w *logChunkWriter) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	// Copy to avoid slice mutation (tweak #2)
	cp := append([]byte(nil), p...)
	w.send(logChunkMsg{data: cp})
	return len(p), nil
}

// stopJobCmd stops a job
func stopJobCmd(c *client.Client, jobID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := c.Stop(ctx, jobID)
		if err != nil {
			return errorMsg{err: err}
		}
		return nil
	}
}

// downCmd stops all jobs
func downCmd(c *client.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := c.Down(ctx)
		if err != nil {
			return errorMsg{err: err}
		}
		return psResultMsg{} // trigger refresh
	}
}

// startJobCmd parses command and mount, calls client.Start
func startJobCmd(c *client.Client, cmdStr, mountStr, profile string, shadow bool) tea.Cmd {
	return func() tea.Msg {
		cmdStr = strings.TrimSpace(cmdStr)
		mountStr = strings.TrimSpace(mountStr)
		if cmdStr == "" {
			return startResultMsg{err: errCommandRequired}
		}
		if mountStr == "" {
			mountStr = ".:/workspace"
		}
		cwd, _ := os.Getwd()
		home, err := os.UserHomeDir()
		if err != nil {
			return startResultMsg{err: err}
		}
		m, err := mounts.ParseMountFromString(mountStr, cwd, home)
		if err != nil {
			return startResultMsg{err: err}
		}
		cmd := parseCommand(cmdStr)
		if len(cmd) == 0 {
			return startResultMsg{err: errCommandRequired}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req := &api.StartRequest{
			RequestID: util.GenerateRequestID(),
			Cmd:       cmd,
			Env:       util.EnvMap(),
			Cwd:       "",
			Profile:   api.NormalizeProfile(profile),
			Mounts:    []api.Mount{m},
			Overlay:   shadow,
			Shadow:    shadow,
		}
		jobID, err := c.Start(ctx, req)
		return startResultMsg{jobID: jobID, err: err}
	}
}

var errCommandRequired = &parseError{msg: "command required"}

type parseError struct{ msg string }

func (e *parseError) Error() string { return e.msg }

// parseCommand splits cmdStr into []string, respecting quoted strings
func parseCommand(cmdStr string) []string {
	var result []string
	var current strings.Builder
	inDouble := false
	inSingle := false
	for i := 0; i < len(cmdStr); i++ {
		c := cmdStr[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				current.WriteByte(c)
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			} else {
				current.WriteByte(c)
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == ' ' || c == '\t':
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(c)
		}
	}
	if current.Len() > 0 {
		result = append(result, current.String())
	}
	return result
}
