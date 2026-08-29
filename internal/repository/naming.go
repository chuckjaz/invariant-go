package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"invariant/internal/names"
	"invariant/internal/slots"
)

// FormatChangeBranchName constructs the canonical Names Service key for a change branch.
func FormatChangeBranchName(user, repoName, changeName string) string {
	return fmt.Sprintf(":%s:%s:%s", user, repoName, changeName)
}

// ParseChangeBranchName parses a canonical change branch string into its components.
func ParseChangeBranchName(formatted string) (user, repoName, changeName string, err error) {
	parts := strings.Split(formatted, ":")
	if len(parts) != 4 || parts[0] != "" {
		return "", "", "", fmt.Errorf("invalid change branch format %q (expected :<user>:<repo>:<change>)", formatted)
	}
	return parts[1], parts[2], parts[3], nil
}

// FormatTagName constructs the canonical Names Service key for a release tag.
func FormatTagName(repoName, tagName string) string {
	return fmt.Sprintf("%s:tags:%s", repoName, tagName)
}

// ParseTagName parses a canonical release tag string into repo and tag names.
func ParseTagName(formatted string) (repoName, tagName string, err error) {
	parts := strings.Split(formatted, ":")
	if len(parts) != 3 || parts[1] != "tags" {
		return "", "", fmt.Errorf("invalid tag format %q (expected <repo>:tags:<tag>)", formatted)
	}
	return parts[0], parts[2], nil
}

// FormatReviewTokenName constructs the canonical Names Service key for a review token.
func FormatReviewTokenName(repoName, token string) string {
	return fmt.Sprintf("%s:reviews:%s", repoName, token)
}

// AllocateSlot generates a new standard 32-byte slot ID and initializes it in the Slots service.
func AllocateSlot(ctx context.Context, slotsClient slots.Slots, initialAddress string, policy string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random slot id: %w", err)
	}
	slotID := hex.EncodeToString(b)

	if err := slotsClient.Create(ctx, slotID, initialAddress, policy); err != nil {
		return "", fmt.Errorf("failed to create slot %s in slots service: %w", slotID, err)
	}
	return slotID, nil
}

// UpdateSlotCAS performs a Compare-And-Swap (CAS) update on a slot.
func UpdateSlotCAS(ctx context.Context, slotsClient slots.Slots, slotID, newAddress, expectedAddress string, auth []byte) error {
	if err := slotsClient.Update(ctx, slotID, newAddress, expectedAddress, auth); err != nil {
		return fmt.Errorf("failed to update slot %s from %s to %s: %w", slotID, expectedAddress, newAddress, err)
	}
	return nil
}

// RegisterRepositoryName registers a repository root with the Names Service.
func RegisterRepositoryName(ctx context.Context, namesClient names.Names, repoName, slotID string) error {
	if err := namesClient.Put(ctx, repoName, slotID, nil); err != nil {
		return fmt.Errorf("failed to register repository %s in names service: %w", repoName, err)
	}
	return nil
}

// RegisterChangeBranch registers a change branch with the Names Service.
func RegisterChangeBranch(ctx context.Context, namesClient names.Names, user, repoName, changeName, slotID string) error {
	key := FormatChangeBranchName(user, repoName, changeName)
	if err := namesClient.Put(ctx, key, slotID, nil); err != nil {
		return fmt.Errorf("failed to register change branch %s in names service: %w", key, err)
	}
	return nil
}

// UnregisterChangeBranch removes a change branch from the Names Service.
func UnregisterChangeBranch(ctx context.Context, namesClient names.Names, user, repoName, changeName, expectedSlotID string) error {
	key := FormatChangeBranchName(user, repoName, changeName)
	if err := namesClient.Delete(ctx, key, expectedSlotID); err != nil {
		return fmt.Errorf("failed to delete change branch %s from names service: %w", key, err)
	}
	return nil
}

// RegisterReleaseTag registers a release tag in the Names Service.
func RegisterReleaseTag(ctx context.Context, namesClient names.Names, repoName, tagName, commitHash string) error {
	key := FormatTagName(repoName, tagName)
	if err := namesClient.Put(ctx, key, commitHash, nil); err != nil {
		return fmt.Errorf("failed to register release tag %s in names service: %w", key, err)
	}
	return nil
}

// UnregisterReleaseTag removes a release tag from the Names Service.
func UnregisterReleaseTag(ctx context.Context, namesClient names.Names, repoName, tagName, expectedCommitHash string) error {
	key := FormatTagName(repoName, tagName)
	if err := namesClient.Delete(ctx, key, expectedCommitHash); err != nil {
		return fmt.Errorf("failed to delete release tag %s from names service: %w", key, err)
	}
	return nil
}

// ResolveBranchSlot resolves the slot ID for a branch (e.g. "main" or a change branch).
func ResolveBranchSlot(ctx context.Context, namesClient names.Names, repoName, branchName string) (string, error) {
	if branchName == "main" || branchName == "" {
		entry, err := namesClient.Get(ctx, repoName)
		if err != nil {
			return "", err
		}
		return entry.Value, nil
	}
	if strings.HasPrefix(branchName, ":") {
		entry, err := namesClient.Get(ctx, branchName)
		if err != nil {
			return "", err
		}
		return entry.Value, nil
	}
	entry, err := namesClient.Get(ctx, branchName)
	if err == nil && entry.Value != "" {
		return entry.Value, nil
	}
	return "", fmt.Errorf("could not resolve branch slot for %s", branchName)
}
