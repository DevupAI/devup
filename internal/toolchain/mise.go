package toolchain

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"devup/internal/logging"
)

const (
	MiseBin     = "/usr/local/bin/mise"
	MiseDataDir = "/opt/devup/mise"
)

// EnsureForWorkdir detects workspace requirements and installs toolchains via mise.
// Returns env vars (including updated PATH) that should be merged into the job env.
// Non-fatal: returns nil map on error so the job can still run with base toolchains.
func EnsureForWorkdir(workdir string) (map[string]string, error) {
	if _, err := exec.LookPath(MiseBin); err != nil {
		return nil, fmt.Errorf("mise not installed: %w", err)
	}

	result := Detect(workdir)

	if result.HasMise {
		return EnvForWorkdir(workdir)
	}

	if len(result.Reqs) == 0 {
		return nil, nil
	}

	reqs := filterProvisionedRequirements(result.Reqs)
	if len(reqs) == 0 {
		return nil, nil
	}

	if err := Ensure(workdir, reqs); err != nil {
		return nil, err
	}

	return EnvForWorkdir(workdir)
}

// Ensure installs each required tool via mise. Idempotent -- fast if already cached.
func Ensure(workdir string, reqs []Requirement) error {
	for _, r := range reqs {
		spec := r.Tool + "@" + r.Version
		logging.Info("mise install", "tool", spec, "source", r.Source)

		cmd := exec.Command(MiseBin, "install", spec)
		cmd.Dir = workdir
		cmd.Env = miseBaseEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			logging.Error("mise install failed", "tool", spec, "err", err, "output", string(out))
			return fmt.Errorf("mise install %s: %w\n%s", spec, err, out)
		}
	}
	return nil
}

// EnvForWorkdir runs `mise env -s bash` in the given directory and parses
// the `export KEY=VALUE` lines into a map. Works with native .mise.toml /
// .tool-versions files as well as tools installed by Ensure.
func EnvForWorkdir(workdir string) (map[string]string, error) {
	cmd := exec.Command(MiseBin, "env", "-s", "bash")
	cmd.Dir = workdir
	cmd.Env = miseBaseEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("mise env: %w", err)
	}
	return parseBashExports(out), nil
}

func parseBashExports(data []byte) map[string]string {
	env := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "export ") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		key := line[:idx]
		val := strings.Trim(line[idx+1:], "\"'")
		env[key] = val
	}
	return env
}

func miseBaseEnv() []string {
	return []string{
		"HOME=/root",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"MISE_DATA_DIR=" + MiseDataDir,
		"MISE_YES=1",
	}
}

func filterProvisionedRequirements(reqs []Requirement) []Requirement {
	filtered := make([]Requirement, 0, len(reqs))
	for _, req := range reqs {
		if shouldUseSystemTool(req) {
			continue
		}
		filtered = append(filtered, req)
	}
	return filtered
}

func shouldUseSystemTool(req Requirement) bool {
	switch req.Tool {
	case "python", "rust":
		if req.Version != "latest" {
			return false
		}
		binary := "python3"
		if req.Tool == "rust" {
			binary = "rustc"
		}
		_, err := exec.LookPath(binary)
		return err == nil
	case "ruby", "java", "php":
		if req.Version != "latest" {
			return false
		}
		binary := req.Tool
		if req.Tool == "php" {
			binary = "php"
		}
		_, err := exec.LookPath(binary)
		return err == nil
	default:
		return false
	}
}
