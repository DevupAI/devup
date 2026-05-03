package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"devup/internal/api"
	"devup/internal/ringbuffer"
)

func TestHandleRunRejectsOversizedJSONBody(t *testing.T) {
	handler := handleRun(make(chan struct{}, 1), make(map[string]*runResult), &sync.RWMutex{})
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(oversizedRunRequestBody()))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d: %s", http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	}
}

func TestHandleStartRejectsOversizedJSONBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/start", strings.NewReader(oversizedStartRequestBody()))
	rec := httptest.NewRecorder()

	handleStart(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d: %s", http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	}
}

func TestBuildEnvNormalizesMacOSPathsAndPreservesLinuxOverrides(t *testing.T) {
	t.Setenv("HOME", "/Users/host")
	t.Setenv("XDG_CACHE_HOME", "/Users/host/.cache")

	env := envSliceToMap(buildEnv(map[string]string{
		"HOME":             "/Users/request",
		"NPM_CONFIG_CACHE": "/tmp/custom-npm-cache",
		"CUSTOM":           "value",
	}))

	if env["HOME"] != defaultHome {
		t.Fatalf("expected HOME to be normalized to %q, got %q", defaultHome, env["HOME"])
	}
	if env["XDG_CACHE_HOME"] != defaultHome+"/.cache" {
		t.Fatalf("expected XDG_CACHE_HOME to be normalized, got %q", env["XDG_CACHE_HOME"])
	}
	if env["NPM_CONFIG_CACHE"] != "/tmp/custom-npm-cache" {
		t.Fatalf("expected Linux override to win, got %q", env["NPM_CONFIG_CACHE"])
	}
	if env["CUSTOM"] != "value" {
		t.Fatalf("expected custom env var to be preserved, got %q", env["CUSTOM"])
	}
}

func TestMergeMiseEnvPrependsPathAndOverridesValues(t *testing.T) {
	merged := envSliceToMap(mergeMiseEnv([]string{
		"PATH=/usr/bin:/bin",
		"FOO=old",
	}, map[string]string{
		"PATH": "/mise/bin",
		"FOO":  "new",
		"BAR":  "added",
	}))

	if merged["PATH"] != "/mise/bin:/usr/bin:/bin" {
		t.Fatalf("expected PATH to be prepended, got %q", merged["PATH"])
	}
	if merged["FOO"] != "new" {
		t.Fatalf("expected FOO to be overridden, got %q", merged["FOO"])
	}
	if merged["BAR"] != "added" {
		t.Fatalf("expected BAR to be added, got %q", merged["BAR"])
	}
}

func TestStreamResultAppendsExitCodeLine(t *testing.T) {
	rec := httptest.NewRecorder()

	streamResult(rec, 17, "hello")

	if body := rec.Body.String(); body != "hello\n"+exitCodePrefix+"17\n" {
		t.Fatalf("unexpected stream body %q", body)
	}
}

func TestWaitForDoneTimesOutWhenChannelStaysOpen(t *testing.T) {
	done := make(chan struct{})
	if waitForDone(done, 0) {
		t.Fatal("expected open channel to time out")
	}
	close(done)
	if !waitForDone(done, 0) {
		t.Fatal("expected closed channel to return immediately")
	}
}

func TestHandleLogsFollowFlushesFinalBufferedOutput(t *testing.T) {
	jobsMu.Lock()
	prevJobs := jobs
	jobs = make(map[string]*job)
	jobsMu.Unlock()
	t.Cleanup(func() {
		jobsMu.Lock()
		jobs = prevJobs
		jobsMu.Unlock()
	})

	j := &job{
		logBuf:    ringbuffer.New(1024),
		broadcast: make(chan []byte, 1),
		done:      make(chan struct{}),
	}

	jobsMu.Lock()
	jobs["job-1"] = j
	jobsMu.Unlock()
	t.Cleanup(func() {
		jobsMu.Lock()
		delete(jobs, "job-1")
		jobsMu.Unlock()
	})

	req := httptest.NewRequest(http.MethodGet, "/logs?id=job-1&follow=1", nil)
	rec := httptest.NewRecorder()

	handlerDone := make(chan struct{})
	go func() {
		handleLogs(rec, req)
		close(handlerDone)
	}()

	deadline := time.Now().Add(250 * time.Millisecond)
	for !rec.Flushed && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !rec.Flushed {
		t.Fatal("handler did not enter follow mode")
	}

	if _, err := j.logBuf.Write([]byte("done\n")); err != nil {
		t.Fatalf("logBuf.Write returned error: %v", err)
	}
	close(j.done)

	select {
	case <-handlerDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("follow request did not exit after job completion")
	}

	if body := rec.Body.String(); !strings.Contains(body, "done\n") {
		t.Fatalf("expected follow response to include final buffered output, got %q", body)
	}
}

func TestResolveExecutionWorkdirMapsGuestPathIntoMountedTree(t *testing.T) {
	mounts := []api.Mount{
		{HostPath: "/var/lib/devup/shadow/app", GuestPath: "/workspace"},
	}

	got := resolveExecutionWorkdir(mounts, "/workspace/frontend")
	want := filepath.Clean("/var/lib/devup/shadow/app/frontend")
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestOverlayStateDirsUsesStablePerMountIDs(t *testing.T) {
	dirs := overlayStateDirs("job-1", []api.Mount{
		{HostPath: "/mnt/host/a", GuestPath: "/workspace/a"},
		{HostPath: "/mnt/host/b", GuestPath: "/workspace/b"},
	}, true)

	want := []string{
		"/var/lib/devup/overlay/job-1-0",
		"/var/lib/devup/overlay/job-1-1",
	}
	if strings.Join(dirs, ",") != strings.Join(want, ",") {
		t.Fatalf("expected %v, got %v", want, dirs)
	}
}

func oversizedRunRequestBody() string {
	req := api.RunRequest{
		RequestID: "req-1",
		Cmd:       []string{"echo", "ok"},
		Env: map[string]string{
			"BIG": strings.Repeat("x", jsonRequestMaxBytes),
		},
	}
	return marshalRequest(req)
}

func oversizedStartRequestBody() string {
	req := api.StartRequest{
		RequestID: "req-2",
		Cmd:       []string{"echo", "ok"},
		Env: map[string]string{
			"BIG": strings.Repeat("x", jsonRequestMaxBytes),
		},
	}
	return marshalRequest(req)
}

func marshalRequest(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return buf.String()
}

func envSliceToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			out[parts[0]] = parts[1]
		}
	}
	return out
}
