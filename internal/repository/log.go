package repository

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// LogOptions specifies parameters for querying commit logs.
type LogOptions struct {
	WorkspaceDir string
	PathFilter   string
	Tree         bool
	MaxCount     int
	StartCommit  string
}

// LogEntry encapsulates a single formatted commit in history.
type LogEntry struct {
	Hash       string
	Commit     *commit.Commit
	IsMerge    bool
	ParentTags string
}

// StreamLog queries and emits the commit history incrementally starting from the workspace HEAD (or specified commit).
func StreamLog(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	commitSvc commit.Service,
	opts LogOptions,
	emit func(entry LogEntry) error,
) error {
	startHash := opts.StartCommit
	if startHash == "" {
		_, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
		if err != nil {
			return err
		}

		if meta.SlotID != "" {
			slotAddr, err := slotsClient.Get(ctx, meta.SlotID)
			if err == nil && slotAddr != "" {
				startHash = slotAddr
			}
		}
		if startHash == "" {
			startHash = meta.CommitHash
		}
	}

	if startHash == "" {
		return fmt.Errorf("no commit history found")
	}

	visited := make(map[string]bool)
	queue := []string{startHash}
	count := 0

	for len(queue) > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		currHash := queue[0]
		queue = queue[1:]

		if currHash == "" || visited[currHash] {
			continue
		}
		visited[currHash] = true

		c, err := commitSvc.GetCommit(ctx, currHash)
		if err != nil || c == nil {
			continue
		}

		include := true
		if opts.PathFilter != "" {
			include = false
			if len(c.Parents) == 0 {
				entries, err := commit.FlattenFileTree(ctx, c.Tree.Address, store, slotsClient)
				if err == nil && entries[opts.PathFilter].Address != "" {
					include = true
				}
			} else {
				parentCommit, err := commitSvc.GetCommit(ctx, c.Parents[0])
				if err == nil && parentCommit != nil {
					e1, _ := commit.FlattenFileTree(ctx, parentCommit.Tree.Address, store, slotsClient)
					e2, _ := commit.FlattenFileTree(ctx, c.Tree.Address, store, slotsClient)
					if e1[opts.PathFilter].Address != e2[opts.PathFilter].Address {
						include = true
					}
				}
			}
		}

		if include {
			entry := LogEntry{
				Hash:    currHash,
				Commit:  c,
				IsMerge: len(c.Parents) > 1,
			}
			if err := emit(entry); err != nil {
				return err
			}
			count++
			if opts.MaxCount > 0 && count >= opts.MaxCount {
				return nil
			}
		}

		if !opts.Tree {
			if len(c.Parents) > 0 {
				queue = append(queue, c.Parents[0])
			}
		} else {
			queue = append(queue, c.Parents...)
		}
	}

	return nil
}

// GetLog queries the commit history starting from the workspace HEAD (or specified commit).
func GetLog(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, opts LogOptions) ([]LogEntry, error) {
	var entries []LogEntry
	err := StreamLog(ctx, store, slotsClient, commitSvc, opts, func(entry LogEntry) error {
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// FormatLogEntry formats a single LogEntry into human-readable text.
func FormatLogEntry(entry LogEntry, tree bool) string {
	var sb strings.Builder
	c := entry.Commit
	shortHash := entry.Hash
	if len(shortHash) > 12 {
		shortHash = shortHash[:12]
	}

	graphPrefix := ""
	if tree {
		if entry.IsMerge {
			graphPrefix = "*   "
		} else {
			graphPrefix = "* | "
		}
	}

	sb.WriteString(fmt.Sprintf("%scommit %s\n", graphPrefix, entry.Hash))
	if len(c.Parents) > 1 {
		var shortParents []string
		for _, p := range c.Parents {
			if len(p) > 8 {
				shortParents = append(shortParents, p[:8])
			} else {
				shortParents = append(shortParents, p)
			}
		}
		sb.WriteString(fmt.Sprintf("%sMerge: %s\n", graphPrefix, strings.Join(shortParents, " ")))
	}

	authorStr := c.Author.Name
	if c.Author.Email != "" {
		authorStr += fmt.Sprintf(" <%s>", c.Author.Email)
	}
	sb.WriteString(fmt.Sprintf("%sAuthor: %s\n", graphPrefix, authorStr))

	t := time.Unix(c.Timestamp, 0).UTC()
	sb.WriteString(fmt.Sprintf("%sDate:   %s\n", graphPrefix, t.Format(time.RFC1123Z)))

	if len(c.Tags) > 0 {
		var tagPairs []string
		for k, v := range c.Tags {
			tagPairs = append(tagPairs, fmt.Sprintf("%s=%s", k, v))
		}
		sb.WriteString(fmt.Sprintf("%sTags:   %s\n", graphPrefix, strings.Join(tagPairs, ", ")))
	}

	if len(c.Refs) > 0 {
		var refPairs []string
		for k, v := range c.Refs {
			refPairs = append(refPairs, fmt.Sprintf("%s:%s", k, v[:min(8, len(v))]))
		}
		sb.WriteString(fmt.Sprintf("%sRefs:   %s\n", graphPrefix, strings.Join(refPairs, ", ")))
	}

	sb.WriteString(fmt.Sprintf("%s\n", graphPrefix))
	lines := strings.SplitSeq(strings.TrimSpace(c.Message), "\n")
	for line := range lines {
		sb.WriteString(fmt.Sprintf("%s    %s\n", graphPrefix, line))
	}
	sb.WriteString(fmt.Sprintf("%s\n", graphPrefix))

	return sb.String()
}

// FormatLog formats a slice of LogEntries into human-readable text.
func FormatLog(entries []LogEntry, tree bool) string {
	if len(entries) == 0 {
		return "No commit history found.\n"
	}

	var sb strings.Builder
	for _, entry := range entries {
		sb.WriteString(FormatLogEntry(entry, tree))
	}

	return sb.String()
}

// FindWorkspaceRelativePath calculates the repository-relative path for a target file.
func FindWorkspaceRelativePath(workspaceDir, targetPath string) (string, error) {
	wsRoot, _, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return "", err
	}

	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(wsRoot, absTarget)
	if err != nil {
		return "", err
	}

	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %s is outside repository workspace %s", targetPath, wsRoot)
	}

	return filepath.Clean(rel), nil
}
