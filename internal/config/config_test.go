package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirectories(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir failed: %v", err)
	}
	if expected := filepath.Join(tempHome, ".invariant"); configDir != expected {
		t.Errorf("Expected configDir %s, got %s", expected, configDir)
	}

	cacheDir, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir failed: %v", err)
	}
	if expected := filepath.Join(tempHome, ".cache", "invariant"); cacheDir != expected {
		t.Errorf("Expected cacheDir %s, got %s", expected, cacheDir)
	}

	keysDir, err := KeysDir()
	if err != nil {
		t.Fatalf("KeysDir failed: %v", err)
	}
	if expected := filepath.Join(tempHome, ".invariant", "keys"); keysDir != expected {
		t.Errorf("Expected keysDir %s, got %s", expected, keysDir)
	}

	info, err := os.Stat(keysDir)
	if err != nil {
		t.Fatalf("Stat keysDir failed: %v", err)
	}
	// Check permissions are 0700
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("Expected keysDir permissions 0700, got %o", perm)
	}
}

func TestLoad_NonExistent(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load on non-existent config failed: %v", err)
	}
	if cfg == nil {
		t.Fatalf("Expected non-nil config")
	}
	if cfg.Discovery != "" {
		t.Errorf("Expected empty discovery, got %s", cfg.Discovery)
	}
}

func TestLoad_ValidConfig(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir failed: %v", err)
	}

	yamlContent := "discovery: http://discovery.example.com:8080\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Discovery != "http://discovery.example.com:8080" {
		t.Errorf("Expected discovery URL 'http://discovery.example.com:8080', got %s", cfg.Discovery)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir failed: %v", err)
	}

	invalidYAML := ": invalid yaml content {"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(invalidYAML), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	_, err = Load()
	if err == nil {
		t.Errorf("Expected error loading invalid YAML, got nil")
	}
}
