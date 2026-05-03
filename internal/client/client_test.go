package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"devup/internal/api"
)

func TestRunIgnoresResponseHeaderTimeoutDuringSlowPreparation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		fmt.Fprintln(w, "hello")
		fmt.Fprintln(w, ExitCodePrefix+"0")
	}))
	defer srv.Close()

	c := &Client{
		baseURL: srv.URL,
		token:   "test-token",
		http: &http.Client{
			Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Millisecond},
			Timeout:   ConnectTimeout,
		},
	}

	var out strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	exitCode, err := c.Run(ctx, &api.RunRequest{Cmd: []string{"echo", "ok"}}, &out)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(out.String(), "hello") {
		t.Fatalf("expected output to contain hello, got %q", out.String())
	}
}

func TestStartIgnoresResponseHeaderTimeoutDuringSlowPreparation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"job_id":"job-123"}`)
	}))
	defer srv.Close()

	c := &Client{
		baseURL: srv.URL,
		token:   "test-token",
		http: &http.Client{
			Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Millisecond},
			Timeout:   ConnectTimeout,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	jobID, err := c.Start(ctx, &api.StartRequest{Cmd: []string{"sleep", "1"}})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if jobID != "job-123" {
		t.Fatalf("expected job id job-123, got %q", jobID)
	}
}
