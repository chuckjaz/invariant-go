package files

import (
	"bytes"
	"encoding/json"
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
