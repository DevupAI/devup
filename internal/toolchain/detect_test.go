package toolchain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectPrefersExistingMiseConfig(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, ".mise.toml"), []byte("[tools]\n"), 0644); err != nil {
		t.Fatalf("write mise config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "package.json"), []byte(`{"engines":{"node":">=20"}}`), 0644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	result := Detect(workdir)
	if !result.HasMise {
		t.Fatal("expected HasMise to be true")
	}
	if len(result.Reqs) != 0 {
		t.Fatalf("expected no detected requirements when mise config exists, got %d", len(result.Reqs))
	}
}

func TestDetectCollectsWorkspaceRequirements(t *testing.T) {
	workdir := t.TempDir()
	files := map[string]string{
		"package.json":     `{"engines":{"node":"^20.11.0"}}`,
		"go.mod":           "module devup\n\ngo 1.23.4\n",
		"requirements.txt": "flask==3.0.0\n",
		"Cargo.toml":       "[package]\nname = \"demo\"\n",
		"Gemfile":          "source 'https://rubygems.org'\n",
		"pom.xml":          "<project/>",
		"composer.json":    `{"name":"demo/app"}`,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(workdir, name), []byte(contents), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	result := Detect(workdir)
	if result.HasMise {
		t.Fatal("expected HasMise to be false")
	}

	expected := []Requirement{
		{Tool: "node", Version: "20.11.0", Source: "package.json"},
		{Tool: "go", Version: "1.23.4", Source: "go.mod"},
		{Tool: "python", Version: "latest", Source: "requirements.txt"},
		{Tool: "rust", Version: "latest", Source: "Cargo.toml"},
		{Tool: "ruby", Version: "latest", Source: "Gemfile"},
		{Tool: "java", Version: "latest", Source: "pom.xml"},
		{Tool: "php", Version: "latest", Source: "composer.json"},
	}
	if len(result.Reqs) != len(expected) {
		t.Fatalf("expected %d requirements, got %d", len(expected), len(result.Reqs))
	}
	for i, req := range expected {
		if result.Reqs[i] != req {
			t.Fatalf("requirement %d mismatch: want %+v, got %+v", i, req, result.Reqs[i])
		}
	}
}

func TestNodeVersionFromPackageJSONFallbacks(t *testing.T) {
	workdir := t.TempDir()

	invalid := filepath.Join(workdir, "invalid.json")
	if err := os.WriteFile(invalid, []byte("{"), 0644); err != nil {
		t.Fatalf("write invalid package.json: %v", err)
	}
	if got := nodeVersionFromPackageJSON(invalid); got != "lts" {
		t.Fatalf("expected invalid JSON to fall back to lts, got %q", got)
	}

	noEngines := filepath.Join(workdir, "no-engines.json")
	if err := os.WriteFile(noEngines, []byte(`{"name":"demo"}`), 0644); err != nil {
		t.Fatalf("write package.json without engines: %v", err)
	}
	if got := nodeVersionFromPackageJSON(noEngines); got != "lts" {
		t.Fatalf("expected missing engines to fall back to lts, got %q", got)
	}
}

func TestGoVersionFromModFallbacks(t *testing.T) {
	workdir := t.TempDir()

	goMod := filepath.Join(workdir, "go.mod")
	if err := os.WriteFile(goMod, []byte("module demo\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if got := goVersionFromMod(goMod); got != "latest" {
		t.Fatalf("expected missing go directive to return latest, got %q", got)
	}

	if got := goVersionFromMod(filepath.Join(workdir, "missing.mod")); got != "latest" {
		t.Fatalf("expected missing file to return latest, got %q", got)
	}
}

func TestFilterProvisionedRequirementsSkipsLatestSystemRuntimes(t *testing.T) {
	reqs := []Requirement{
		{Tool: "python", Version: "latest", Source: "requirements.txt"},
		{Tool: "rust", Version: "latest", Source: "Cargo.toml"},
		{Tool: "ruby", Version: "latest", Source: "Gemfile"},
		{Tool: "java", Version: "latest", Source: "pom.xml"},
		{Tool: "php", Version: "latest", Source: "composer.json"},
		{Tool: "go", Version: "1.25.4", Source: "go.mod"},
	}

	filtered := filterProvisionedRequirements(reqs)
	if len(filtered) == 0 {
		t.Fatal("expected at least one requirement to remain")
	}
	foundGo := false
	for _, req := range filtered {
		if req.Tool == "go" {
			foundGo = true
		}
		if shouldUseSystemTool(req) {
			t.Fatalf("expected provisioned system tool to be filtered out, got %#v", req)
		}
	}
	if !foundGo {
		t.Fatalf("expected go requirement to remain, got %#v", filtered)
	}
}
