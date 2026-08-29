package repository

import (
	"context"
	"testing"

	"invariant/internal/names"
	"invariant/internal/slots"
)

func TestChangeBranchNaming(t *testing.T) {
	formatted := FormatChangeBranchName("alice", "myrepo", "feat-auth")
	if formatted != ":alice:myrepo:feat-auth" {
		t.Fatalf("FormatChangeBranchName mismatch: got %q, want :alice:myrepo:feat-auth", formatted)
	}

	user, repo, change, err := ParseChangeBranchName(formatted)
	if err != nil {
		t.Fatalf("ParseChangeBranchName failed: %v", err)
	}
	if user != "alice" || repo != "myrepo" || change != "feat-auth" {
		t.Errorf("Parsed components mismatch: user=%q, repo=%q, change=%q", user, repo, change)
	}

	// Test invalid format
	if _, _, _, err := ParseChangeBranchName("invalid-branch"); err == nil {
		t.Errorf("Expected error for invalid branch string, got nil")
	}
}

func TestTagNameNaming(t *testing.T) {
	formatted := FormatTagName("myrepo", "v1.0.0")
	if formatted != "myrepo:tags:v1.0.0" {
		t.Fatalf("FormatTagName mismatch: got %q, want myrepo:tags:v1.0.0", formatted)
	}

	repo, tag, err := ParseTagName(formatted)
	if err != nil {
		t.Fatalf("ParseTagName failed: %v", err)
	}
	if repo != "myrepo" || tag != "v1.0.0" {
		t.Errorf("Parsed tag components mismatch: repo=%q, tag=%q", repo, tag)
	}

	// Test invalid format
	if _, _, err := ParseTagName("invalid:tag"); err == nil {
		t.Errorf("Expected error for invalid tag string, got nil")
	}
}

func TestSlotAndNamesLifecycle(t *testing.T) {
	ctx := context.Background()
	slotsClient := slots.NewMemorySlots("test-slots")
	namesClient := names.NewInMemoryNames()

	// 1. Allocate slot for main branch
	initialCommit := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	slotID, err := AllocateSlot(ctx, slotsClient, initialCommit, "")
	if err != nil {
		t.Fatalf("AllocateSlot failed: %v", err)
	}
	if slotID == "" {
		t.Fatalf("Expected non-empty slot ID")
	}

	// 2. Register repository name in Names service
	repoName := "testrepo"
	if err := RegisterRepositoryName(ctx, namesClient, repoName, slotID); err != nil {
		t.Fatalf("RegisterRepositoryName failed: %v", err)
	}

	registeredEntry, err := namesClient.Get(ctx, repoName)
	if err != nil {
		t.Fatalf("namesClient.Get(%s) failed: %v", repoName, err)
	}
	if registeredEntry.Value != slotID {
		t.Errorf("Registered slot mismatch: got %s, want %s", registeredEntry.Value, slotID)
	}

	// 3. Update slot via CAS
	nextCommit := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	if err := UpdateSlotCAS(ctx, slotsClient, slotID, nextCommit, initialCommit, nil); err != nil {
		t.Fatalf("UpdateSlotCAS failed: %v", err)
	}

	// CAS update with wrong expected address should fail
	wrongExpected := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := UpdateSlotCAS(ctx, slotsClient, slotID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", wrongExpected, nil); err == nil {
		t.Errorf("Expected CAS failure on mismatched expected address, got nil")
	}

	// 4. Register & Unregister change branch
	changeSlotID, err := AllocateSlot(ctx, slotsClient, nextCommit, "")
	if err != nil {
		t.Fatalf("AllocateSlot for change branch failed: %v", err)
	}
	if err := RegisterChangeBranch(ctx, namesClient, "alice", repoName, "feat-x", changeSlotID); err != nil {
		t.Fatalf("RegisterChangeBranch failed: %v", err)
	}
	branchKey := FormatChangeBranchName("alice", repoName, "feat-x")
	branchEntry, err := namesClient.Get(ctx, branchKey)
	if err != nil || branchEntry.Value != changeSlotID {
		t.Fatalf("namesClient.Get(%s) failed: got %s, want %s", branchKey, branchEntry.Value, changeSlotID)
	}

	if err := UnregisterChangeBranch(ctx, namesClient, "alice", repoName, "feat-x", changeSlotID); err != nil {
		t.Fatalf("UnregisterChangeBranch failed: %v", err)
	}
	if _, err := namesClient.Get(ctx, branchKey); err == nil {
		t.Errorf("Expected branch %s to be unregistered, but still found in names service", branchKey)
	}

	// 5. Register & Unregister release tag
	tagCommit := nextCommit
	if err := RegisterReleaseTag(ctx, namesClient, repoName, "v1.0.0", tagCommit); err != nil {
		t.Fatalf("RegisterReleaseTag failed: %v", err)
	}
	tagKey := FormatTagName(repoName, "v1.0.0")
	tagEntry, err := namesClient.Get(ctx, tagKey)
	if err != nil || tagEntry.Value != tagCommit {
		t.Fatalf("namesClient.Get(%s) failed: got %s, want %s", tagKey, tagEntry.Value, tagCommit)
	}

	if err := UnregisterReleaseTag(ctx, namesClient, repoName, "v1.0.0", tagCommit); err != nil {
		t.Fatalf("UnregisterReleaseTag failed: %v", err)
	}
	if _, err := namesClient.Get(ctx, tagKey); err == nil {
		t.Errorf("Expected tag %s to be unregistered, but still found in names service", tagKey)
	}
}
