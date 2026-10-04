package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config is the configuration of the dev server.
type Config struct {
	// Workspace is the root directory that contains the modules.
	Workspace string `json:"workspace"`
	// Address is the TCP listen address, e.g. ":8080".
	Address string `json:"address"`
	// DebounceWindow merges bursts of change notifications.
	DebounceWindow Duration `json:"debounce_window"`
	// BuildTimeout bounds a single build cycle.
	BuildTimeout Duration `json:"build_timeout"`
	// MaxParallelBuilds limits concurrent per-module builds.
	MaxParallelBuilds int `json:"max_parallel_builds"`
	// WatchExtra lists additional directories watched in addition to module dirs.
	WatchExtra []string `json:"watch_extra"`
	// WatchPollInterval is the filesystem polling interval.
	WatchPollInterval Duration `json:"watch_poll_interval"`
	// Modules describes the modules of the workspace.
	Modules []ModuleConfig `json:"modules"`
}

// ModuleConfig describes a single module.
type ModuleConfig struct {
	// Name is the unique module name.
	Name string `json:"name"`
	// Dir is the module source directory, relative to Workspace.
	Dir string `json:"dir"`
	// Deps lists the names of modules this module depends on.
	Deps []string `json:"deps"`
	// Command is the optional build command run for the module.
	// Working directory is the module directory.
	Command []string `json:"command,omitempty"`
	// Include selects files (relative globs) that become artifacts when no Command.
	Include []string `json:"include,omitempty"`
	// ArtifactDir is where Command writes its outputs (relative to module dir).
	ArtifactDir string `json:"artifact_dir,omitempty"`
}

// Duration wraps time.Duration so it can be read from JSON strings
// such as "100ms" or "2s".
type Duration time.Duration

// Std returns the wrapped standard library duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(raw []byte) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"200ms\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Default returns a config with sensible defaults.
func Default() *Config {
	return &Config{
		Address:           ":8080",
		DebounceWindow:    Duration(150 * time.Millisecond),
		BuildTimeout:      Duration(30 * time.Second),
		MaxParallelBuilds: 4,
		WatchPollInterval: Duration(200 * time.Millisecond),
	}
}

// Load reads and validates a JSON config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks the configuration and fills implied defaults.
func (c *Config) Validate() error {
	if c.Workspace == "" {
		c.Workspace = "."
	}
	if abs, err := filepath.Abs(c.Workspace); err == nil {
		c.Workspace = abs
	}
	if c.Address == "" {
		c.Address = ":8080"
	}
	if c.DebounceWindow < 0 {
		return fmt.Errorf("debounce_window must not be negative")
	}
	if c.BuildTimeout <= 0 {
		return fmt.Errorf("build_timeout must be positive")
	}
	if c.MaxParallelBuilds <= 0 {
		c.MaxParallelBuilds = 1
	}
	if c.WatchPollInterval <= 0 {
		c.WatchPollInterval = Duration(200 * time.Millisecond)
	}
	if len(c.Modules) == 0 {
		return fmt.Errorf("no modules configured")
	}
	seen := map[string]bool{}
	for i := range c.Modules {
		m := &c.Modules[i]
		if m.Name == "" {
			return fmt.Errorf("modules[%d]: name is required", i)
		}
		if seen[m.Name] {
			return fmt.Errorf("duplicate module name %q", m.Name)
		}
		seen[m.Name] = true
		if m.Dir == "" {
			m.Dir = m.Name
		}
	}
	for _, m := range c.Modules {
		for _, dep := range m.Deps {
			if !seen[dep] {
				return fmt.Errorf("module %q depends on unknown module %q", m.Name, dep)
			}
			if dep == m.Name {
				return fmt.Errorf("module %q depends on itself", m.Name)
			}
		}
	}
	return nil
}
