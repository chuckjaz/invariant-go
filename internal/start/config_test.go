package start

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "services.yaml")

	yamlContent := `
common:
  base:
    port: "8080"
    log: "debug"
  extra:
    trace: "true"
    port: "9090"
services:
  - command: test-service
    use: base
    args:
      env: "prod"
      log: "info"
  - command: test-multi
    use: [base, extra]
    args:
      env: "staging"
`
	err := os.WriteFile(configPath, []byte(yamlContent), 0644)
	if err != nil {
		t.Fatalf("failed to write temp config file: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if len(cfg.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(cfg.Services))
	}

	svc := cfg.Services[0]
	if svc.Command != "test-service" {
		t.Errorf("expected command 'test-service', got '%s'", svc.Command)
	}

	if svc.Args["port"] != "8080" {
		t.Errorf("expected port '8080' from common, got '%s'", svc.Args["port"])
	}

	if svc.Args["env"] != "prod" {
		t.Errorf("expected env 'prod', got '%s'", svc.Args["env"])
	}

	if svc.Args["log"] != "info" {
		t.Errorf("expected log 'info' (overridden), got '%s'", svc.Args["log"])
	}

	multiSvc := cfg.Services[1]
	if multiSvc.Command != "test-multi" {
		t.Errorf("expected command 'test-multi', got '%s'", multiSvc.Command)
	}

	if multiSvc.Args["trace"] != "true" {
		t.Errorf("expected trace 'true' from extra common, got '%s'", multiSvc.Args["trace"])
	}

	if multiSvc.Args["port"] != "8080" {
		t.Errorf("expected port '8080' from first common taking precedence, got '%s'", multiSvc.Args["port"])
	}
}

func TestSubstituteString(t *testing.T) {
	os.Setenv("TESTVAR", "hello")
	os.Setenv("EMPTYVAR", "")
	defer os.Unsetenv("TESTVAR")
	defer os.Unsetenv("EMPTYVAR")

	homeDir, _ := os.UserHomeDir()

	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no substitution",
			input:    "plain string",
			expected: "plain string",
		},
		{
			name:     "env var substitution",
			input:    "$TESTVAR/world",
			expected: "hello/world",
		},
		{
			name:     "empty env var",
			input:    "foo$EMPTYVAR/bar",
			expected: "foo/bar",
		},
		{
			name:     "undefined env var",
			input:    "foo$UNDEFINED_VAR/bar",
			expected: "foo/bar",
		},
		{
			name:     "tilde substitution",
			input:    "~/workspace",
			expected: homeDir + "/workspace",
		},
		{
			name:     "escaped dollar",
			input:    "\\$TESTVAR",
			expected: "$TESTVAR",
		},
		{
			name:     "escaped tilde",
			input:    "\\~/workspace",
			expected: "~/workspace",
		},
		{
			name:     "escaped backslash",
			input:    "\\\\$TESTVAR",
			expected: "\\hello",
		},
		{
			name:     "star substitution",
			input:    "*/workspace",
			expected: "/mock/base/dir/workspace",
		},
		{
			name:     "escaped star",
			input:    "\\*/workspace",
			expected: "*/workspace",
		},
		{
			name:     "multiple substitutions",
			input:    "~/$TESTVAR/\\$TESTVAR/\\~/\\*/*",
			expected: homeDir + "/hello/$TESTVAR/~/*//mock/base/dir",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actual := SubstituteString(tc.input, "/mock/base/dir")
			if actual != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, actual)
			}
		})
	}
}
