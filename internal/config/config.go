// Package config defines runtime configuration for the incremental
// build and hot-reload server.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config is the root configuration object.
type Config struct {
	// Workspace is the root directory modules are relative to.
	// Defaults to the directory containing the config file.
	Workspace string `json:"workspace,omitempty"`
	// Modules declares the build modules in the workspace.
	Modules []Module `json:"modules"`
	// Watch configures filesystem change detection.
	Watch WatchConfig `json:"watch"`
	// Build configures build execution.
	Build BuildConfig `json:"build"`
	// Server configures the HTTP server.
	Server ServerConfig `json:"server"`
	// ArtifactDir is where built artifacts are stored, relative to the
	// workspace. Defaults to ".hmr/artifacts".
	ArtifactDir string `json:"artifactDir,omitempty"`
}

// Module describes a single build module.
type Module struct {
	// Name is the unique module identifier.
	Name string `json:"name"`
	// Root is the module source directory, relative to the workspace root.
	Root string `json:"root"`
	// Deps lists the names of modules this module depends on.
	Deps []string `json:"deps,omitempty"`
	// Command is the build command run inside Root. If empty the file
	// hashes alone are used and the artifact is a synthetic summary.
	Command string `json:"command,omitempty"`
	// Args are the arguments passed to Command.
	Args []string `json:"args,omitempty"`
	// Artifact is the file (relative to Root) produced by the build.
	// When empty, the command's stdout is used as the artifact.
	Artifact string `json:"artifact,omitempty"`
}

// WatchConfig configures filesystem watching.
type WatchConfig struct {
	// Roots limits watch scope to these workspace-relative directories.
	// Empty means every module root is watched.
	Roots []string `json:"roots,omitempty"`
	// Include further limits watching to matching paths (glob, workspace relative).
	Include []string `json:"include,omitempty"`
	// Exclude removes paths from watching (glob, workspace relative).
	Exclude []string `json:"exclude,omitempty"`
	// PollInterval is the polling interval for change detection.
	PollInterval Duration `json:"pollInterval"`
	// Debounce merges repeated notifications within this time window.
	Debounce Duration `json:"debounce"`
}

// BuildConfig configures build execution.
type BuildConfig struct {
	// Concurrency is the maximum number of module builds run in parallel.
	Concurrency int `json:"concurrency"`
	// Timeout bounds a single module build. Zero means no timeout.
	Timeout Duration `json:"timeout"`
}

// ServerConfig configures the HTTP server.
type ServerConfig struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string `json:"addr"`
}

// Duration wraps time.Duration so it can be unmarshaled from JSON text
// such as "250ms" and marshaled back the same way.
type Duration struct {
	time.Duration
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case string:
		parsed, err := time.ParseDuration(t)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", t, err)
		}
		d.Duration = parsed
		return nil
	case float64:
		d.Duration = time.Duration(t)
		return nil
	default:
		return fmt.Errorf("duration must be a string or number, got %T", v)
	}
}

// Load reads, parses and validates a config file, applying defaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Workspace == "" {
		if abs, err := filepath.Abs(path); err == nil {
			cfg.Workspace = filepath.Dir(abs)
		}
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.ArtifactDir == "" {
		c.ArtifactDir = ".hmr/artifacts"
	}
	if c.Watch.PollInterval.Duration == 0 {
		c.Watch.PollInterval.Duration = 100 * time.Millisecond
	}
	if c.Watch.Debounce.Duration == 0 {
		c.Watch.Debounce.Duration = 200 * time.Millisecond
	}
	if c.Build.Concurrency <= 0 {
		c.Build.Concurrency = 1
	}
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
}

// Validate checks structural config constraints.
func (c *Config) Validate() error {
	if len(c.Modules) == 0 {
		return fmt.Errorf("config: at least one module is required")
	}
	seen := make(map[string]bool, len(c.Modules))
	for _, m := range c.Modules {
		if m.Name == "" {
			return fmt.Errorf("config: module with empty name")
		}
		if m.Root == "" {
			return fmt.Errorf("config: module %q has empty root", m.Name)
		}
		if seen[m.Name] {
			return fmt.Errorf("config: duplicate module name %q", m.Name)
		}
		seen[m.Name] = true
	}
	for _, m := range c.Modules {
		for _, d := range m.Deps {
			if !seen[d] {
				return fmt.Errorf("config: module %q depends on unknown module %q", m.Name, d)
			}
			if d == m.Name {
				return fmt.Errorf("config: module %q depends on itself", m.Name)
			}
		}
	}
	if c.Build.Concurrency < 0 {
		return fmt.Errorf("config: build concurrency must be >= 0")
	}
	if c.Watch.PollInterval.Duration < 0 || c.Watch.Debounce.Duration < 0 {
		return fmt.Errorf("config: watch intervals must be >= 0")
	}
	return nil
}

// ModuleByName returns the module with the given name, or false.
func (c *Config) ModuleByName(name string) (Module, bool) {
	for _, m := range c.Modules {
		if m.Name == name {
			return m, true
		}
	}
	return Module{}, false
}
