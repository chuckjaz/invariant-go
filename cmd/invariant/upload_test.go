package main

import (
	"invariant/internal/filetree"
	"os"
	"path/filepath"
	"testing"
)

func TestIgnoreRulesMatches(t *testing.T) {
	tests := []struct {
		name     string
		rules    filetree.IgnoreRules
		path     string
		isDir    bool
		expected bool
	}{
		{
			name:     "match exact file",
			rules:    filetree.IgnoreRules{"test.txt"},
			path:     "test.txt",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match exact file in subdir",
			rules:    filetree.IgnoreRules{"test.txt"},
			path:     "subdir/test.txt",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match glob prefix",
			rules:    filetree.IgnoreRules{"*.log"},
			path:     "error.log",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match glob prefix in subdir",
			rules:    filetree.IgnoreRules{"*.log"},
			path:     "var/error.log",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match directory trailing slash",
			rules:    filetree.IgnoreRules{"node_modules/"},
			path:     "node_modules",
			isDir:    true,
			expected: true,
		},
		{
			name:     "match directory trailing slash but path is file",
			rules:    filetree.IgnoreRules{"node_modules/"},
			path:     "node_modules",
			isDir:    false,
			expected: false,
		},
		{
			name:     "implicit .git match with gitignore",
			rules:    filetree.IgnoreRules{"*.txt"},
			path:     ".git",
			isDir:    true,
			expected: true,
		},
		{
			name:     "no implicit .git match without gitignore",
			rules:    nil, // No .gitignore file
			path:     ".git",
			isDir:    true,
			expected: false,
		},
		{
			name:     "implicit .invariant match",
			rules:    filetree.IgnoreRules{},
			path:     ".invariant",
			isDir:    true,
			expected: true,
		},
		{
			name:     "non-matching file",
			rules:    filetree.IgnoreRules{"*.log"},
			path:     "hello.txt",
			isDir:    false,
			expected: false,
		},
		{
			name:     "match relative path",
			rules:    filetree.IgnoreRules{"build/*.o"},
			path:     "build/main.o",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match deep relative path",
			rules:    filetree.IgnoreRules{"build/cache/*.o"},
			path:     "build/cache/main.o",
			isDir:    false,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.rules.Matches(tt.path, tt.isDir)
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

	expected := filetree.IgnoreRules{"*.log", "node_modules/", "build/*.o"}
	if len(rules) != len(expected) {
		t.Fatalf("expected %d rules, got %d", len(expected), len(rules))
	}

	for i, r := range expected {
		if rules[i] != r {
			t.Errorf("expected rule %q at %d, got %q", r, i, rules[i])
		}
	}
}
