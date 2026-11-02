package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStartupConfigCreatesMissingFile(t *testing.T) {
	t.Setenv("WB2A_API_KEY", "")
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := loadStartupConfig(path)
	if err != nil {
		t.Fatalf("loadStartupConfig: %v", err)
	}
	if cfg.APIKey == "" {
		t.Fatal("generated config must have an api key")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("generated config missing: %v", err)
	}
}

func TestLoadStartupConfigFailsClosedWhenDefaultWriteFails(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent-file")
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadStartupConfig(filepath.Join(parent, "config.json"))
	if err == nil {
		t.Fatal("expected default-write failure")
	}
}

func TestLoadStartupConfigPreservesExistingAndEnvOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{"api_key":"file-key","listen":":9001"}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB2A_API_KEY", "env-key")
	cfg, err := loadStartupConfig(path)
	if err != nil {
		t.Fatalf("loadStartupConfig: %v", err)
	}
	if cfg.APIKey != "env-key" || cfg.Listen != ":9001" {
		t.Fatalf("cfg=%+v", cfg)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != original {
		t.Fatalf("existing config changed: %q (%v)", raw, err)
	}
}

func TestLoadStartupConfigMalformedFileIsNotOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := "{malformed"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadStartupConfig(path)
	if err == nil || !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("err=%v want parse error", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || string(raw) != original {
		t.Fatalf("malformed config changed: %q (%v)", raw, readErr)
	}
}

func TestLoadStartupConfigEmptyPathUsesEnvironment(t *testing.T) {
	t.Setenv("WB2A_API_KEY", "env-only-key")
	cfg, err := loadStartupConfig("")
	if err != nil {
		t.Fatalf("loadStartupConfig: %v", err)
	}
	if cfg.APIKey != "env-only-key" {
		t.Fatalf("api_key=%q want env-only-key", cfg.APIKey)
	}
}
