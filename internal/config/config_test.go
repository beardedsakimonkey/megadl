package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadIgnoresLegacyDownloaderSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath, err := path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	downloadDir := filepath.Join(home, "downloads")
	data := fmt.Sprintf(`{
  "download_dir": %q,
  "driver": "megadl",
  "megatools_bin": "/unused/megatools"
}
`, downloadDir)
	if err := os.WriteFile(configPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("legacy config should still load: %v", err)
	}
	if cfg.DownloadDir != downloadDir {
		t.Errorf("download dir = %q, want %q", cfg.DownloadDir, downloadDir)
	}
}

func TestLoadMigratesLegacyAppConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldPath, err := legacyPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	downloadDir := filepath.Join(home, "downloads")
	data := fmt.Sprintf(`{
  "download_dir": %q
}
`, downloadDir)
	if err := os.WriteFile(oldPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DownloadDir != downloadDir {
		t.Errorf("download dir = %q, want %q", cfg.DownloadDir, downloadDir)
	}

	newPath, err := path()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("migrated config: %v", err)
	}
}

func TestConfigPathUsesDotConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath, err := path()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", appName, "config.json")
	if configPath != want {
		t.Errorf("path() = %q, want %q", configPath, want)
	}
}

func TestDBPathUsesLegacyDatabaseWhenPresent(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, ".megatui.db")
	if err := os.WriteFile(legacy, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{DownloadDir: dir}
	if got := cfg.DBPath(); got != legacy {
		t.Errorf("DBPath() = %q, want %q", got, legacy)
	}

	current := filepath.Join(dir, ".megadl.db")
	if err := os.WriteFile(current, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := cfg.DBPath(); got != current {
		t.Errorf("DBPath() with current database = %q, want %q", got, current)
	}
}
