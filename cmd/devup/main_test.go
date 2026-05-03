package main

import (
	"testing"

	"devup/internal/api"
)

func TestParseLogsArgsSupportsFlagAfterJobID(t *testing.T) {
	jobID, follow := parseLogsArgs([]string{"abc123", "-f"})
	if jobID != "abc123" {
		t.Fatalf("expected job id abc123, got %q", jobID)
	}
	if !follow {
		t.Fatal("expected follow=true when -f appears after job id")
	}
}

func TestParseLogsArgsSupportsFlagBeforeJobID(t *testing.T) {
	jobID, follow := parseLogsArgs([]string{"--follow", "abc123"})
	if jobID != "abc123" {
		t.Fatalf("expected job id abc123, got %q", jobID)
	}
	if !follow {
		t.Fatal("expected follow=true when --follow appears before job id")
	}
}

func TestParseRunStartFlagsParsesProfile(t *testing.T) {
	opts, err := parseRunStartFlags([]string{"--profile", "interactive"})
	if err != nil {
		t.Fatalf("parseRunStartFlags returned error: %v", err)
	}
	if opts.Profile != api.ProfileInteractive {
		t.Fatalf("expected interactive profile, got %q", opts.Profile)
	}
}

func TestParseRunStartFlagsShadowImpliesOverlay(t *testing.T) {
	opts, err := parseRunStartFlags([]string{"--shadow"})
	if err != nil {
		t.Fatalf("parseRunStartFlags returned error: %v", err)
	}
	if !opts.Shadow {
		t.Fatal("expected shadow mode to be enabled")
	}
	if !opts.Overlay {
		t.Fatal("expected shadow mode to imply overlay")
	}
}

func TestFormatJobMemorySupportsUnlimitedMax(t *testing.T) {
	got := formatJobMemory(&api.MemoryStatus{
		CurrentMB: 64,
		LowMB:     96,
		HighMB:    384,
		MaxMB:     0,
	})
	if got != "64/96/384/max" {
		t.Fatalf("unexpected memory string %q", got)
	}
}

func TestParseAppLogsFlagsSupportsFollowAfterService(t *testing.T) {
	manifestPath, service, follow, err := parseAppLogsFlags([]string{"web", "-f", "--file", "devup.app.yaml"})
	if err != nil {
		t.Fatalf("parseAppLogsFlags returned error: %v", err)
	}
	if service != "web" {
		t.Fatalf("expected service web, got %q", service)
	}
	if manifestPath != "devup.app.yaml" {
		t.Fatalf("expected manifest path devup.app.yaml, got %q", manifestPath)
	}
	if !follow {
		t.Fatal("expected follow=true when -f appears after service")
	}
}

func TestParseAppFlagsParsesFileAndServices(t *testing.T) {
	manifestPath, services, err := parseAppFlags([]string{"--file", "devup.app.yaml", "api", "web"})
	if err != nil {
		t.Fatalf("parseAppFlags returned error: %v", err)
	}
	if manifestPath != "devup.app.yaml" {
		t.Fatalf("expected manifest path devup.app.yaml, got %q", manifestPath)
	}
	if len(services) != 2 || services[0] != "api" || services[1] != "web" {
		t.Fatalf("unexpected services %#v", services)
	}
}
