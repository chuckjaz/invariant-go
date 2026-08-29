package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// BlameOptions specifies parameters for computing line attribution.
type BlameOptions struct {
	WorkspaceDir string
	FilePath     string
	CommitHash   string
}

// GetBlame computes line attribution for target file at the specified commit (or HEAD).
func GetBlame(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, opts BlameOptions) ([]commit.BlameLine, error) {
	wsRoot, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
	if err != nil {
		return nil, err
	}

	commitHash := opts.CommitHash
	if commitHash == "" {
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

	relPath, err := FindWorkspaceRelativePath(wsRoot, opts.FilePath)
	if err != nil {
		relPath = filepathClean(opts.FilePath)
	}

	lines, err := commitSvc.Blame(ctx, commitHash, relPath)
	if err != nil {
		return nil, fmt.Errorf("blame failed for %s at %s: %w", relPath, commitHash, err)
	}

	return lines, nil
}

// FormatBlame formats BlameLine structs into aligned column output.
func FormatBlame(lines []commit.BlameLine) string {
	if len(lines) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, l := range lines {
		shortHash := l.CommitHash
		if len(shortHash) > 8 {
			shortHash = shortHash[:8]
		}
		author := l.Author.Name
		if author == "" {
			author = "unknown"
		}
		if len(author) > 16 {
			author = author[:16]
		}

		t := time.Unix(l.Timestamp, 0).UTC().Format("2006-01-02 15:04:05")
		sb.WriteString(fmt.Sprintf("%s (%-16s %s %4d) %s\n", shortHash, author, t, l.LineNumber, l.Content))
	}

	return sb.String()
}
