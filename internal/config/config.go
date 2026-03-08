package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	ConfigDir  = ".devup"
	ConfigFile = "config.json"
)

// Config holds host configuration
type Config struct {
	Token string `json:"token"`
}

// Path returns the config file path (~/.devup/config.json)
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ConfigDir, ConfigFile), nil
}

// Load reads config from disk; creates with new token if missing
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return Create()
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Token == "" {
		return nil, fmt.Errorf("config missing token; run 'devup vm reset-token'")
	}
	return &c, nil
}

// Create creates config dir, generates token, writes config (0600)
func Create() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("mkdir config: %w", err)
	}
	token, err := generateToken()
	if err != nil {
		return nil, err
	}
	c := &Config{Token: token}
	if err := c.Save(); err != nil {
		return nil, err
	}
	return c, nil
}

// Save writes config to disk with 0600
func (c *Config) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0600)
}

// ResetToken generates a new token and saves
func (c *Config) ResetToken() error {
	token, err := generateToken()
	if err != nil {
		return err
	}
	c.Token = token
	return c.Save()
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
