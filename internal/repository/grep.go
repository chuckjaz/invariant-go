package repository

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// GrepOptions specifies parameters for pattern searching in CAS commit trees.
type GrepOptions struct {
	WorkspaceDir string
	Pattern      string
	CommitHash   string
	IgnoreCase   bool
	LineNumbers  bool
	PathFilter   string
}

// GrepMatch represents a single matching line in a file.
type GrepMatch struct {
	FilePath    string
	LineNumber  int
	LineContent string
}

// GrepTree searches for pattern matches across the files in a commit tree directly in CAS.
func GrepTree(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, opts GrepOptions) ([]GrepMatch, error) {
	if strings.TrimSpace(opts.Pattern) == "" {
		return nil, fmt.Errorf("grep requires a search pattern")
	}

	commitHash := opts.CommitHash
	if commitHash == "" {
		_, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
		if err != nil {
			return nil, err
		}
		if meta.SlotID != "" {
			slotAddr, err := slotsClient.Get(ctx, meta.SlotID)
			if err == nil && slotAddr != "" {
				commitHash = slotAddr
			}
		}
		if commitHash == "" {
			commitHash = meta.CommitHash
		}
	}

	c, err := commitSvc.GetCommit(ctx, commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve commit %s: %w", commitHash, err)
	}

	treeEntries, err := commit.FlattenFileTree(ctx, c.Tree.Address, store, slotsClient)
	if err != nil {
		return nil, fmt.Errorf("failed to traverse commit tree: %w", err)
	}

	pattern := opts.Pattern
	if opts.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regular expression %q: %w", opts.Pattern, err)
	}

	var matches []GrepMatch
	for relPath, entry := range treeEntries {
		if entry.Address == "" {
			continue
		}
		if opts.PathFilter != "" && !strings.HasPrefix(relPath, opts.PathFilter) {
			continue
		}

		data, err := readTreeObjectContent(ctx, store, entry.Address)
		if err != nil {
			continue
		}

		// Check if binary data (contains null bytes)
		if bytes.IndexByte(data, 0) != -1 {
			continue
		}

		scanner := bufio.NewScanner(bytes.NewReader(data))
		lineNum := 1
		for scanner.Scan() {
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, GrepMatch{
					FilePath:    relPath,
					LineNumber:  lineNum,
					LineContent: line,
				})
			}
			lineNum++
		}
	}

	return matches, nil
}

// FormatGrepMatches formats GrepMatch entries into standard grep output.
func FormatGrepMatches(matches []GrepMatch, showLineNumbers bool) string {
	if len(matches) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, m := range matches {
		if showLineNumbers {
			sb.WriteString(fmt.Sprintf("%s:%d:%s\n", m.FilePath, m.LineNumber, m.LineContent))
		} else {
			sb.WriteString(fmt.Sprintf("%s:%s\n", m.FilePath, m.LineContent))
		}
	}
	return sb.String()
}
