package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/repository/review"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestReviewWorkflow_FullLifecycleAndSubmitGating(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("slots-id")
	namesClient := names.NewInMemoryNames()
	idProvider := &workflowMockIDProvider{name: "Alice"}
	SetDefaultIdentityProvider(idProvider)

	commitSvc := commit.NewLocalService(store, slotsClient, namesClient, idProvider)
	reviewSvc := review.NewLocalService(store, slotsClient, namesClient)

	tempDir := t.TempDir()
	repoName := "review-repo"
	repoRoot := filepath.Join(tempDir, repoName)

	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	// 1. Create repository
	_, rootCommitHash, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      repoName,
		TargetDir: repoRoot,
		Writable:  false,
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}

	// Enable ReviewRequired in RepoConfig
	cfg, slotID, prevAddr, err := loadRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName)
	if err != nil {
		t.Fatalf("loadRepoConfigForTag failed: %v", err)
	}
	cfg.ReviewRequired = true
	if err := saveRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName, cfg, slotID, prevAddr); err != nil {
		t.Fatalf("saveRepoConfigForTag failed: %v", err)
	}

	// 2. Create Change branch
	changeMeta, err := CreateChangeBranch(ctx, store, slotsClient, namesClient, commitSvc, ChangeOptions{
		RepoRoot:   repoRoot,
		ChangeName: "feat-login",
		AuthorName: "Alice",
	})
	if err != nil {
		t.Fatalf("CreateChangeBranch failed: %v", err)
	}
	changeDir := filepath.Join(repoRoot, "feat-login")

	// Commit something on the change branch
	headCommit, err := commitSvc.GetCommit(ctx, rootCommitHash)
	if err != nil {
		t.Fatalf("GetCommit failed: %v", err)
	}
	newCommit, newHash, err := commitSvc.CreateCommit(ctx, commit.CreateRequest{
		RepoName:   repoName,
		BranchName: changeMeta.BranchName,
		TreeLink:   headCommit.Tree,
		Parents:    []string{rootCommitHash},
		Message:    "Add login feature",
		Author:     Identity{Name: "Alice"},
	})
	if err != nil {
		t.Fatalf("CreateCommit failed: %v", err)
	}
	_ = slotsClient.Update(ctx, changeMeta.SlotID, newHash, rootCommitHash, nil)
	changeMeta.CommitHash = newHash
	_ = WriteWorkspaceMetadata(changeDir, changeMeta)

	// 3. Request review
	rec, reviewURL, err := RequestReview(ctx, store, slotsClient, namesClient, commitSvc, reviewSvc, RequestReviewOptions{
		WorkspaceDir: changeDir,
		AuthorName:   "Alice",
	})
	if err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}
	if rec.Status != review.StatusPending || reviewURL == "" {
		t.Errorf("Unexpected review record or URL: %+v, url=%s", rec, reviewURL)
	}

	// 4. Submit Gating: submitting while review is pending MUST FAIL
	_, err = ExecuteSubmit(ctx, store, slotsClient, namesClient, commitSvc, SubmitOptions{
		WorkspaceDir:  changeDir,
		TargetBranch:  "main",
		AuthorName:    "Alice",
		ReviewService: reviewSvc,
	})
	if err == nil {
		t.Fatalf("Expected submit to fail due to pending review, but it succeeded")
	}

	// 5. Open review for inspection without mutating state
	reviewWsDir := filepath.Join(tempDir, "review-ws")
	openedWs, openedRec, err := OpenReview(ctx, store, slotsClient, namesClient, commitSvc, reviewSvc, OpenReviewOptions{
		Identifier:   rec.Token,
		TargetDir:    reviewWsDir,
		WorkspaceDir: changeDir,
	})
	if err != nil {
		t.Fatalf("OpenReview failed: %v", err)
	}
	if openedRec.Status != review.StatusPending {
		t.Errorf("Expected review status to remain StatusPending after open, got %s", openedRec.Status)
	}
	if openedWs != reviewWsDir {
		t.Errorf("Expected workspace dir %s, got %s", reviewWsDir, openedWs)
	}

	// 6. Start review (transitions state to in_progress)
	startWsDir := filepath.Join(tempDir, "start-ws")
	_, startedRec, err := StartReview(ctx, store, slotsClient, namesClient, commitSvc, reviewSvc, StartReviewOptions{
		Identifier:   rec.Token,
		TargetDir:    startWsDir,
		ReviewerName: "Bob",
	})
	if err != nil {
		t.Fatalf("StartReview failed: %v", err)
	}
	if startedRec.Status != review.StatusInProgress || startedRec.Reviewer != "Bob" {
		t.Errorf("Expected StatusInProgress with reviewer Bob, got: %+v", startedRec)
	}

	// 7. Add Comments
	line := 10
	if err := AddReviewComment(ctx, store, slotsClient, namesClient, commitSvc, reviewSvc, AddCommentOptions{
		Identifier:  rec.Token,
		AuthorName:  "Bob",
		CommentText: "Ensure token expiration is verified",
		File:        "auth.go",
		StartLine:   &line,
	}); err != nil {
		t.Fatalf("AddReviewComment failed: %v", err)
	}

	// Also add comment from file
	commentJSONFile := filepath.Join(tempDir, "comment.json")
	_ = os.WriteFile(commentJSONFile, []byte(`[{"file":"auth.go","startLine":12,"comments":[{"comment":"Use secure hash","author":"Bob"}]}]`), 0644)
	if err := AddReviewComment(ctx, store, slotsClient, namesClient, commitSvc, reviewSvc, AddCommentOptions{
		Identifier:  rec.Token,
		CommentFile: commentJSONFile,
	}); err != nil {
		t.Fatalf("AddReviewComment from file failed: %v", err)
	}

	commentsMd, err := GetReviewComments(ctx, store, slotsClient, namesClient, commitSvc, reviewSvc, GetCommentsOptions{
		Identifier: rec.Token,
	})
	if err != nil {
		t.Fatalf("GetReviewComments failed: %v", err)
	}
	if len(commentsMd) == 0 {
		t.Errorf("Expected non-empty formatted comments")
	}

	// 8. Submit Gating: submitting while review is in_progress MUST FAIL
	_, err = ExecuteSubmit(ctx, store, slotsClient, namesClient, commitSvc, SubmitOptions{
		WorkspaceDir:  changeDir,
		TargetBranch:  "main",
		AuthorName:    "Alice",
		ReviewService: reviewSvc,
	})
	if err == nil {
		t.Fatalf("Expected submit to fail due to in_progress review, but it succeeded")
	}

	// 9. Approve review
	approvedRec, err := ApproveReview(ctx, store, slotsClient, namesClient, commitSvc, reviewSvc, ReviewActionOptions{
		Identifier:   rec.Token,
		ReviewerName: "Bob",
	})
	if err != nil {
		t.Fatalf("ApproveReview failed: %v", err)
	}
	if approvedRec.Status != review.StatusApproved {
		t.Errorf("Expected StatusApproved, got %s", approvedRec.Status)
	}

	// 10. Submit Gating: submitting with approved review MUST SUCCEED
	submitResp, err := ExecuteSubmit(ctx, store, slotsClient, namesClient, commitSvc, SubmitOptions{
		WorkspaceDir:  changeDir,
		TargetBranch:  "main",
		AuthorName:    "Alice",
		ReviewService: reviewSvc,
	})
	if err != nil {
		t.Fatalf("ExecuteSubmit failed after review approval: %v", err)
	}
	if submitResp.NewHeadCommit == "" && newCommit.Message == "" {
		t.Errorf("Unexpected submit response: %+v", submitResp)
	}

	// 11. Verify closed review can be opened for viewing
	closedWsDir := filepath.Join(tempDir, "closed-ws")
	_, closedRec, err := OpenReview(ctx, store, slotsClient, namesClient, commitSvc, reviewSvc, OpenReviewOptions{
		Identifier: rec.Token,
		TargetDir:  closedWsDir,
	})
	if err != nil {
		t.Fatalf("OpenReview on closed review failed: %v", err)
	}
	if closedRec.Status != review.StatusApproved {
		t.Errorf("Expected StatusApproved for closed review, got %s", closedRec.Status)
	}
}

func TestReviewWorkflow_RejectAndAbandon(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("slots-id")
	namesClient := names.NewInMemoryNames()
	idProvider := &workflowMockIDProvider{name: "Alice"}
	commitSvc := commit.NewLocalService(store, slotsClient, namesClient, idProvider)
	reviewSvc := review.NewLocalService(store, slotsClient, namesClient)

	// Test reject
	rec1, err := reviewSvc.RequestReview(ctx, "repo1", "branch1", Identity{Name: "Alice"})
	if err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}
	rejectedRec, err := RejectReview(ctx, store, slotsClient, namesClient, commitSvc, reviewSvc, ReviewActionOptions{
		Identifier:   rec1.Token,
		ReviewerName: "Bob",
	})
	if err != nil || rejectedRec.Status != review.StatusRejected {
		t.Fatalf("RejectReview failed: got %+v, err %v", rejectedRec, err)
	}

	// Test abandon
	rec2, err := reviewSvc.RequestReview(ctx, "repo2", "branch2", Identity{Name: "Alice"})
	if err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}
	abandonedRec, err := AbandonReview(ctx, store, slotsClient, namesClient, commitSvc, reviewSvc, ReviewActionOptions{
		Identifier:   rec2.Token,
		ReviewerName: "Alice",
	})
	if err != nil || abandonedRec.Status != review.StatusAbandoned {
		t.Fatalf("AbandonReview failed: got %+v, err %v", abandonedRec, err)
	}
}
