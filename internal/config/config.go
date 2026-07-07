// Package config loads mrep's TOML configuration (HLD §13), resolving
// ~-prefixed paths and creating the config file with defaults on first run.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const configRelPath = ".config/mrep/config.toml"

// Config mirrors HLD §13. Fields are exported for toml (de)serialization.
type Config struct {
	Port          int    `toml:"port"`
	DataDir       string `toml:"data_dir"`
	SaveDir       string `toml:"save_dir"`
	RetentionDays int    `toml:"retention_days"`
	DeviceName    string `toml:"device_name"`
	HistoryLimit  int    `toml:"history_limit"`
	TLS           bool   `toml:"tls"`
	RequireToken  bool   `toml:"require_token"`
}

// Default returns the built-in defaults from HLD §13.
func Default() Config {
	return Config{
		Port:          8787,
		DataDir:       "~/.local/share/mrep",
		SaveDir:       "~/Downloads",
		RetentionDays: 7,
		DeviceName:    "laptop",
		HistoryLimit:  200,
		TLS:           false,
		RequireToken:  true,
	}
}

// Load reads ~/.config/mrep/config.toml, creating it with defaults if it
// doesn't exist, then expands ~-prefixed paths into absolute ones.
func Load() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve home dir: %w", err)
	}
	path := filepath.Join(home, configRelPath)

	cfg := Default()

	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		if err := writeDefault(path, cfg); err != nil {
			return Config{}, fmt.Errorf("write default config: %w", err)
		}
	case err != nil:
		return Config{}, fmt.Errorf("read config: %w", err)
	default:
		if err := toml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	cfg.DataDir = expandHome(cfg.DataDir, home)
	cfg.SaveDir = expandHome(cfg.SaveDir, home)

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return Config{}, fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "files"), 0o755); err != nil {
		return Config{}, fmt.Errorf("create files dir: %w", err)
	}
	if err := os.MkdirAll(cfg.SaveDir, 0o755); err != nil {
		return Config{}, fmt.Errorf("create save dir: %w", err)
	}

	return cfg, nil
}

func writeDefault(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// DBPath returns the path to the SQLite database file inside DataDir.
func (c Config) DBPath() string {
	return filepath.Join(c.DataDir, "mrep.db")
}

// FilesDir returns the path to the blob storage directory inside DataDir.
func (c Config) FilesDir() string {
	return filepath.Join(c.DataDir, "files")
}
