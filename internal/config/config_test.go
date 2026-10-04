package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAppliesDefaultsAndParsesDurations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hmr.json")
	content := `{
		"modules": [
			{"name": "app", "root": "src/app", "deps": ["lib"]},
			{"name": "lib", "root": "src/lib"}
		],
		"watch": {"roots": ["src"], "debounce": "75ms", "pollInterval": "25ms", "exclude": ["*.tmp"]},
		"build": {"concurrency": 3, "timeout": "10s"},
		"server": {"addr": ":9191"}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Watch.Debounce.Duration != 75*time.Millisecond {
		t.Fatalf("debounce = %v", cfg.Watch.Debounce)
	}
	if cfg.Build.Concurrency != 3 || cfg.Build.Timeout.Duration != 10*time.Second {
		t.Fatalf("build cfg wrong: %+v", cfg.Build)
	}
	if cfg.Server.Addr != ":9191" {
		t.Fatalf("addr = %s", cfg.Server.Addr)
	}
	if cfg.Workspace != dir {
		t.Fatalf("workspace = %s want %s", cfg.Workspace, dir)
	}
}

func TestDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hmr.json")
	os.WriteFile(path, []byte(`{"modules":[{"name":"a","root":"a"}]}`), 0o644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Build.Concurrency != 1 {
		t.Fatal("default concurrency should be 1")
	}
	if cfg.Server.Addr != ":8080" {
		t.Fatalf("default addr = %s", cfg.Server.Addr)
	}
}

func TestValidation(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"no modules":  `{}`,
		"unknown dep": `{"modules":[{"name":"a","root":"a","deps":["x"]}]}`,
		"duplicate":   `{"modules":[{"name":"a","root":"a"},{"name":"a","root":"b"}]}`,
		"self dep":    `{"modules":[{"name":"a","root":"a","deps":["a"]}]}`,
		"empty name":  `{"modules":[{"name":"","root":"a"}]}`,
	}
	for name, body := range cases {
		path := filepath.Join(dir, "hmr.json")
		os.WriteFile(path, []byte(body), 0o644)
		if _, err := Load(path); err == nil {
			t.Fatalf("case %q: expected validation error", name)
		}
	}
}
