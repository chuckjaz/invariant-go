package files

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"invariant/internal/content"
	"invariant/internal/discovery"
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
	err := memSlots.Create(context.Background(), "test-slot", initLink.Address, "")
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
	addr, err := memSlots.Get(context.Background(), "test-slot")
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
	err := memSlots.Create(context.Background(), "test-slot-multi", initLink.Address, "")
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
		Layers: []Layer{
			{RootLink: rootLink},
		},
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

func TestFilesService_WriteFile_AppendAndOffset(t *testing.T) {
	store := storage.NewInMemoryStorage()
	memSlots := slots.NewMemorySlots("test-slot-id")

	dirData, _ := json.Marshal(filetree.Directory{})
	initLink, _ := content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	memSlots.Create(context.Background(), "test-slot", initLink.Address, "")

	rootLink := content.ContentLink{
		Address: "test-slot",
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

	// Initial write (file size: 5, content: "hello")
	err = filesService.CreateEntry(ctx, 1, "test.txt", filetree.FileKind, "", nil, bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("failed to create entry: %v", err)
	}
	filesService.mu.RLock()
	fileID := filesService.nodes[1].Children["test.txt"]
	filesService.mu.RUnlock()

	// Append " world"
	err = filesService.WriteFile(ctx, fileID, 0, true, bytes.NewReader([]byte(" world")))
	if err != nil {
		t.Fatalf("failed to append: %v", err)
	}

	// Verify append
	rc, err := filesService.ReadFile(ctx, fileID, 0, 0)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(data))
	}

	// Overwrite part of the file (offset 6, "WORLD" -> "hello WORLD")
	err = filesService.WriteFile(ctx, fileID, 6, false, bytes.NewReader([]byte("WORLD")))
	if err != nil {
		t.Fatalf("failed to overwrite: %v", err)
	}

	rc, err = filesService.ReadFile(ctx, fileID, 0, 0)
	data, _ = io.ReadAll(rc)
	rc.Close()
	if string(data) != "hello WORLD" {
		t.Errorf("expected 'hello WORLD', got %q", string(data))
	}

	// Offset past end of file (zero padding)
	err = filesService.WriteFile(ctx, fileID, 15, false, bytes.NewReader([]byte("!")))
	if err != nil {
		t.Fatalf("failed to write past EOF: %v", err)
	}

	rc, err = filesService.ReadFile(ctx, fileID, 0, 0)
	data, _ = io.ReadAll(rc)
	rc.Close()
	expected := "hello WORLD\x00\x00\x00\x00!"
	if string(data) != expected {
		t.Errorf("expected %q, got %q", expected, string(data))
	}
}

// mockDiscovery is a simple mock discovery service for testing
type mockDiscovery struct {
	services map[string]discovery.ServiceDescription
}

func (m *mockDiscovery) Find(ctx context.Context, protocol, tag string, count int) ([]discovery.ServiceDescription, error) {
	return nil, nil // Not needed for this test
}

func (m *mockDiscovery) Get(ctx context.Context, id string) (discovery.ServiceDescription, bool) {
	desc, ok := m.services[id]
	return desc, ok
}

func (m *mockDiscovery) Register(ctx context.Context, reg discovery.ServiceRegistration) error {
	return nil
}

func TestFilesService_StorageDestination(t *testing.T) {
	// Root storage
	defaultStore := storage.NewInMemoryStorage()

	destStore := storage.NewInMemoryStorage()
	destServer := storage.NewStorageServer(destStore)
	ts := httptest.NewServer(destServer.Handler())
	defer ts.Close()

	customDestID := strings.Repeat("a", 64)

	disc := &mockDiscovery{
		services: map[string]discovery.ServiceDescription{
			customDestID: {ID: customDestID, Address: ts.URL},
		},
	}

	memSlots := slots.NewMemorySlots("test-slot-id")

	dirData, _ := json.Marshal(filetree.Directory{})
	initLink, _ := content.Write(bytes.NewReader(dirData), defaultStore, content.WriterOptions{})
	content.Write(bytes.NewReader(dirData), destStore, content.WriterOptions{}) // Give destStore the root directory block too
	memSlots.Create(context.Background(), "test-slot", initLink.Address, "")

	rootLink := content.ContentLink{
		Address: "test-slot",
		Slot:    true,
	}

	filesService, err := NewInMemoryFiles(Options{
		Storage:          defaultStore,
		Discovery:        disc,
		Slots:            memSlots,
		RootLink:         rootLink,
		AutoSyncTimeout:  time.Hour,
		SlotPollInterval: time.Hour,
		Layers: []Layer{
			{
				RootLink: rootLink,
				Excludes: []string{"/dest-dir", "/dest-dir/*"},
			},
			{
				RootLink:           rootLink, // Using same root mock for test simplicity
				Includes:           []string{"/dest-dir", "/dest-dir/*"},
				StorageDestination: customDestID,
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	defer filesService.Close()

	ctx := context.Background()

	err = filesService.CreateEntry(ctx, 1, "dest-dir", filetree.DirectoryKind, "", nil, nil)
	if err != nil {
		t.Fatalf("failed to create entry: %v", err)
	}

	filesService.mu.RLock()
	dirID := filesService.nodes[1].Children["dest-dir"]
	filesService.mu.RUnlock()

	err = filesService.CreateEntry(ctx, dirID, "test-dest.txt", filetree.FileKind, "", nil, bytes.NewReader([]byte("destination content")))
	if err != nil {
		t.Fatalf("failed to create entry: %v", err)
	}

	filesService.mu.RLock()
	fileID := filesService.nodes[dirID].Children["test-dest.txt"]
	fileNode := filesService.nodes[fileID]
	filesService.mu.RUnlock()

	// Content should explicitly not exist in defaultStore
	if defaultStore.Has(ctx, fileNode.Content.Address) {
		t.Errorf("Expected content to NOT be in defaultStore")
	}

	// Content should explicitly exist in destStore
	if !destStore.Has(ctx, fileNode.Content.Address) {
		t.Errorf("Expected content to be in destStore")
	}
}

func TestLayerJSON(t *testing.T) {
	// 1. Marshal/Unmarshal standard Layer
	l1 := Layer{
		RootLink:           content.ContentLink{Address: "addr1"},
		Includes:           []string{"*.txt"},
		Excludes:           []string{"*.log"},
		StorageDestination: "dest1",
		ReadOnly:           true,
	}

	data, err := json.Marshal(l1)
	if err != nil {
		t.Fatal(err)
	}

	var l2 Layer
	err = json.Unmarshal(data, &l2)
	if err != nil {
		t.Fatal(err)
	}

	if l1.StorageDestination != l2.StorageDestination || l1.ReadOnly != l2.ReadOnly || l2.RootLink.Address != "addr1" {
		t.Errorf("Unexpected unmarshal result: %+v", l2)
	}

	// 2. Marshal/Unmarshal temporary slot RootLink
	lTemp := Layer{
		RootLink: content.ContentLink{Slot: true},
	}
	data, err = json.Marshal(lTemp)
	if err != nil {
		t.Fatal(err)
	}

	var lTempDecoded Layer
	err = json.Unmarshal(data, &lTempDecoded)
	if err != nil {
		t.Fatal(err)
	}
	if !lTempDecoded.RootLink.Slot || lTempDecoded.RootLink.Address != "" {
		t.Errorf("Expected temporary slot RootLink, got %+v", lTempDecoded.RootLink)
	}

	// 3. UnmarshalJSON errors
	var l3 Layer
	err = json.Unmarshal([]byte("123"), &l3)
	if err == nil {
		t.Error("Expected error unmarshalling number as Layer")
	}

	err = json.Unmarshal([]byte(`{"rootLink": 123}`), &l3)
	if err == nil {
		t.Error("Expected error unmarshalling number as RootLink")
	}
}

//go:fix inline
func uint64Ptr(v uint64) *uint64 {
	return new(v)
}

func TestFilesService_AllEndpoints(t *testing.T) {
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

	filesService, err := NewInMemoryFiles(Options{
		Storage:          store,
		Slots:            memSlots,
		RootLink:         rootLink,
		AutoSyncTimeout:  10 * time.Millisecond,
		SlotPollInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	defer filesService.Close()

	server := NewServer(filesService)
	handler := server.Handler()

	// 1. Create directory 'dir1'
	req := httptest.NewRequest(http.MethodPut, "/1/dir1?kind=Directory", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %v: %v", rr.Code, rr.Body.String())
	}

	// 2. Lookup 'dir1'
	req = httptest.NewRequest(http.MethodGet, "/lookup/1/dir1", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %v", rr.Code)
	}
	var lookupRes ContentInformationCommon
	if err := json.Unmarshal(rr.Body.Bytes(), &lookupRes); err != nil {
		t.Fatalf("failed to unmarshal dir1 ID: %v", err)
	}
	dir1ID := lookupRes.Node

	// 3. Create a file 'file1' inside 'dir1'
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/%d/file1", dir1ID), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %v: %v", rr.Code, rr.Body.String())
	}

	// Lookup 'file1'
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/lookup/%d/file1", dir1ID), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %v", rr.Code)
	}
	var lookupResFile ContentInformationCommon
	if err := json.Unmarshal(rr.Body.Bytes(), &lookupResFile); err != nil {
		t.Fatalf("failed to unmarshal file1 ID: %v", err)
	}
	file1ID := lookupResFile.Node

	// Post content to 'file1'
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/file/%d", file1ID), bytes.NewReader([]byte("test file content")))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %v", rr.Code)
	}

	// 4. GET /directory/{id}
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/directory/%d", dir1ID), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for handleGetDirectory, got %v", rr.Code)
	}

	// 5. GET /attributes/{id}
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/attributes/%d", file1ID), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for handleGetAttributes, got %v", rr.Code)
	}

	// 6. POST /attributes/{id}
	attrs := EntryAttributes{Size: new(uint64(10))}
	attrsBody, _ := json.Marshal(attrs)
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/attributes/%d", file1ID), bytes.NewReader(attrsBody))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for handleSetAttributes, got %v: %v", rr.Code, rr.Body.String())
	}

	// 7. GET /content/{id}
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/content/%d", file1ID), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for handleGetContent, got %v", rr.Code)
	}

	// 8. GET /info/{id}
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/info/%d", file1ID), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for handleGetInfo, got %v", rr.Code)
	}

	// 9. PUT /link/{parent_id}/{name}?node={target_id}
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/link/1/link1?node=%d", file1ID), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Errorf("expected 201 Created for handleLink, got %v: %v", rr.Code, rr.Body.String())
	}

	// 10. POST /rename/{src_parent_id}/{src_name}?name={newName}&directory={dest_parent_id}
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/rename/%d/file1?name=file1_renamed&directory=%d", dir1ID, dir1ID), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for handleRename, got %v: %v", rr.Code, rr.Body.String())
	}

	// 11. PUT /remove/{parent_id}/{name}
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/remove/%d/file1_renamed", dir1ID), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for handleRemove, got %v: %v", rr.Code, rr.Body.String())
	}

	// 12. PUT /sync
	req = httptest.NewRequest(http.MethodPut, "/sync", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for handleSync, got %v: %v", rr.Code, rr.Body.String())
	}

	// 13. Test parseNodeID in InMemoryFiles directly
	_, errParsed := filesService.parseNodeID("/123")
	if errParsed != nil {
		t.Errorf("Unexpected error parsing valid node ID: %v", errParsed)
	}
	_, errParsed = filesService.parseNodeID("invalid")
	if errParsed == nil {
		t.Error("Expected error parsing invalid node ID, got nil")
	}

	// 14. Recursive deletion test (covers deleteNodeRecursively)
	// Create parent dir
	req = httptest.NewRequest(http.MethodPut, "/1/delete_tree?kind=Directory", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("failed to create delete_tree dir: %v", rr.Code)
	}

	var deleteTreeRes ContentInformationCommon
	req = httptest.NewRequest(http.MethodGet, "/lookup/1/delete_tree", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	json.Unmarshal(rr.Body.Bytes(), &deleteTreeRes)
	deleteTreeID := deleteTreeRes.Node

	// Create subdir
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/%d/sub_dir?kind=Directory", deleteTreeID), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("failed to create sub_dir: %v", rr.Code)
	}

	var subDirRes ContentInformationCommon
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/lookup/%d/sub_dir", deleteTreeID), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	json.Unmarshal(rr.Body.Bytes(), &subDirRes)
	subDirID := subDirRes.Node

	// Create subfile
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/%d/sub_file", subDirID), nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("failed to create sub_file: %v", rr.Code)
	}

	// Delete parent dir
	req = httptest.NewRequest(http.MethodPut, "/remove/1/delete_tree", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for recursive remove, got %v", rr.Code)
	}

	// 15. Server error/bad request paths
	// GET /file/invalid
	req = httptest.NewRequest(http.MethodGet, "/file/invalid", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request for invalid GET file ID, got %d", rr.Code)
	}

	// PUT /link/1/link2 (missing node param)
	req = httptest.NewRequest(http.MethodPut, "/link/1/link2", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request for link missing node, got %d", rr.Code)
	}

	// PUT /link/1/link2?node=invalid
	req = httptest.NewRequest(http.MethodPut, "/link/1/link2?node=invalid", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request for link invalid node, got %d", rr.Code)
	}

	// POST /rename/1/dir1 (missing name param)
	req = httptest.NewRequest(http.MethodPost, "/rename/1/dir1", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request for rename missing name, got %d", rr.Code)
	}

	// POST /rename/1/dir1?name=newname&directory=invalid
	req = httptest.NewRequest(http.MethodPost, "/rename/1/dir1?name=newname&directory=invalid", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request for rename invalid directory, got %d", rr.Code)
	}
}

func TestFilesService_MountConfigPseudoFile(t *testing.T) {
	store := storage.NewInMemoryStorage()
	mc := &MountConfig{
		InvariantMount:  true,
		CacheDir:        "/tmp/test-cache",
		IsWorkspace:     true,
		DiscoveryURL:    "http://127.0.0.1:8080",
		CacheSizeMB:     128,
		DiskCacheSizeMB: 1024,
	}

	filesService, err := NewInMemoryFiles(Options{
		Storage:     store,
		MountConfig: mc,
	})
	if err != nil {
		t.Fatalf("Failed to create files service: %v", err)
	}
	defer filesService.Close()

	ctx := context.Background()
	info, err := filesService.Lookup(ctx, 1, ".invariant-mount.json")
	if err != nil {
		t.Fatalf("Lookup(.invariant-mount.json) failed: %v", err)
	}

	if info.Kind != string(filetree.FileKind) {
		t.Errorf("Expected FileKind for pseudo file, got %s", info.Kind)
	}

	r, err := filesService.ReadFile(ctx, info.Node, 0, 0)
	if err != nil {
		t.Fatalf("ReadFile(.invariant-mount.json) failed: %v", err)
	}
	defer r.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("Failed to read pseudo file content: %v", err)
	}

	var readMC MountConfig
	if err := json.Unmarshal(data, &readMC); err != nil {
		t.Fatalf("Failed to unmarshal .invariant-mount.json content: %v", err)
	}

	if !readMC.InvariantMount || !readMC.IsWorkspace || readMC.CacheDir != "/tmp/test-cache" {
		t.Errorf("Mismatch in MountConfig fields: %+v", readMC)
	}

	entries, err := filesService.ReadDirectory(ctx, 1, 0, 0)
	if err != nil {
		t.Fatalf("ReadDirectory failed: %v", err)
	}

	found := false
	for _, entry := range entries {
		if entry.GetName() == ".invariant-mount.json" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected .invariant-mount.json in ReadDirectory entries")
	}
}

func TestFilesService_ConcurrentCreateEntry(t *testing.T) {
	store := storage.NewInMemoryStorage()
	memSlots := slots.NewMemorySlots("test-slot-concurrent")

	dirData, _ := json.Marshal(filetree.Directory{})
	initLink, err := content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	err = memSlots.Create(context.Background(), "test-slot", initLink.Address, "")
	if err != nil {
		t.Fatal(err)
	}

	filesService, err := NewInMemoryFiles(Options{
		Storage: store,
		Slots:   memSlots,
		RootLink: content.ContentLink{
			Address: "test-slot",
			Slot:    true,
		},
	})
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	defer filesService.Close()

	ctx := context.Background()
	const numFiles = 50
	errCh := make(chan error, numFiles)

	for i := 0; i < numFiles; i++ {
		go func(idx int) {
			name := fmt.Sprintf("concurrent-file-%d.txt", idx)
			data := []byte(fmt.Sprintf("Concurrent file data content #%d", idx))
			err := filesService.CreateEntry(ctx, 1, name, filetree.FileKind, "", nil, bytes.NewReader(data))
			if err != nil {
				errCh <- err
				return
			}

			// Concurrent lookup
			info, err := filesService.Lookup(ctx, 1, name)
			if err != nil {
				errCh <- err
				return
			}

			// Concurrent read
			r, err := filesService.ReadFile(ctx, info.Node, 0, 0)
			if err != nil {
				errCh <- err
				return
			}
			defer r.Close()
			readData, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(readData, data) {
				errCh <- fmt.Errorf("data mismatch for %s", name)
				return
			}

			errCh <- nil
		}(i)
	}

	for i := 0; i < numFiles; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("Concurrent operation failed: %v", err)
		}
	}
}
