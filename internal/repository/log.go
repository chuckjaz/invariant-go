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

// GetLog queries the commit history starting from the workspace HEAD (or specified commit).
func GetLog(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, opts LogOptions) ([]LogEntry, error) {
	startHash := opts.StartCommit
	if startHash == "" {
		_, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
		if err != nil {
			return nil, err
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
		return nil, fmt.Errorf("no commit history found")
	}

	spineOnly := !opts.Tree
	commits, hashes, err := commitSvc.GetHistory(ctx, startHash, spineOnly, opts.PathFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit history: %w", err)
	}

	var entries []LogEntry
	for i, c := range commits {
		if opts.MaxCount > 0 && len(entries) >= opts.MaxCount {
			break
		}
		entries = append(entries, LogEntry{
			Hash:    hashes[i],
			Commit:  c,
			IsMerge: len(c.Parents) > 1,
		})
	}

	return entries, nil
}

// FormatLog formats a slice of LogEntries into human-readable text.
func FormatLog(entries []LogEntry, tree bool) string {
	if len(entries) == 0 {
		return "No commit history found.\n"
	}

	var sb strings.Builder
	for i, entry := range entries {
		c := entry.Commit
		shortHash := entry.Hash
		if len(shortHash) > 12 {
			shortHash = shortHash[:12]
		}

		graphPrefix := ""
		if tree {
			if entry.IsMerge {
				graphPrefix = "*   "
			} else if i < len(entries)-1 {
				graphPrefix = "* | "
			} else {
				graphPrefix = "*   "
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
