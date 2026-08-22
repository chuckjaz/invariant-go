package buildcache

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/kv"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestHandler_BasicFlow(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "build-cache")

	memoryKV := kv.NewMemoryKeyValueStore()
	memoryStorage := storage.NewInMemoryStorage()
	memorySlots := slots.NewMemorySlots("test-slot")

	cfg := CacheConfig{
		CacheDir: cacheDir,
		KVStore:  memoryKV,
		Storage:  memoryStorage,
		Slots:    memorySlots,
	}

	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	// Prepare stdin buffer with: Put request + body, Get request, Close request
	var stdinBuf bytes.Buffer
	writer := bufio.NewWriter(&stdinBuf)

	actionID := []byte("test-action-1")
	outputID := []byte("test-output-1")
	bodyData := []byte("hello build cache content")

	putReq := Request{
		ID:       1,
		Command:  CmdPut,
		ActionID: actionID,
		OutputID: outputID,
		BodySize: int64(len(bodyData)),
	}
	putJSON, _ := json.Marshal(putReq)
	writer.Write(putJSON)
	writer.WriteByte('\n')

	bodyJSON, _ := json.Marshal(bodyData)
	writer.Write(bodyJSON)
	writer.WriteByte('\n')

	getReq := Request{
		ID:       2,
		Command:  CmdGet,
		ActionID: actionID,
	}
	getJSON, _ := json.Marshal(getReq)
	writer.Write(getJSON)
	writer.WriteByte('\n')

	closeReq := Request{
		ID:      3,
		Command: CmdClose,
	}
	closeJSON, _ := json.Marshal(closeReq)
	writer.Write(closeJSON)
	writer.WriteByte('\n')

	writer.Flush()

	var stdoutBuf bytes.Buffer
	if err := handler.Start(&stdinBuf, &stdoutBuf); err != nil {
		t.Fatalf("handler.Start returned error: %v", err)
	}

	reader := bufio.NewReader(&stdoutBuf)

	// 1. Handshake response
	line1, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read line 1: %v", err)
	}
	var initResp Response
	if err := json.Unmarshal(bytes.TrimSpace(line1), &initResp); err != nil {
		t.Fatalf("failed to unmarshal line 1: %v", err)
	}
	if initResp.ID != 0 || len(initResp.KnownCommands) != 3 {
		t.Errorf("unexpected handshake response: %+v", initResp)
	}

	// 2. Put response
	line2, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read line 2: %v", err)
	}
	var putResp Response
	if err := json.Unmarshal(bytes.TrimSpace(line2), &putResp); err != nil {
		t.Fatalf("failed to unmarshal line 2: %v", err)
	}
	if putResp.ID != 1 || putResp.Err != "" || putResp.DiskPath == "" {
		t.Errorf("unexpected put response: %+v", putResp)
	}

	// Verify local cached file was written
	fileContent, err := os.ReadFile(putResp.DiskPath)
	if err != nil {
		t.Fatalf("failed to read cached disk file: %v", err)
	}
	if string(fileContent) != string(bodyData) {
		t.Errorf("expected file content %q, got %q", bodyData, fileContent)
	}

	// Verify KV entry exists under key go-build-cache:<hex(ActionID)>
	kvKey := "go-build-cache:" + hex.EncodeToString(actionID)
	kvVal, _, err := memoryKV.Get(t.Context(), nil, kvKey)
	if err != nil {
		t.Fatalf("failed to get KV key %s: %v", kvKey, err)
	}
	var entry ActionEntry
	if err := json.Unmarshal(kvVal, &entry); err != nil {
		t.Fatalf("failed to unmarshal KV ActionEntry: %v", err)
	}
	if string(entry.OutputID) != string(outputID) || entry.Size != int64(len(bodyData)) {
		t.Errorf("unexpected KV ActionEntry: %+v", entry)
	}

	// 3. Get response
	line3, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read line 3: %v", err)
	}
	var getResp Response
	if err := json.Unmarshal(bytes.TrimSpace(line3), &getResp); err != nil {
		t.Fatalf("failed to unmarshal line 3: %v", err)
	}
	if getResp.ID != 2 || getResp.Miss || getResp.DiskPath == "" || string(getResp.OutputID) != string(outputID) {
		t.Errorf("unexpected get response: %+v", getResp)
	}

	// 4. Close response
	line4, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read line 4: %v", err)
	}
	var closeResp Response
	if err := json.Unmarshal(bytes.TrimSpace(line4), &closeResp); err != nil {
		t.Fatalf("failed to unmarshal line 4: %v", err)
	}
	if closeResp.ID != 3 {
		t.Errorf("unexpected close response: %+v", closeResp)
	}
}

func TestHandler_CacheMiss(t *testing.T) {
	tempDir := t.TempDir()
	handler, err := NewHandler(CacheConfig{
		CacheDir: filepath.Join(tempDir, "build-cache"),
		KVStore:  kv.NewMemoryKeyValueStore(),
		Storage:  storage.NewInMemoryStorage(),
	})
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	var stdinBuf bytes.Buffer
	getReq := Request{
		ID:       10,
		Command:  CmdGet,
		ActionID: []byte("non-existent-action"),
	}
	getJSON, _ := json.Marshal(getReq)
	stdinBuf.Write(getJSON)
	stdinBuf.WriteByte('\n')

	closeReq := Request{ID: 11, Command: CmdClose}
	closeJSON, _ := json.Marshal(closeReq)
	stdinBuf.Write(closeJSON)
	stdinBuf.WriteByte('\n')

	var stdoutBuf bytes.Buffer
	if err := handler.Start(&stdinBuf, &stdoutBuf); err != nil {
		t.Fatalf("handler.Start returned error: %v", err)
	}

	reader := bufio.NewReader(&stdoutBuf)
	// Skip handshake
	_, _ = reader.ReadBytes('\n')

	// Get response
	getLine, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read get response line: %v", err)
	}
	var getResp Response
	if err := json.Unmarshal(bytes.TrimSpace(getLine), &getResp); err != nil {
		t.Fatalf("failed to unmarshal get response: %v", err)
	}
	if getResp.ID != 10 || !getResp.Miss {
		t.Errorf("expected cache miss, got: %+v", getResp)
	}
}

func TestHandler_CompressionAndEncryption(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "build-cache")

	memoryKV := kv.NewMemoryKeyValueStore()
	memoryStorage := storage.NewInMemoryStorage()

	suppliedKey := bytes.Repeat([]byte{0x42}, 32)

	cfg := CacheConfig{
		CacheDir: cacheDir,
		KVStore:  memoryKV,
		Storage:  memoryStorage,
		WriterOptions: content.WriterOptions{
			CompressAlgorithm: "gzip",
			EncryptAlgorithm:  "aes-256-cbc",
			KeyPolicy:         content.SuppliedAllKey,
			SuppliedKey:       suppliedKey,
		},
	}

	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	actionID := []byte("encrypted-action")
	outputID := []byte("encrypted-output")
	bodyData := []byte("super secret data to compress and encrypt")

	var stdinBuf bytes.Buffer
	writer := bufio.NewWriter(&stdinBuf)

	putReq := Request{
		ID:       1,
		Command:  CmdPut,
		ActionID: actionID,
		OutputID: outputID,
		BodySize: int64(len(bodyData)),
	}
	putJSON, _ := json.Marshal(putReq)
	writer.Write(putJSON)
	writer.WriteByte('\n')

	bodyJSON, _ := json.Marshal(bodyData)
	writer.Write(bodyJSON)
	writer.WriteByte('\n')

	// Delete local file to simulate cold disk cache retrieval
	closeReq := Request{ID: 2, Command: CmdClose}
	closeJSON, _ := json.Marshal(closeReq)
	writer.Write(closeJSON)
	writer.WriteByte('\n')

	writer.Flush()

	var stdoutBuf bytes.Buffer
	if err := handler.Start(&stdinBuf, &stdoutBuf); err != nil {
		t.Fatalf("handler.Start failed: %v", err)
	}

	// Remove local cached file from disk to test fetching encrypted/compressed content from storage
	localFilePath := filepath.Join(cacheDir, hex.EncodeToString(actionID))
	_ = os.Remove(localFilePath)

	// Now issue a Get request
	var stdinBuf2 bytes.Buffer
	getReq := Request{ID: 3, Command: CmdGet, ActionID: actionID}
	getJSON, _ := json.Marshal(getReq)
	stdinBuf2.Write(getJSON)
	stdinBuf2.WriteByte('\n')

	closeReq2 := Request{ID: 4, Command: CmdClose}
	closeJSON2, _ := json.Marshal(closeReq2)
	stdinBuf2.Write(closeJSON2)
	stdinBuf2.WriteByte('\n')

	var stdoutBuf2 bytes.Buffer
	if err := handler.Start(&stdinBuf2, &stdoutBuf2); err != nil {
		t.Fatalf("handler.Start 2 failed: %v", err)
	}

	reader2 := bufio.NewReader(&stdoutBuf2)
	// Handshake
	_, _ = reader2.ReadBytes('\n')
	// Get response
	getLine, err := reader2.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read get response line: %v", err)
	}
	var getResp Response
	if err := json.Unmarshal(bytes.TrimSpace(getLine), &getResp); err != nil {
		t.Fatalf("failed to unmarshal get response: %v", err)
	}
	if getResp.ID != 3 || getResp.Miss || getResp.DiskPath == "" {
		t.Fatalf("expected hit on get response, got: %+v", getResp)
	}

	// Read downloaded file to ensure decryption and decompression restored original body
	restoredContent, err := os.ReadFile(getResp.DiskPath)
	if err != nil {
		t.Fatalf("failed to read restored disk file: %v", err)
	}
	if string(restoredContent) != string(bodyData) {
		t.Errorf("expected restored content %q, got %q", bodyData, restoredContent)
	}
}

func TestHandler_LRUEvictionAndKVBackup(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "build-cache")

	memoryKV := kv.NewMemoryKeyValueStore()
	memoryStorage := storage.NewInMemoryStorage()
	memorySlots := slots.NewMemorySlots("test-slot")

	cfg := CacheConfig{
		CacheDir:         cacheDir,
		KVStore:          memoryKV,
		Storage:          memoryStorage,
		Slots:            memorySlots,
		InMemoryCapacity: 2,
	}

	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	// Put 3 items to overflow capacity 2
	for i := 1; i <= 3; i++ {
		actionID := []byte(filepath.Join("act", string(rune('0'+i))))
		outputID := []byte(filepath.Join("out", string(rune('0'+i))))
		bodyData := []byte(filepath.Join("content", string(rune('0'+i))))

		req := Request{
			ID:       int64(i),
			Command:  CmdPut,
			ActionID: actionID,
			OutputID: outputID,
			BodySize: int64(len(bodyData)),
		}

		resp := handler.handlePut(t.Context(), req, bodyData)
		if resp.Err != "" {
			t.Fatalf("handlePut failed for item %d: %s", i, resp.Err)
		}
	}

	// Wait for async puts to complete
	handler.Wait()

	// Item 1 (hex: "6163742f31") should have been evicted from in-memory cache due to LRU (capacity = 2)
	item1Key := hex.EncodeToString([]byte("act/1"))
	if _, ok := handler.getMemory(item1Key); ok {
		t.Errorf("expected item 1 to be evicted from memory, but it was found")
	}

	// Items 2 and 3 should be in memory
	item2Key := hex.EncodeToString([]byte("act/2"))
	item3Key := hex.EncodeToString([]byte("act/3"))
	if _, ok := handler.getMemory(item2Key); !ok {
		t.Errorf("expected item 2 to be in memory")
	}
	if _, ok := handler.getMemory(item3Key); !ok {
		t.Errorf("expected item 3 to be in memory")
	}

	// Getting item 1 should fetch from KV and bring it back into memory
	getReq1 := Request{
		ID:       10,
		Command:  CmdGet,
		ActionID: []byte("act/1"),
	}
	getResp1 := handler.handleGet(t.Context(), getReq1)
	if getResp1.Miss || getResp1.Err != "" {
		t.Fatalf("expected get for item 1 to succeed from KV, got miss=%v err=%s", getResp1.Miss, getResp1.Err)
	}

	// Item 1 should now be back in memory
	if _, ok := handler.getMemory(item1Key); !ok {
		t.Errorf("expected item 1 to be brought into memory after get")
	}
}

type failingStorage struct {
	storage.Storage
}

func (f *failingStorage) Store(ctx context.Context, r io.Reader) (string, error) {
	return "", fmt.Errorf("simulated storage failure")
}

func (f *failingStorage) StoreAt(ctx context.Context, address string, r io.Reader) (bool, error) {
	return false, fmt.Errorf("simulated storage failure")
}

func TestHandler_StorageFailureDoesNotPoisonKV(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "build-cache")

	memoryKV := kv.NewMemoryKeyValueStore()
	badStorage := &failingStorage{Storage: storage.NewInMemoryStorage()}

	cfg := CacheConfig{
		CacheDir: cacheDir,
		KVStore:  memoryKV,
		Storage:  badStorage,
	}

	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	actionID := []byte("failing-storage-action")
	outputID := []byte("failing-storage-output")
	body := []byte("data")

	req := Request{
		ID:       1,
		Command:  CmdPut,
		ActionID: actionID,
		OutputID: outputID,
		BodySize: int64(len(body)),
	}

	// Put locally succeeds
	resp := handler.handlePut(context.Background(), req, body)
	if resp.Err != "" {
		t.Fatalf("handlePut failed locally: %s", resp.Err)
	}

	// Wait for background upload/KV put
	handler.Wait()

	// Verify that the failed storage put did not insert an empty-link entry in the KV store
	kvKey := fmt.Sprintf("go-build-cache:%x", actionID)
	val, _, err := memoryKV.Get(context.Background(), nil, kvKey)
	if err == nil && len(val) > 0 {
		t.Errorf("expected KV store to NOT contain record after storage failure, but found %s", string(val))
	}
}

func TestHandler_WriteTag(t *testing.T) {
	ctx := context.Background()
	d := discovery.NewInMemoryDiscovery()

	memGen := storage.NewInMemoryStorage()
	srvGen := storage.NewStorageServer(memGen)
	tsGen := httptest.NewServer(srvGen.Handler())
	defer tsGen.Close()

	memOther := storage.NewInMemoryStorage()
	srvOther := storage.NewStorageServer(memOther)
	tsOther := httptest.NewServer(srvOther.Handler())
	defer tsOther.Close()

	d.Register(ctx, discovery.ServiceRegistration{
		ID:        "storage-generated",
		Address:   tsGen.URL,
		Protocols: []string{"storage-v1", "batch-storage-v1"},
		Tags:      []string{"generated"},
	})
	d.Register(ctx, discovery.ServiceRegistration{
		ID:        "storage-other",
		Address:   tsOther.URL,
		Protocols: []string{"storage-v1", "batch-storage-v1"},
		Tags:      []string{"other"},
	})

	aggStorage := storage.NewAggregateClient(nil, d, 2, 100)

	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "build-cache")
	memoryKV := kv.NewMemoryKeyValueStore()

	// 1. Test default write tag (should default to "generated")
	cfg := CacheConfig{
		CacheDir: cacheDir,
		KVStore:  memoryKV,
		Storage:  aggStorage,
		// WriteTag empty -> defaults to "generated"
	}

	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	actionID := []byte("action-default-tag")
	outputID := []byte("output-default-tag")
	body := []byte("data for generated build cache")

	req := Request{
		ID:       1,
		Command:  CmdPut,
		ActionID: actionID,
		OutputID: outputID,
		BodySize: int64(len(body)),
	}

	resp := handler.handlePut(ctx, req, body)
	if resp.Err != "" {
		t.Fatalf("handlePut failed: %s", resp.Err)
	}

	handler.Wait()

	// Verify entry was written to KV and block to memGen (and NOT memOther)
	kvKey := fmt.Sprintf("go-build-cache:%x", actionID)
	val, _, err := memoryKV.Get(ctx, nil, kvKey)
	if err != nil || len(val) == 0 {
		t.Fatalf("expected KV store to contain record, got err: %v", err)
	}

	var entry ActionEntry
	if err := json.Unmarshal(val, &entry); err != nil {
		t.Fatalf("failed to unmarshal action entry: %v", err)
	}

	if !memGen.Has(ctx, entry.ContentLink.Address) {
		t.Errorf("expected block %s to be written to 'generated' storage", entry.ContentLink.Address)
	}
	if memOther.Has(ctx, entry.ContentLink.Address) {
		t.Errorf("expected block %s NOT to be written to 'other' storage", entry.ContentLink.Address)
	}

	// 2. Test explicit custom write tag "other"
	cfgCustom := CacheConfig{
		CacheDir: cacheDir,
		KVStore:  memoryKV,
		Storage:  aggStorage,
		WriteTag: "other",
	}

	handlerCustom, err := NewHandler(cfgCustom)
	if err != nil {
		t.Fatalf("NewHandler with custom tag failed: %v", err)
	}

	actionID2 := []byte("action-custom-tag")
	outputID2 := []byte("output-custom-tag")
	body2 := []byte("data for other tag build cache")

	req2 := Request{
		ID:       2,
		Command:  CmdPut,
		ActionID: actionID2,
		OutputID: outputID2,
		BodySize: int64(len(body2)),
	}

	resp2 := handlerCustom.handlePut(ctx, req2, body2)
	if resp2.Err != "" {
		t.Fatalf("handlePut failed: %s", resp2.Err)
	}

	handlerCustom.Wait()

	kvKey2 := fmt.Sprintf("go-build-cache:%x", actionID2)
	val2, _, err := memoryKV.Get(ctx, nil, kvKey2)
	if err != nil || len(val2) == 0 {
		t.Fatalf("expected KV store to contain record 2, got err: %v", err)
	}

	var entry2 ActionEntry
	if err := json.Unmarshal(val2, &entry2); err != nil {
		t.Fatalf("failed to unmarshal action entry 2: %v", err)
	}

	if !memOther.Has(ctx, entry2.ContentLink.Address) {
		t.Errorf("expected block %s to be written to 'other' storage", entry2.ContentLink.Address)
	}
	if memGen.Has(ctx, entry2.ContentLink.Address) {
		t.Errorf("expected block %s NOT to be written to 'generated' storage", entry2.ContentLink.Address)
	}
}
