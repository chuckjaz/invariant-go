package repository

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"invariant/internal/content"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// ShowOptions specifies parameters for inspecting a commit or file snapshot.
type ShowOptions struct {
	WorkspaceDir string
	Target       string // "<commit>" or "<commit>:<path>"
}

// ShowResult holds the formatted output or raw file content of a show operation.
type ShowResult struct {
	IsFileContent bool
	FileContent   []byte
	FormattedText string
}

// GetShow retrieves and formats a commit snapshot or reads a specific file directly from CAS.
func GetShow(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, opts ShowOptions) (*ShowResult, error) {
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		_, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
		if err != nil {
			return nil, err
		}
		if meta.SlotID != "" {
			slotAddr, err := slotsClient.Get(ctx, meta.SlotID)
			if err == nil && slotAddr != "" {
				target = slotAddr
			}
		}
		if target == "" {
			target = meta.CommitHash
		}
	}

	commitHash := target
	filePath := ""
	if before, after, ok := strings.Cut(target, ":"); ok {
		commitHash = before
		filePath = after
	}

	c, err := commitSvc.GetCommit(ctx, commitHash)
	if err != nil {
		return nil, fmt.Errorf("could not find commit %q: %w", commitHash, err)
	}

	// If filePath is specified, read the file content from the commit tree
	if filePath != "" {
		cleanPath := filepathClean(filePath)
		entries, err := commit.FlattenFileTree(ctx, c.Tree.Address, store, slotsClient)
		if err != nil {
			return nil, fmt.Errorf("failed to traverse commit tree: %w", err)
		}

		entry, ok := entries[cleanPath]
		if !ok || entry.Address == "" {
			return nil, fmt.Errorf("file %q does not exist in commit %s", filePath, commitHash)
		}

		contentBytes, err := readTreeObjectContent(ctx, store, entry.Address)
		if err != nil {
			return nil, fmt.Errorf("failed to read file content: %w", err)
		}

		return &ShowResult{
			IsFileContent: true,
			FileContent:   contentBytes,
		}, nil
	}

	// Format commit metadata and diff against its first parent
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("commit %s\n", commitHash))
	if len(c.Parents) > 1 {
		var shortParents []string
		for _, p := range c.Parents {
			if len(p) > 8 {
				shortParents = append(shortParents, p[:8])
			} else {
				shortParents = append(shortParents, p)
			}
		}
		sb.WriteString(fmt.Sprintf("Merge: %s\n", strings.Join(shortParents, " ")))
	}

	authorStr := c.Author.Name
	if c.Author.Email != "" {
		authorStr += fmt.Sprintf(" <%s>", c.Author.Email)
	}
	sb.WriteString(fmt.Sprintf("Author: %s\n", authorStr))

	t := time.Unix(c.Timestamp, 0).UTC()
	sb.WriteString(fmt.Sprintf("Date:   %s\n", t.Format(time.RFC1123Z)))

	if len(c.Tags) > 0 {
		var tagPairs []string
		for k, v := range c.Tags {
			tagPairs = append(tagPairs, fmt.Sprintf("%s=%s", k, v))
		}
		sb.WriteString(fmt.Sprintf("Tags:   %s\n", strings.Join(tagPairs, ", ")))
	}

	if len(c.Refs) > 0 {
		var refPairs []string
		for k, v := range c.Refs {
			refPairs = append(refPairs, fmt.Sprintf("%s:%s", k, v[:min(8, len(v))]))
		}
		sb.WriteString(fmt.Sprintf("Refs:   %s\n", strings.Join(refPairs, ", ")))
	}

	sb.WriteString("\n")
	for line := range strings.SplitSeq(strings.TrimSpace(c.Message), "\n") {
		sb.WriteString(fmt.Sprintf("    %s\n", line))
	}
	sb.WriteString("\n")

	// Diff against parent (or empty tree if root commit)
	var parentTree content.ContentLink
	if len(c.Parents) > 0 {
		parentCommit, err := commitSvc.GetCommit(ctx, c.Parents[0])
		if err == nil && parentCommit != nil {
			parentTree = parentCommit.Tree
		}
	}

	diffText, _, err := commitSvc.ComputeDiff(ctx, parentTree, c.Tree)
	if err == nil && diffText != "" {
		sb.WriteString(diffText)
	}

	return &ShowResult{
		IsFileContent: false,
		FormattedText: sb.String(),
	}, nil
}

func filepathClean(p string) string {
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "./")
	return p
}

func readTreeObjectContent(ctx context.Context, store storage.Storage, addr string) ([]byte, error) {
	link := content.ContentLink{Address: addr}
	r, err := content.Read(link, store, nil)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
