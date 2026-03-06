package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIgnoreRulesMatches(t *testing.T) {
	tests := []struct {
		name     string
		rules    ignoreRules
		path     string
		isDir    bool
		expected bool
	}{
		{
			name:     "match exact file",
			rules:    ignoreRules{"test.txt"},
			path:     "test.txt",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match exact file in subdir",
			rules:    ignoreRules{"test.txt"},
			path:     "subdir/test.txt",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match glob prefix",
			rules:    ignoreRules{"*.log"},
			path:     "error.log",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match glob prefix in subdir",
			rules:    ignoreRules{"*.log"},
			path:     "var/error.log",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match directory trailing slash",
			rules:    ignoreRules{"node_modules/"},
			path:     "node_modules",
			isDir:    true,
			expected: true,
		},
		{
			name:     "match directory trailing slash but path is file",
			rules:    ignoreRules{"node_modules/"},
			path:     "node_modules",
			isDir:    false,
			expected: false,
		},
		{
			name:     "implicit .git match",
			rules:    ignoreRules{"*.txt"},
			path:     ".git",
			isDir:    true,
			expected: true,
		},
		{
			name:     "implicit .invariant match",
			rules:    ignoreRules{},
			path:     ".invariant",
			isDir:    true,
			expected: true,
		},
		{
			name:     "non-matching file",
			rules:    ignoreRules{"*.log"},
			path:     "hello.txt",
			isDir:    false,
			expected: false,
		},
		{
			name:     "match relative path",
			rules:    ignoreRules{"build/*.o"},
			path:     "build/main.o",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match deep relative path",
			rules:    ignoreRules{"build/cache/*.o"},
			path:     "build/cache/main.o",
			isDir:    false,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.rules.matches(tt.path, tt.isDir)
			if result != tt.expected {
				t.Errorf("expected %v, got %v for path %q", tt.expected, result, tt.path)
			}
		})
	}
}

func TestParseIgnoreFile(t *testing.T) {
	content := `
# A comment

*.log
node_modules/
build/*.o

# Another comment
`
	tmpDir, err := os.MkdirTemp("", "invariant-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ignoreFile := filepath.Join(tmpDir, ".gitignore")
	err = os.WriteFile(ignoreFile, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	rules, err := parseIgnoreFile(ignoreFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := ignoreRules{"*.log", "node_modules/", "build/*.o"}
	if len(rules) != len(expected) {
		t.Fatalf("expected %d rules, got %d", len(expected), len(rules))
	}

	for i, r := range expected {
		if rules[i] != r {
			t.Errorf("expected rule %q at %d, got %q", r, i, rules[i])
		}
	}
}
