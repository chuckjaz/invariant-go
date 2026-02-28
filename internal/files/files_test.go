package files

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"invariant/internal/content"
	"invariant/internal/filetree"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestFilesService_ReadOnly(t *testing.T) {
	store := storage.NewInMemoryStorage()

	// Create an empty root directory
	filesService, err := NewInMemoryFiles(Options{
		Storage: store,
	})
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	defer filesService.Close()

	if filesService.isWritable() {
		t.Errorf("expected read-only service")
	}

	server := NewServer(filesService)
	handler := server.Handler()

	// Ensure PUT fails with read-only
	req := httptest.NewRequest(http.MethodPut, "/1/test.txt", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for writing to read-only service, got %v", rr.Code)
	}
}

func TestFilesService_WriteAndSync(t *testing.T) {
	store := storage.NewInMemoryStorage()
	memSlots := slots.NewMemorySlots("test-slot-id")

	dirData, _ := json.Marshal(filetree.Directory{})
	initLink, _ := content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	err := memSlots.Create("test-slot", initLink.Address)
	if err != nil {
		t.Fatal(err)
	}

	rootLink := content.ContentLink{
		Address: "test-slot",
		Slot:    true,
	}

	filesService, err := NewInMemoryFiles(Options{
		Storage:          store,
		Slots:            memSlots,
		RootLink:         rootLink,
		AutoSyncTimeout:  10 * time.Millisecond,
		SlotPollInterval: time.Hour, // don't care about slot polling for this test
	})

	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	defer filesService.Close()

	if !filesService.isWritable() {
		t.Errorf("expected writable service")
	}

	server := NewServer(filesService)
	handler := server.Handler()

	// Write empty file
	req := httptest.NewRequest(http.MethodPut, "/1/test.txt", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %v: %v", rr.Code, rr.Body.String())
	}

	// Post content to file
	// Note: In our current implementation childID assigned will be 2
	data := []byte("hello world")
	req = httptest.NewRequest(http.MethodPost, "/file/2", bytes.NewReader(data))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %v: %v", rr.Code, rr.Body.String())
	}

	// Read file
	req = httptest.NewRequest(http.MethodGet, "/file/2", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %v: %v", rr.Code, rr.Body.String())
	}

	if !bytes.Equal(rr.Body.Bytes(), data) {
		t.Errorf("expected %q, got %q", data, rr.Body.Bytes())
	}

	// Trigger sync manually so we can verify the address properly changes
	req = httptest.NewRequest(http.MethodPut, "/sync?wait=true", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on sync, got %v", rr.Code)
	}

	// Check slot
	addr, err := memSlots.Get("test-slot")
	if err != nil {
		t.Fatal(err)
	}
	if addr == "init-addr" {
		t.Fatalf("slot address was not updated after sync")
	}
}

func TestFilesService_WriteAndSyncMultipleParents(t *testing.T) {
	store := storage.NewInMemoryStorage()
	memSlots := slots.NewMemorySlots("test-slot-multi-id")

	dirData, _ := json.Marshal(filetree.Directory{})
	initLink, _ := content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	err := memSlots.Create("test-slot-multi", initLink.Address)
	if err != nil {
		t.Fatal(err)
	}

	rootLink := content.ContentLink{
		Address: "test-slot-multi",
		Slot:    true,
	}

	filesService, err := NewInMemoryFiles(Options{
		Storage:          store,
		Slots:            memSlots,
		RootLink:         rootLink,
		AutoSyncTimeout:  time.Hour,
		SlotPollInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	defer filesService.Close()

	ctx := context.Background()

	// Create a subdirectory "dir1"
	err = filesService.CreateEntry(ctx, 1, "dir1", filetree.DirectoryKind, "", nil, nil)
	if err != nil {
		t.Fatalf("failed to create dir1: %v", err)
	}

	filesService.mu.RLock()
	dir1ID := filesService.nodes[1].Children["dir1"]
	filesService.mu.RUnlock()

	// Create `file1` within `dir1`
	err = filesService.CreateEntry(ctx, dir1ID, "file1", filetree.FileKind, "", nil, bytes.NewReader([]byte("init")))
	if err != nil {
		t.Fatalf("failed to create file1: %v", err)
	}

	filesService.mu.RLock()
	file1ID := filesService.nodes[dir1ID].Children["file1"]
	filesService.mu.RUnlock()

	// Link `file1` into the root directory directly
	err = filesService.Link(ctx, 1, "file1-link", file1ID)
	if err != nil {
		t.Fatalf("failed to link file1: %v", err)
	}

	// Reset dirty state to properly observe write side-effects
	filesService.mu.Lock()
	for k := range filesService.dirtyNodes {
		delete(filesService.dirtyNodes, k)
	}
	for _, node := range filesService.nodes {
		node.IsDirty = false
	}
	filesService.mu.Unlock()

	// Modify the file content to simulate a write event
	server := NewServer(filesService)
	handler := server.Handler()

	data := []byte("updated content")
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/file/%d", file1ID), bytes.NewReader(data))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %v: %v", rr.Code, rr.Body.String())
	}

	// Verify both dir1 and root directories are marked dirty
	filesService.mu.RLock()
	defer filesService.mu.RUnlock()

	if !filesService.dirtyNodes[dir1ID] {
		t.Errorf("expected dir1 to be marked dirty because it contains the file")
	}

	if !filesService.dirtyNodes[1] {
		t.Errorf("expected root directory to be marked dirty because it contains a link to the file")
	}
}
