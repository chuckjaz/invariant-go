package files

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"invariant/internal/content"
	"invariant/internal/filetree"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestFilesService_MergeRemoteIntoLocal_Nested(t *testing.T) {
	store := storage.NewInMemoryStorage()
	memSlots := slots.NewMemorySlots("test-slot-id")

	dirData, _ := json.Marshal(filetree.Directory{})
	initLink, _ := content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	err := memSlots.Create(context.Background(), "test-slot", initLink.Address, "")
	if err != nil {
		t.Fatal(err)
	}

	rootLink := content.ContentLink{
		Address: "test-slot",
		Slot:    true,
	}

	opts := Options{
		Storage:          store,
		Slots:            memSlots,
		RootLink:         rootLink,
		AutoSyncTimeout:  time.Hour,
		SlotPollInterval: time.Hour,
		Layers: []Layer{
			{RootLink: rootLink},
		},
	}

	fs1, err := NewInMemoryFiles(opts)
	if err != nil {
		t.Fatalf("failed to create fs1: %v", err)
	}
	defer fs1.Close()

	ctx := context.Background()

	// Initial setup: create a nested directory and a base file
	err = fs1.CreateEntry(ctx, 1, "nested", filetree.DirectoryKind, "", nil, nil)
	if err != nil {
		t.Fatalf("failed to create nested dir: %v", err)
	}

	info, err := fs1.Lookup(ctx, 1, "nested")
	if err != nil {
		t.Fatalf("failed to lookup nested dir: %v", err)
	}
	nestedID := info.Node

	err = fs1.CreateEntry(ctx, nestedID, "base.txt", filetree.FileKind, "", nil, bytes.NewReader([]byte("base")))
	if err != nil {
		t.Fatalf("failed to create base.txt: %v", err)
	}

	// Sync fs1 to establish the baseline in the slot
	err = fs1.Sync(ctx, 1, true)
	if err != nil {
		t.Fatalf("failed to sync fs1: %v", err)
	}

	// Now simulate a remote change using a second fs instance (fs2)
	fs2, err := NewInMemoryFiles(opts)
	if err != nil {
		t.Fatalf("failed to create fs2: %v", err)
	}
	defer fs2.Close()

	// fs2 reads the baseline
	info2, err := fs2.Lookup(ctx, 1, "nested")
	if err != nil {
		t.Fatalf("fs2 failed to lookup nested: %v", err)
	}
	fs2NestedID := info2.Node

	// fs2 creates remote.txt (sibling change)
	err = fs2.CreateEntry(ctx, fs2NestedID, "remote.txt", filetree.FileKind, "", nil, bytes.NewReader([]byte("remote")))
	if err != nil {
		t.Fatalf("fs2 failed to create remote.txt: %v", err)
	}

	// fs2 modifies base.txt
	infoBase2, err := fs2.Lookup(ctx, fs2NestedID, "base.txt")
	if err != nil {
		t.Fatalf("fs2 failed to lookup base.txt: %v", err)
	}
	err = fs2.WriteFile(ctx, infoBase2.Node, 0, false, bytes.NewReader([]byte("remote base")))
	if err != nil {
		t.Fatalf("fs2 failed to overwrite base.txt: %v", err)
	}

	// Sync fs2 to publish changes to the slot
	err = fs2.Sync(ctx, 1, true)
	if err != nil {
		t.Fatalf("failed to sync fs2: %v", err)
	}

	// Back in fs1:
	// 1. Create a local sibling
	err = fs1.CreateEntry(ctx, nestedID, "local.txt", filetree.FileKind, "", nil, bytes.NewReader([]byte("local")))
	if err != nil {
		t.Fatalf("fs1 failed to create local.txt: %v", err)
	}

	// 2. Modify base.txt locally
	infoBase1, err := fs1.Lookup(ctx, nestedID, "base.txt")
	if err != nil {
		t.Fatalf("fs1 failed to lookup base.txt: %v", err)
	}
	err = fs1.WriteFile(ctx, infoBase1.Node, 0, false, bytes.NewReader([]byte("local base")))
	if err != nil {
		t.Fatalf("fs1 failed to overwrite base.txt: %v", err)
	}

	// Trigger slot poll in fs1 to pull in fs2's changes and merge them
	fs1.pollSlot()

	// Verify the result in fs1's nested directory
	// 1. local.txt should exist (local sibling kept)
	_, err = fs1.Lookup(ctx, nestedID, "local.txt")
	if err != nil {
		t.Errorf("expected local.txt to exist: %v", err)
	}

	// 2. remote.txt should exist (remote sibling merged in)
	_, err = fs1.Lookup(ctx, nestedID, "remote.txt")
	if err != nil {
		t.Errorf("expected remote.txt to exist: %v", err)
	}

	// 3. base.txt should have "local base" as its content (local modification overrides remote)
	rc, err := fs1.ReadFile(ctx, infoBase1.Node, 0, 0)
	if err != nil {
		t.Fatalf("failed to read base.txt: %v", err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "local base" {
		t.Errorf("expected 'local base', got %q", string(data))
	}

	// Verify remote content is actually 'remote'
	infoRemote, err := fs1.Lookup(ctx, nestedID, "remote.txt")
	if err == nil {
		rc, _ = fs1.ReadFile(ctx, infoRemote.Node, 0, 0)
		data, _ = io.ReadAll(rc)
		rc.Close()
		if string(data) != "remote" {
			t.Errorf("expected 'remote', got %q", string(data))
		}
	}
}
