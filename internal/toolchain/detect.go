package toolchain

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Requirement describes a single tool needed by a workspace.
type Requirement struct {
	Tool    string // mise tool name: "node", "go", "python", "rust", "ruby", "java", "php"
	Version string // version spec: "20", "1.22", "3.12", "latest"
	Source  string // file that triggered detection: "package.json", "go.mod", etc.
}

// DetectResult holds the output of workspace language detection.
type DetectResult struct {
	Reqs    []Requirement
	HasMise bool // .mise.toml or .tool-versions exists; mise handles everything
}

// Detect scans workdir for language marker files and returns tool requirements.
// If a .mise.toml or .tool-versions file already exists, it returns an empty
// list with HasMise=true so the caller defers entirely to mise's native config.
func Detect(workdir string) DetectResult {
	if fileExists(filepath.Join(workdir, ".mise.toml")) || fileExists(filepath.Join(workdir, ".tool-versions")) {
		return DetectResult{HasMise: true}
	}

	var reqs []Requirement

	if fileExists(filepath.Join(workdir, "package.json")) {
		ver := nodeVersionFromPackageJSON(filepath.Join(workdir, "package.json"))
		reqs = append(reqs, Requirement{Tool: "node", Version: ver, Source: "package.json"})
	}

	if fileExists(filepath.Join(workdir, "go.mod")) {
		ver := goVersionFromMod(filepath.Join(workdir, "go.mod"))
		reqs = append(reqs, Requirement{Tool: "go", Version: ver, Source: "go.mod"})
	}

	for _, f := range []string{"requirements.txt", "pyproject.toml", "setup.py", "Pipfile"} {
		if fileExists(filepath.Join(workdir, f)) {
			reqs = append(reqs, Requirement{Tool: "python", Version: "latest", Source: f})
			break
		}
	}

	if fileExists(filepath.Join(workdir, "Cargo.toml")) {
		reqs = append(reqs, Requirement{Tool: "rust", Version: "latest", Source: "Cargo.toml"})
	}

	if fileExists(filepath.Join(workdir, "Gemfile")) {
		reqs = append(reqs, Requirement{Tool: "ruby", Version: "latest", Source: "Gemfile"})
	}

	for _, f := range []string{"pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"} {
		if fileExists(filepath.Join(workdir, f)) {
			reqs = append(reqs, Requirement{Tool: "java", Version: "latest", Source: f})
			break
		}
	}

	if fileExists(filepath.Join(workdir, "composer.json")) {
		reqs = append(reqs, Requirement{Tool: "php", Version: "latest", Source: "composer.json"})
	}

	return DetectResult{Reqs: reqs}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// nodeVersionFromPackageJSON reads engines.node from package.json.
// Returns "lts" if the field is absent or unparseable.
func nodeVersionFromPackageJSON(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "lts"
	}
	var pkg struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil || pkg.Engines.Node == "" {
		return "lts"
	}
	ver := strings.TrimLeft(pkg.Engines.Node, ">=^~ ")
	if ver == "" {
		return "lts"
	}
	return ver
}

// goVersionFromMod parses the `go X.Y` directive from go.mod.
// Returns "latest" if not found.
func goVersionFromMod(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "latest"
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "go ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return "latest"
}
