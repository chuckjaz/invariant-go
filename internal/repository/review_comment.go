package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/repository/review"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// AddCommentOptions specifies parameters for adding review comments.
type AddCommentOptions struct {
	WorkspaceDir string
	Identifier   string
	CommentFile  string
	AuthorName   string
	CommentText  string
	File         string
	StartLine    *int
	EndLine      *int
}

// GetCommentsOptions specifies parameters for retrieving formatted review comments.
type GetCommentsOptions struct {
	WorkspaceDir string
	Identifier   string
	JSON         bool
}

// AddReviewComment appends comments to an active code review.
func AddReviewComment(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	reviewSvc review.Service,
	opts AddCommentOptions,
) error {
	token := opts.Identifier
	if token == "" {
		cwd := opts.WorkspaceDir
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		_, meta, err := FindWorkspaceRoot(cwd)
		if err == nil {
			token = meta.BranchName
		}
	}

	if token == "" {
		return fmt.Errorf("missing review identifier (token or branch name)")
	}

	author := Identity{Name: opts.AuthorName}
	if author.Name == "" {
		author = CurrentIdentity(ctx)
	}

	var comments []review.ReviewComment

	if opts.CommentFile != "" {
		data, err := os.ReadFile(opts.CommentFile)
		if err != nil {
			return fmt.Errorf("failed to read comment file %s: %w", opts.CommentFile, err)
		}
		// Try unmarshaling as []review.ReviewComment
		if err := json.Unmarshal(data, &comments); err != nil {
			// Try unmarshaling as single review.ReviewComment
			var single review.ReviewComment
			if err2 := json.Unmarshal(data, &single); err2 != nil {
				return fmt.Errorf("invalid comment JSON format: %w", err)
			}
			comments = append(comments, single)
		}
	} else if opts.CommentText != "" {
		comments = append(comments, review.ReviewComment{
			File:      opts.File,
			StartLine: opts.StartLine,
			EndLine:   opts.EndLine,
			Comments: []review.Comment{
				{
					Comment: opts.CommentText,
					Author:  author.Name,
				},
			},
		})
	} else {
		return fmt.Errorf("no comment text or comment file provided")
	}

	return reviewSvc.AddComments(ctx, token, comments, author)
}

// GetReviewComments retrieves and formats comments for a review.
func GetReviewComments(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	reviewSvc review.Service,
	opts GetCommentsOptions,
) (string, error) {
	token := opts.Identifier
	if token == "" {
		cwd := opts.WorkspaceDir
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		_, meta, err := FindWorkspaceRoot(cwd)
		if err == nil {
			token = meta.BranchName
		}
	}

	if token == "" {
		return "", fmt.Errorf("missing review identifier (token or branch name)")
	}

	rec, err := reviewSvc.GetReview(ctx, token)
	if err != nil {
		return "", err
	}

	if opts.JSON {
		data, err := json.MarshalIndent(rec.Comments, "", "  ")
		if err != nil {
			return "", err
		}
		return string(data) + "\n", nil
	}

	if len(rec.Comments) == 0 {
		return "No review comments found.\n", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Review Comments for %s (Status: %s)\n\n", rec.Token, rec.Status))

	for i, thread := range rec.Comments {
		location := thread.File
		if location == "" {
			location = "General"
		}
		if thread.StartLine != nil {
			if thread.EndLine != nil && *thread.EndLine != *thread.StartLine {
				location += fmt.Sprintf(" (Lines %d-%d)", *thread.StartLine, *thread.EndLine)
			} else {
				location += fmt.Sprintf(" (Line %d)", *thread.StartLine)
			}
		}

		sb.WriteString(fmt.Sprintf("### Comment Thread #%d: `%s`\n", i+1, location))
		for _, c := range thread.Comments {
			author := c.Author
			if author == "" {
				author = "Anonymous"
			}
			sb.WriteString(fmt.Sprintf("**%s**: %s\n\n", author, c.Comment))
		}
	}

	return sb.String(), nil
}
