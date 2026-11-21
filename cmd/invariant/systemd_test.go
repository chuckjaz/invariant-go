package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chuckjaz/invariant-go/internal/start"
)

func TestExtractServiceName(t *testing.T) {
	counts := make(map[string]int)

	if got := extractServiceName(start.ServiceConfig{
		Command: "inv-distribute",
		Args:    map[string]string{"name": "distribute-1"},
	}, counts); got != "distribute-1" {
		t.Fatalf("got %q, want distribute-1", got)
	}

	if got := extractServiceName(start.ServiceConfig{
		Command: "inv-storage",
		Args:    map[string]string{"dir": "/var/lib/invariant/storage-1"},
	}, counts); got != "storage-1" {
		t.Fatalf("got %q, want storage-1", got)
	}

	if got := extractServiceName(start.ServiceConfig{
		Command: "inv-discovery",
		Args:    map[string]string{},
	}, counts); got != "inv-discovery" {
		t.Fatalf("got %q, want inv-discovery", got)
	}

	if got := extractServiceName(start.ServiceConfig{
		Command: "inv-discovery",
		Args:    map[string]string{},
	}, counts); got != "inv-discovery-2" {
		t.Fatalf("got %q, want inv-discovery-2", got)
	}
}

func TestGenerateSystemdFiles(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "services.yaml")
	outDir := filepath.Join(dir, "units")

	yamlContent := `services:
  - command: inv-discovery
    args:
      port: "3003"
      listen-ip: "true"
  - command: inv-storage
    args:
      dir: "*/storage-1"
      discovery: "http://localhost:3003"
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("write services.yaml failed: %v", err)
	}

	generateFiles(cfgPath, "", "invariant", "invariant", outDir)

	discUnit, err := os.ReadFile(filepath.Join(outDir, "invariant-inv-discovery.service"))
	if err != nil {
		t.Fatalf("expected discovery unit file: %v", err)
	}
	if !strings.Contains(string(discUnit), "User=invariant") || !strings.Contains(string(discUnit), "inv-discovery") {
		t.Fatalf("unexpected discovery unit contents:\n%s", string(discUnit))
	}

	storageUnit, err := os.ReadFile(filepath.Join(outDir, "invariant-storage-1.service"))
	if err != nil {
		t.Fatalf("expected storage unit file: %v", err)
	}
	if !strings.Contains(string(storageUnit), "inv-storage") {
		t.Fatalf("unexpected storage unit contents:\n%s", string(storageUnit))
	}
}
