package fuse

import (
	"context"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"

	"invariant/internal/files"
	"invariant/internal/storage"
)

func TestFuseNodeCreation(t *testing.T) {
	storageClient := storage.NewInMemoryStorage()

	opts := files.Options{
		Storage: storageClient,
	}

	filesrv, err := files.NewInMemoryFiles(opts)
	if err != nil {
		t.Fatalf("Failed to initialize files service: %v", err)
	}
	defer filesrv.Close()

	ctx := context.Background()
	_ = filesrv.CreateEntry(ctx, 1, "test.txt", "File", "", nil, nil)

	rootNode := NewNode(filesrv, 1)
	if rootNode == nil {
		t.Fatal("Expected non-nil Node")
	}

	attrOut := &fuse.AttrOut{}
	errno := rootNode.Getattr(ctx, nil, attrOut)
	if errno != 0 {
		t.Errorf("Getattr failed with errno %d", errno)
	}

	if attrOut.Ino != 1 {
		t.Errorf("Expected Ino 1, got %d", attrOut.Ino)
	}
}
