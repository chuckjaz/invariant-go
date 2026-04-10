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
		rules    *filetree.IgnoreMatcher
		path     string
		isDir    bool
		expected bool
	}{
		{
			name:     "match exact file",
			rules:    filetree.CompileIgnore([]string{"test.txt"}),
			path:     "test.txt",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match exact file in subdir",
			rules:    filetree.CompileIgnore([]string{"test.txt"}),
			path:     "subdir/test.txt",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match glob prefix",
			rules:    filetree.CompileIgnore([]string{"*.log"}),
			path:     "error.log",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match glob prefix in subdir",
			rules:    filetree.CompileIgnore([]string{"*.log"}),
			path:     "var/error.log",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match directory trailing slash",
			rules:    filetree.CompileIgnore([]string{"node_modules/"}),
			path:     "node_modules",
			isDir:    true,
			expected: true,
		},
		{
			name:     "match directory trailing slash but path is file",
			rules:    filetree.CompileIgnore([]string{"node_modules/"}),
			path:     "node_modules",
			isDir:    false,
			expected: false,
		},
		{
			name:     "implicit .git match with gitignore",
			rules:    filetree.CompileIgnore([]string{"*.txt"}),
			path:     ".git",
			isDir:    true,
			expected: true,
		},
		{
			name:     "no implicit .git match without gitignore",
			rules:    filetree.CompileIgnore(nil), // No .gitignore file
			path:     ".git",
			isDir:    true,
			expected: false,
		},
		{
			name:     "implicit .invariant match",
			rules:    filetree.CompileIgnore([]string{}),
			path:     ".invariant",
			isDir:    true,
			expected: true,
		},
		{
			name:     "non-matching file",
			rules:    filetree.CompileIgnore([]string{"*.log"}),
			path:     "hello.txt",
			isDir:    false,
			expected: false,
		},
		{
			name:     "match relative path",
			rules:    filetree.CompileIgnore([]string{"build/*.o"}),
			path:     "build/main.o",
			isDir:    false,
			expected: true,
		},
		{
			name:     "match deep relative path",
			rules:    filetree.CompileIgnore([]string{"build/cache/*.o"}),
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

	expected := []string{"*.log", "node_modules/", "build/*.o"}
	expectedMatcher := filetree.CompileIgnore(expected)
	if rules.Matches("error.log", false) != expectedMatcher.Matches("error.log", false) {
		t.Errorf("Matcher behavior divergence")
	}
}
