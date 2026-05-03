package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const appStateDir = "apps"

type AppState struct {
	Name         string                     `json:"name"`
	ManifestPath string                     `json:"manifest_path"`
	UpdatedAt    int64                      `json:"updated_at_unix"`
	Services     map[string]AppServiceState `json:"services"`
}

type AppServiceState struct {
	JobID     string   `json:"job_id"`
	Profile   string   `json:"profile,omitempty"`
	Cmd       []string `json:"cmd,omitempty"`
	StartedAt int64    `json:"started_at_unix,omitempty"`
}

func AppStatePath(manifestPath string) (string, error) {
	absPath, err := filepath.Abs(manifestPath)
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	sum := sha256.Sum256([]byte(absPath))
	fileName := hex.EncodeToString(sum[:8]) + ".json"
	return filepath.Join(home, ConfigDir, appStateDir, fileName), nil
}

func ReadAppState(manifestPath string) (*AppState, error) {
	p, err := AppStatePath(manifestPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no app state for %s", manifestPath)
		}
		return nil, err
	}
	var state AppState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Services == nil {
		state.Services = make(map[string]AppServiceState)
	}
	return &state, nil
}

func WriteAppState(manifestPath string, state *AppState) error {
	p, err := AppStatePath(manifestPath)
	if err != nil {
		return err
	}
	if state.ManifestPath == "" {
		absPath, err := filepath.Abs(manifestPath)
		if err != nil {
			return err
		}
		state.ManifestPath = absPath
	}
	if state.Services == nil {
		state.Services = make(map[string]AppServiceState)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

func DeleteAppState(manifestPath string) error {
	p, err := AppStatePath(manifestPath)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func HasAppState(manifestPath string) bool {
	p, err := AppStatePath(manifestPath)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}
