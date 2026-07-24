// Package config loads and persists megadl settings.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	appName       = "megadl"
	legacyAppName = "megatui"
)

type Config struct {
	DownloadDir string `json:"download_dir"`
}

func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", appName, "config.json"), nil
}

func legacyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", legacyAppName, "config.json"), nil
}

// Load reads the config file, creating it with defaults on first run.
func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}

	cfg := defaults()
	sourcePath := p
	migrate := false
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		legacy, pathErr := legacyPath()
		if pathErr != nil {
			return nil, pathErr
		}
		data, err = os.ReadFile(legacy)
		if err == nil {
			sourcePath = legacy
			migrate = true
		}
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := cfg.save(p); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", sourcePath, err)
		}
	}

	if migrate {
		if err := cfg.save(p); err != nil {
			return nil, fmt.Errorf("migrate config to %s: %w", p, err)
		}
	}
	cfg.DownloadDir = expandHome(cfg.DownloadDir)
	if err := os.MkdirAll(cfg.DownloadDir, 0o755); err != nil {
		return nil, fmt.Errorf("create download dir: %w", err)
	}
	return cfg, nil
}

func defaults() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		DownloadDir: filepath.Join(home, "Media", "mega"),
	}
}

func (c *Config) save(p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o644)
}

// DBPath returns the SQLite path; the DB lives with the library.
func (c *Config) DBPath() string {
	current := filepath.Join(c.DownloadDir, ".megadl.db")
	if _, err := os.Stat(current); err == nil || !errors.Is(err, os.ErrNotExist) {
		return current
	}

	legacy := filepath.Join(c.DownloadDir, ".megatui.db")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return current
}

func expandHome(p string) string {
	if len(p) >= 2 && p[0] == '~' && p[1] == '/' {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
