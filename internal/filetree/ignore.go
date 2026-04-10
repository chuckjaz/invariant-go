package filetree

import (
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

// IgnoreMatcher instances provide compiled scalable rule lookups
type IgnoreMatcher struct {
	compiler *ignore.GitIgnore
	hasRules bool
}

// CompileIgnore pre-compiles gitignore rules to speed up Matches testing
func CompileIgnore(rules []string) *IgnoreMatcher {
	if len(rules) == 0 {
		return &IgnoreMatcher{hasRules: false}
	}
	return &IgnoreMatcher{
		hasRules: true,
		compiler: ignore.CompileIgnoreLines(rules...),
	}
}

// Matches returns true if the specified relative path or its base matches against the configured gitignore patterns
func (m *IgnoreMatcher) Matches(path string, isDir bool) bool {
	base := filepath.Base(path)
	if base == ".invariant" {
		return true
	}
	if m.hasRules && base == ".git" {
		return true
	}

	if !m.hasRules {
		return false
	}

	checkPath := path
	if isDir && !strings.HasSuffix(checkPath, "/") {
		checkPath = checkPath + "/"
	}
	return m.compiler.MatchesPath(checkPath)
}
