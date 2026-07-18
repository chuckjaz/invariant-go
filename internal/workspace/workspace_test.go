package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"invariant/internal/content"
	"invariant/internal/files"
	"invariant/internal/filetree"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestWorkspace_BranchAndMerge(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	memSlots := slots.NewMemorySlots("test-slot-id")

	// 1. Create a parent workspace
	dirData, _ := json.Marshal(filetree.Directory{})
	initLink, _ := content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	parentSlotID := "parent-slot"
	err := memSlots.Create(ctx, parentSlotID, initLink.Address, "")
	if err != nil {
		t.Fatalf("failed to create parent slot: %v", err)
	}

	parentRootLink := content.ContentLink{
		Address: parentSlotID,
		Slot:    true,
	}

	parentOpts := files.Options{
		Storage:          store,
		Slots:            memSlots,
		RootLink:         parentRootLink,
		AutoSyncTimeout:  time.Hour,
		SlotPollInterval: time.Hour,
		Layers: []files.Layer{
			{RootLink: parentRootLink},
		},
	}

	fsParent, err := files.NewInMemoryFiles(parentOpts)
	if err != nil {
		t.Fatalf("failed to create parent fs: %v", err)
	}
	defer fsParent.Close()

	// Add file1.txt to parent
	err = fsParent.CreateEntry(ctx, 1, "file1.txt", filetree.FileKind, "", nil, bytes.NewReader([]byte("parent content 1")))
	if err != nil {
		t.Fatalf("failed to create file1.txt: %v", err)
	}

	// Sync parent to write changes to slot
	err = fsParent.Sync(ctx, 1, true)
	if err != nil {
		t.Fatalf("failed to sync parent fs: %v", err)
	}

	// 2. Resolve parent state for branching
	parentSnapshotHash, err := memSlots.Get(ctx, parentSlotID)
	if err != nil {
		t.Fatalf("failed to get parent slot value: %v", err)
	}

	// 3. Create branch workspace slot and fs
	branchSlotID := "branch-slot"
	err = memSlots.Create(ctx, branchSlotID, parentSnapshotHash, "")
	if err != nil {
		t.Fatalf("failed to create branch slot: %v", err)
	}

	branchRootLink := content.ContentLink{
		Address: branchSlotID,
		Slot:    true,
	}

	branchOpts := files.Options{
		Storage:          store,
		Slots:            memSlots,
		RootLink:         branchRootLink,
		AutoSyncTimeout:  time.Hour,
		SlotPollInterval: time.Hour,
		Layers: []files.Layer{
			{RootLink: branchRootLink},
		},
	}

	fsBranch, err := files.NewInMemoryFiles(branchOpts)
	if err != nil {
		t.Fatalf("failed to create branch fs: %v", err)
	}
	defer fsBranch.Close()

	// Verify branch has file1.txt on startup
	info, err := fsBranch.Lookup(ctx, 1, "file1.txt")
	if err != nil {
		t.Fatalf("expected file1.txt to exist in branch: %v", err)
	}

	// 4. Modify branch and parent independently
	// In branch: create branch_only.txt and modify file1.txt
	err = fsBranch.CreateEntry(ctx, 1, "branch_only.txt", filetree.FileKind, "", nil, bytes.NewReader([]byte("branch only content")))
	if err != nil {
		t.Fatalf("failed to create branch_only.txt: %v", err)
	}

	err = fsBranch.WriteFile(ctx, info.Node, 0, false, bytes.NewReader([]byte("branch modified content")))
	if err != nil {
		t.Fatalf("failed to modify file1.txt in branch: %v", err)
	}

	err = fsBranch.Sync(ctx, 1, true)
	if err != nil {
		t.Fatalf("failed to sync branch: %v", err)
	}

	// In parent: create parent_only.txt
	err = fsParent.CreateEntry(ctx, 1, "parent_only.txt", filetree.FileKind, "", nil, bytes.NewReader([]byte("parent only content")))
	if err != nil {
		t.Fatalf("failed to create parent_only.txt in parent: %v", err)
	}

	err = fsParent.Sync(ctx, 1, true)
	if err != nil {
		t.Fatalf("failed to sync parent: %v", err)
	}

	// 5. Retrieve current addresses for merge
	parentCurrentAddress, err := memSlots.Get(ctx, parentSlotID)
	if err != nil {
		t.Fatalf("failed to get current parent address: %v", err)
	}

	branchCurrentAddress, err := memSlots.Get(ctx, branchSlotID)
	if err != nil {
		t.Fatalf("failed to get current branch address: %v", err)
	}

	// 6. Merge branch into parent
	mergedRootAddress, conflicts, err := MergeTrees(ctx, parentSnapshotHash, parentCurrentAddress, branchCurrentAddress, store, memSlots)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if len(conflicts) > 0 {
		t.Fatalf("expected no conflicts, got: %v", conflicts)
	}

	// Apply merge to parent slot
	err = memSlots.Update(ctx, parentSlotID, mergedRootAddress, parentCurrentAddress, nil)
	if err != nil {
		t.Fatalf("failed to update parent slot with merged address: %v", err)
	}

	// 7. Verify merged state in parent
	mergedOpts := files.Options{
		Storage:          store,
		Slots:            memSlots,
		RootLink:         parentRootLink,
		AutoSyncTimeout:  time.Hour,
		SlotPollInterval: time.Hour,
		Layers: []files.Layer{
			{RootLink: parentRootLink},
		},
	}

	fsMerged, err := files.NewInMemoryFiles(mergedOpts)
	if err != nil {
		t.Fatalf("failed to create merged parent fs: %v", err)
	}
	defer fsMerged.Close()

	// Verify file1.txt has branch's modified content
	info1, err := fsMerged.Lookup(ctx, 1, "file1.txt")
	if err != nil {
		t.Fatalf("expected file1.txt in merged fs: %v", err)
	}
	rc, err := fsMerged.ReadFile(ctx, info1.Node, 0, 0)
	if err != nil {
		t.Fatalf("failed to read file1.txt: %v", err)
	}
	content1, _ := io.ReadAll(rc)
	rc.Close()
	if string(content1) != "branch modified content" {
		t.Errorf("expected 'branch modified content', got %q", string(content1))
	}

	// Verify parent_only.txt exists with parent content
	infoParentOnly, err := fsMerged.Lookup(ctx, 1, "parent_only.txt")
	if err != nil {
		t.Fatalf("expected parent_only.txt in merged fs: %v", err)
	}
	rc, err = fsMerged.ReadFile(ctx, infoParentOnly.Node, 0, 0)
	if err != nil {
		t.Fatalf("failed to read parent_only.txt: %v", err)
	}
	contentParent, _ := io.ReadAll(rc)
	rc.Close()
	if string(contentParent) != "parent only content" {
		t.Errorf("expected 'parent only content', got %q", string(contentParent))
	}

	// Verify branch_only.txt exists with branch content
	infoBranchOnly, err := fsMerged.Lookup(ctx, 1, "branch_only.txt")
	if err != nil {
		t.Fatalf("expected branch_only.txt in merged fs: %v", err)
	}
	rc, err = fsMerged.ReadFile(ctx, infoBranchOnly.Node, 0, 0)
	if err != nil {
		t.Fatalf("failed to read branch_only.txt: %v", err)
	}
	contentBranch, _ := io.ReadAll(rc)
	rc.Close()
	if string(contentBranch) != "branch only content" {
		t.Errorf("expected 'branch only content', got %q", string(contentBranch))
	}
}

func TestWorkspace_MergeConflicts(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	memSlots := slots.NewMemorySlots("test-slot-id")

	// Create a parent workspace
	dirData, _ := json.Marshal(filetree.Directory{})
	initLink, _ := content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	parentSlotID := "parent-slot"
	_ = memSlots.Create(ctx, parentSlotID, initLink.Address, "")

	parentRootLink := content.ContentLink{
		Address: parentSlotID,
		Slot:    true,
	}

	parentOpts := files.Options{
		Storage:          store,
		Slots:            memSlots,
		RootLink:         parentRootLink,
		AutoSyncTimeout:  time.Hour,
		SlotPollInterval: time.Hour,
		Layers: []files.Layer{
			{RootLink: parentRootLink},
		},
	}

	fsParent, _ := files.NewInMemoryFiles(parentOpts)
	defer fsParent.Close()

	// Add file1.txt to parent
	_ = fsParent.CreateEntry(ctx, 1, "file1.txt", filetree.FileKind, "", nil, bytes.NewReader([]byte("parent content 1")))
	_ = fsParent.Sync(ctx, 1, true)

	parentSnapshotHash, _ := memSlots.Get(ctx, parentSlotID)

	// Create branch
	branchSlotID := "branch-slot"
	_ = memSlots.Create(ctx, branchSlotID, parentSnapshotHash, "")

	branchRootLink := content.ContentLink{
		Address: branchSlotID,
		Slot:    true,
	}

	branchOpts := files.Options{
		Storage:          store,
		Slots:            memSlots,
		RootLink:         branchRootLink,
		AutoSyncTimeout:  time.Hour,
		SlotPollInterval: time.Hour,
		Layers: []files.Layer{
			{RootLink: branchRootLink},
		},
	}

	fsBranch, _ := files.NewInMemoryFiles(branchOpts)
	defer fsBranch.Close()

	// Modify same file file1.txt in branch
	infoBranch, _ := fsBranch.Lookup(ctx, 1, "file1.txt")
	_ = fsBranch.WriteFile(ctx, infoBranch.Node, 0, false, bytes.NewReader([]byte("branch content change")))
	_ = fsBranch.Sync(ctx, 1, true)

	// Modify same file file1.txt in parent differently
	infoParent, _ := fsParent.Lookup(ctx, 1, "file1.txt")
	_ = fsParent.WriteFile(ctx, infoParent.Node, 0, false, bytes.NewReader([]byte("parent content change")))
	_ = fsParent.Sync(ctx, 1, true)

	// Retrieve current addresses for merge
	parentCurrentAddress, _ := memSlots.Get(ctx, parentSlotID)
	branchCurrentAddress, _ := memSlots.Get(ctx, branchSlotID)

	// Merge branch into parent, expecting conflict
	_, conflicts, err := MergeTrees(ctx, parentSnapshotHash, parentCurrentAddress, branchCurrentAddress, store, memSlots)
	if err != nil {
		t.Fatalf("unexpected error during merge: %v", err)
	}

	if len(conflicts) != 1 || conflicts[0] != "file1.txt" {
		t.Errorf("expected 1 conflict on 'file1.txt', got: %v", conflicts)
	}
}

func TestWorkspace_Rebase(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	memSlots := slots.NewMemorySlots("test-slot-id")

	// 1. Create parent workspace and base file
	dirData, _ := json.Marshal(filetree.Directory{})
	initLink, _ := content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	parentSlotID := "parent-slot"
	_ = memSlots.Create(ctx, parentSlotID, initLink.Address, "")

	parentRootLink := content.ContentLink{
		Address: parentSlotID,
		Slot:    true,
	}

	parentOpts := files.Options{
		Storage:          store,
		Slots:            memSlots,
		RootLink:         parentRootLink,
		AutoSyncTimeout:  time.Hour,
		SlotPollInterval: time.Hour,
		Layers: []files.Layer{
			{RootLink: parentRootLink},
		},
	}

	fsParent, _ := files.NewInMemoryFiles(parentOpts)
	defer fsParent.Close()

	_ = fsParent.CreateEntry(ctx, 1, "file1.txt", filetree.FileKind, "", nil, bytes.NewReader([]byte("base parent")))
	_ = fsParent.Sync(ctx, 1, true)

	parentSnapshotHash, _ := memSlots.Get(ctx, parentSlotID)

	// 2. Create branch
	branchSlotID := "branch-slot"
	_ = memSlots.Create(ctx, branchSlotID, parentSnapshotHash, "")

	branchRootLink := content.ContentLink{
		Address: branchSlotID,
		Slot:    true,
	}

	branchOpts := files.Options{
		Storage:          store,
		Slots:            memSlots,
		RootLink:         branchRootLink,
		AutoSyncTimeout:  time.Hour,
		SlotPollInterval: time.Hour,
		Layers: []files.Layer{
			{RootLink: branchRootLink},
		},
	}

	fsBranch, _ := files.NewInMemoryFiles(branchOpts)
	defer fsBranch.Close()

	// Modify branch: create branch_only.txt
	_ = fsBranch.CreateEntry(ctx, 1, "branch_only.txt", filetree.FileKind, "", nil, bytes.NewReader([]byte("branch content")))
	_ = fsBranch.Sync(ctx, 1, true)

	// Modify parent: create parent_only.txt
	_ = fsParent.CreateEntry(ctx, 1, "parent_only.txt", filetree.FileKind, "", nil, bytes.NewReader([]byte("parent content")))
	_ = fsParent.Sync(ctx, 1, true)

	// 3. Perform rebase programmatically
	parentCurrentAddress, _ := memSlots.Get(ctx, parentSlotID)
	branchCurrentAddress, _ := memSlots.Get(ctx, branchSlotID)

	mergedRootAddress, conflicts, err := MergeTrees(ctx, parentSnapshotHash, parentCurrentAddress, branchCurrentAddress, store, memSlots)
	if err != nil {
		t.Fatalf("rebase merge failed: %v", err)
	}
	if len(conflicts) > 0 {
		t.Fatalf("expected no conflicts, got: %v", conflicts)
	}

	// Update branch slot
	err = memSlots.Update(ctx, branchSlotID, mergedRootAddress, branchCurrentAddress, nil)
	if err != nil {
		t.Fatalf("failed to update branch slot: %v", err)
	}

	// 4. Verify branch state after rebase
	fsRebased, _ := files.NewInMemoryFiles(branchOpts)
	defer fsRebased.Close()

	// Branch should now contain branch_only.txt, parent_only.txt, and file1.txt
	if _, err := fsRebased.Lookup(ctx, 1, "branch_only.txt"); err != nil {
		t.Errorf("branch_only.txt missing from rebased branch: %v", err)
	}
	if _, err := fsRebased.Lookup(ctx, 1, "parent_only.txt"); err != nil {
		t.Errorf("parent_only.txt missing from rebased branch: %v", err)
	}
	if _, err := fsRebased.Lookup(ctx, 1, "file1.txt"); err != nil {
		t.Errorf("file1.txt missing from rebased branch: %v", err)
	}
}
