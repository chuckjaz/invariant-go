package filetree

import (
	"path/filepath"
	"strings"
)

// IgnoreRules slice implements a matching engine for .gitignore style specs
type IgnoreRules []string

// Matches returns true if the specified relative path or its base matches against the configured gitignore patterns
func (ir IgnoreRules) Matches(path string, isDir bool) bool {
	base := filepath.Base(path)
	if base == ".invariant" {
		return true
	}
	if ir != nil && base == ".git" {
		return true
	}

	parts := strings.Split(path, "/")

	for _, rule := range ir {
		// Basic glob match against the base name
		if matched, _ := filepath.Match(rule, base); matched {
			return true
		}
		// Match directory trailing slashes
		if strings.HasSuffix(rule, "/") {
			cleanRule := strings.TrimSuffix(rule, "/")
			matched, _ := filepath.Match(cleanRule, base)
			if isDir && matched {
				return true
			}

			// Also match if any directory part in the path matches this directory rule
			for i, part := range parts {
				if i == len(parts)-1 && !isDir {
					continue // Skip last part if it's not a directory
				}
				if matched, _ := filepath.Match(cleanRule, part); matched {
					return true
				}
			}
		}
		// Also match exact paths relative to root if it contains a slash (simplification)
		if matched, _ := filepath.Match(rule, path); matched {
			return true
		}
	}
	return false
}
