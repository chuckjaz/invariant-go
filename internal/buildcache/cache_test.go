package buildcache

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"invariant/internal/content"
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
