package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaultsAndDurations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	content := `{
		"workspace": ".",
		"address": ":9999",
		"debounce_window": "75ms",
		"build_timeout": "5s",
		"max_parallel_builds": 3,
		"watch_poll_interval": "50ms",
		"watch_extra": ["extra"],
		"modules": [
			{"name": "lib", "dir": "lib"},
			{"name": "app", "dir": "app", "deps": ["lib"], "include": ["**/*.js"]}
		]
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DebounceWindow.Std() != 75*time.Millisecond {
		t.Fatalf("debounce = %v", cfg.DebounceWindow)
	}
	if cfg.BuildTimeout.Std() != 5*time.Second {
		t.Fatalf("timeout = %v", cfg.BuildTimeout)
	}
	if cfg.MaxParallelBuilds != 3 || cfg.Address != ":9999" {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
	if cfg.Workspace, _ = filepath.Abs(cfg.Workspace); !filepath.IsAbs(cfg.Workspace) {
		t.Fatal("workspace should be made absolute")
	}
}

func TestRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"no modules":      `{"workspace":".","build_timeout":"1s","modules":[]}`,
		"bad duration":    `{"workspace":".","build_timeout":"1s","debounce_window":"abc","modules":[{"name":"a"}]}`,
		"duplicate names": `{"workspace":".","build_timeout":"1s","modules":[{"name":"a"},{"name":"a"}]}`,
		"unknown dep":     `{"workspace":".","build_timeout":"1s","modules":[{"name":"a","deps":["x"]}]}`,
		"zero timeout":    `{"workspace":".","build_timeout":"0s","modules":[{"name":"a"}]}`,
	}
	for name, body := range cases {
		p := filepath.Join(dir, name+".json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
}
