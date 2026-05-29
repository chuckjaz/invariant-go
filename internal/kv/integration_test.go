package kv

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestIntegration_ClientServerAll(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")

	// 1. Create concrete KeyValueStore (use large merge threshold to make history checks synchronous)
	s, err := NewFileKeyValueStore(ctx, slotClient, "btree-slot-integ", nil, "journal-slot-integ", nil, storeClient, t.TempDir(), 1000000, 1000, 1000, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// 2. Start HTTP test server
	server := NewServer(s)
	ts := httptest.NewServer(server)
	defer ts.Close()

	// 3. Create HTTP client
	client := NewClient(ts.URL, nil)

	// --- 4. Test Basic PUT and GET (Implicit Transactions) ---
	seq, err := client.Put(ctx, nil, "k1", []byte("v1"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if seq == 0 {
		t.Errorf("Expected sequence > 0, got 0")
	}

	val, readSeq, err := client.Get(ctx, nil, "k1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "v1" {
		t.Errorf("Expected 'v1', got %s", string(val))
	}
	if readSeq != seq {
		t.Errorf("Expected transaction ID %d, got %d", seq, readSeq)
	}

	// GET non-existent key
	_, _, err = client.Get(ctx, nil, "nonexistent")
	if err == nil {
		t.Errorf("Expected error for nonexistent key, got nil")
	}

	// --- 5. Test Transaction isolation and Commit ---
	txID, err := client.StartTransaction(ctx, false)
	if err != nil {
		t.Fatalf("StartTransaction failed: %v", err)
	}

	_, err = client.Put(ctx, &txID, "k2", []byte("v2"))
	if err != nil {
		t.Fatalf("Put inside transaction failed: %v", err)
	}

	// Invisible to other transactions
	_, _, err = client.Get(ctx, nil, "k2")
	if err == nil {
		t.Errorf("Uncommitted write was visible to implicit checkpoint transaction")
	}

	// Visible inside same transaction
	txVal, _, err := client.Get(ctx, &txID, "k2")
	if err != nil || string(txVal) != "v2" {
		t.Errorf("Uncommitted write not visible inside transaction: %v, val: %s", err, string(txVal))
	}

	// Commit transaction
	err = client.CommitTransaction(ctx, txID)
	if err != nil {
		t.Fatalf("CommitTransaction failed: %v", err)
	}

	// Visible now
	val2, _, err := client.Get(ctx, nil, "k2")
	if err != nil || string(val2) != "v2" {
		t.Errorf("Committed write not visible: %v, val: %s", err, string(val2))
	}

	// --- 6. Test Transaction Abort ---
	txID2, err := client.StartTransaction(ctx, false)
	if err != nil {
		t.Fatalf("StartTransaction failed: %v", err)
	}

	_, err = client.Put(ctx, &txID2, "k3", []byte("v3"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	err = client.AbortTransaction(ctx, txID2)
	if err != nil {
		t.Fatalf("AbortTransaction failed: %v", err)
	}

	_, _, err = client.Get(ctx, nil, "k3")
	if err == nil {
		t.Errorf("Aborted write was visible")
	}

	// --- 7. Test Checkpoints ---
	chkID, err := client.CreateCheckpoint(ctx)
	if err != nil {
		t.Fatalf("CreateCheckpoint failed: %v", err)
	}
	if chkID == 0 {
		t.Errorf("Expected checkpoint ID > 0")
	}

	// --- 8. Test Batch Put and Get ---
	batchKVs := map[string][]byte{
		"bk1": []byte("bv1"),
		"bk2": []byte("bv2"),
	}
	batchSeq, err := client.BatchPut(ctx, nil, batchKVs)
	if err != nil {
		t.Fatalf("BatchPut failed: %v", err)
	}
	if batchSeq == 0 {
		t.Errorf("Expected batch transaction ID > 0")
	}

	batchRead, err := client.BatchGet(ctx, nil, []string{"bk1", "bk2", "nonexistent"})
	if err != nil {
		t.Fatalf("BatchGet failed: %v", err)
	}
	if string(batchRead["bk1"].Value) != "bv1" || batchRead["bk1"].TransactionID != batchSeq {
		t.Errorf("Invalid bk1 read: %+v", batchRead["bk1"])
	}
	if string(batchRead["bk2"].Value) != "bv2" || batchRead["bk2"].TransactionID != batchSeq {
		t.Errorf("Invalid bk2 read: %+v", batchRead["bk2"])
	}
	if _, ok := batchRead["nonexistent"]; ok {
		t.Errorf("Non-existent key returned in BatchGet results")
	}

	// --- 9. Test History and Batch History ---
	// Update k1 multiple times to create history (since threshold is 1000, these stay in pending)
	for i := 2; i <= 5; i++ {
		_, err = client.Put(ctx, nil, "k1", []byte(fmt.Sprintf("v1-%d", i)))
		if err != nil {
			t.Fatalf("Put update failed: %v", err)
		}
	}

	historyPage, err := client.GetHistory(ctx, nil, "k1", 0, ^uint64(0), 3)
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}
	fmt.Printf("DEBUG: GetHistory(k1) values: %+v\n", historyPage.Values)
	if len(historyPage.Values) != 3 {
		t.Errorf("Expected 3 history items, got %d", len(historyPage.Values))
	}
	if string(historyPage.Values[0].Value) != "v1-5" {
		t.Errorf("Expected latest version to be v1-5, got %s", string(historyPage.Values[0].Value))
	}
	if !historyPage.HasMore {
		t.Errorf("Expected HasMore to be true")
	}

	// Batch history
	batchHistory, err := client.BatchGetHistory(ctx, nil, []string{"k1", "k2"}, 0, ^uint64(0), 10)
	if err != nil {
		t.Fatalf("BatchGetHistory failed: %v", err)
	}
	fmt.Printf("DEBUG: BatchGetHistory keys in map: %v\n", len(batchHistory))
	for k, v := range batchHistory {
		fmt.Printf("DEBUG: key: %s, values: %+v\n", k, v.Values)
	}
	if len(batchHistory["k1"].Values) != 5 {
		t.Errorf("Expected 5 history versions for k1, got %d", len(batchHistory["k1"].Values))
	}
	if len(batchHistory["k2"].Values) != 1 {
		t.Errorf("Expected 1 history version for k2, got %d", len(batchHistory["k2"].Values))
	}

	// --- 10. Test MVCC Conflict Detection (Sequential Transactions) ---
	seqTx1, err := client.StartTransaction(ctx, true)
	if err != nil {
		t.Fatalf("Failed to start seqTx1: %v", err)
	}
	seqTx2, err := client.StartTransaction(ctx, true)
	if err != nil {
		t.Fatalf("Failed to start seqTx2: %v", err)
	}

	_, _, err = client.Get(ctx, &seqTx1, "k1")
	if err != nil {
		t.Fatalf("seqTx1 read failed: %v", err)
	}
	_, _, err = client.Get(ctx, &seqTx2, "k1")
	if err != nil {
		t.Fatalf("seqTx2 read failed: %v", err)
	}

	_, err = client.Put(ctx, &seqTx1, "k1", []byte("seqTx1-update"))
	if err != nil {
		t.Fatalf("seqTx1 write failed: %v", err)
	}
	err = client.CommitTransaction(ctx, seqTx1)
	if err != nil {
		t.Fatalf("seqTx1 commit failed: %v", err)
	}

	_, err = client.Put(ctx, &seqTx2, "k2", []byte("seqTx2-update"))
	if err != nil {
		t.Fatalf("seqTx2 write failed: %v", err)
	}

	// seqTx2 commit should conflict and fail
	err = client.CommitTransaction(ctx, seqTx2)
	if err == nil {
		t.Errorf("Expected seqTx2 commit to conflict and abort, but it succeeded")
	}
}

func TestIntegration_ServerBadRequests(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")

	s, err := NewFileKeyValueStore(ctx, slotClient, "btree-slot-err", nil, "journal-slot-err", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	ts := httptest.NewServer(NewServer(s))
	defer ts.Close()

	client := ts.Client()

	// 1. GET /get with missing key
	resp, _ := client.Get(ts.URL + "/get")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request, got %d", resp.StatusCode)
	}

	// 2. POST /put with missing key
	resp, _ = client.Post(ts.URL+"/put", "application/octet-stream", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request, got %d", resp.StatusCode)
	}

	// 3. POST /tx/commit with missing/invalid tx param
	resp, _ = client.Post(ts.URL+"/tx/commit?tx=invalid", "application/json", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request, got %d", resp.StatusCode)
	}

	// 4. POST /tx/abort with missing/invalid tx param
	resp, _ = client.Post(ts.URL+"/tx/abort?tx=invalid", "application/json", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request, got %d", resp.StatusCode)
	}

	// 5. POST /batch_put with invalid multipart data
	resp, _ = client.Post(ts.URL+"/batch_put", "multipart/form-data; boundary=invalid", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request, got %d", resp.StatusCode)
	}

	// 6. POST /batch_get with invalid JSON body
	resp, _ = client.Post(ts.URL+"/batch_get", "application/json", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request, got %d", resp.StatusCode)
	}

	// 7. POST /batch_history with invalid JSON body
	resp, _ = client.Post(ts.URL+"/batch_history", "application/json", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request, got %d", resp.StatusCode)
	}

	// 8. GET /get with invalid tx (should fall back to implicit tx and return 404)
	resp, _ = client.Get(ts.URL + "/get?key=nonexistent&tx=invalid")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected 404, got %d", resp.StatusCode)
	}

	// 9. POST /put with invalid tx (should fall back to implicit tx and return 200)
	resp, _ = client.Post(ts.URL+"/put?key=k&tx=invalid", "application/octet-stream", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	// 10. GET /history with invalid min, max, limit
	resp, _ = client.Get(ts.URL + "/history?key=k&min=invalid&max=invalid&limit=invalid")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	// 11. GET /history with invalid tx
	resp, _ = client.Get(ts.URL + "/history?key=k&tx=invalid")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	// 12. POST /batch_get with invalid tx
	resp, _ = client.Post(ts.URL+"/batch_get?tx=invalid", "application/json", strings.NewReader("[]"))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	// 13. POST /batch_history with invalid tx
	resp, _ = client.Post(ts.URL+"/batch_history?tx=invalid", "application/json", strings.NewReader("[]"))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}
}

type mockKeyValueStore struct {
	putFunc        func(ctx context.Context, txID *uint64, key string, value []byte) (uint64, error)
	getFunc        func(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error)
	getHistoryFunc func(ctx context.Context, txID *uint64, key string, minTxID uint64, maxTxID uint64, pageSize int) (HistoryPage, error)
}

func (m *mockKeyValueStore) StartTransaction(ctx context.Context, sequential bool) (uint64, error) {
	return 1, nil
}
func (m *mockKeyValueStore) CommitTransaction(ctx context.Context, txID uint64) error {
	return nil
}
func (m *mockKeyValueStore) AbortTransaction(ctx context.Context, txID uint64) error {
	return nil
}
func (m *mockKeyValueStore) CreateCheckpoint(ctx context.Context) (uint64, error) {
	return 1, nil
}
func (m *mockKeyValueStore) Put(ctx context.Context, txID *uint64, key string, value []byte) (uint64, error) {
	if m.putFunc != nil {
		return m.putFunc(ctx, txID, key, value)
	}
	return 10, nil
}
func (m *mockKeyValueStore) Get(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, txID, key)
	}
	return []byte("mockval"), 20, nil
}
func (m *mockKeyValueStore) GetHistory(ctx context.Context, txID *uint64, key string, minTxID uint64, maxTxID uint64, pageSize int) (HistoryPage, error) {
	if m.getHistoryFunc != nil {
		return m.getHistoryFunc(ctx, txID, key, minTxID, maxTxID, pageSize)
	}
	return HistoryPage{
		Values: []ValueWithTransaction{
			{Value: []byte("hist1"), TransactionID: 30},
		},
		HasMore: true,
	}, nil
}

func TestIntegration_FallbackAndErrors(t *testing.T) {
	ctx := context.Background()
	mockStore := &mockKeyValueStore{}
	server := NewServer(mockStore)
	ts := httptest.NewServer(server)
	defer ts.Close()

	client := NewClient(ts.URL, nil)

	// Test 1: BatchPut fallback
	kvs := map[string][]byte{
		"fbk1": []byte("fbv1"),
		"fbk2": []byte("fbv2"),
	}
	seq, err := client.BatchPut(ctx, nil, kvs)
	if err != nil {
		t.Fatalf("BatchPut fallback failed: %v", err)
	}
	if seq != 10 {
		t.Errorf("Expected sequence 10 from mock, got %d", seq)
	}

	// Test 2: BatchGet fallback
	results, err := client.BatchGet(ctx, nil, []string{"fbk1", "fbk2"})
	if err != nil {
		t.Fatalf("BatchGet fallback failed: %v", err)
	}
	if string(results["fbk1"].Value) != "mockval" || results["fbk1"].TransactionID != 20 {
		t.Errorf("Expected mock values, got %+v", results["fbk1"])
	}

	// Test 3: BatchGetHistory fallback
	histResults, err := client.BatchGetHistory(ctx, nil, []string{"fbk1"}, 0, 100, 10)
	if err != nil {
		t.Fatalf("BatchGetHistory fallback failed: %v", err)
	}
	page := histResults["fbk1"]
	if len(page.Values) != 1 || string(page.Values[0].Value) != "hist1" || page.Values[0].TransactionID != 30 {
		t.Errorf("Expected mock history page, got %+v", page)
	}
	if !page.HasMore {
		t.Errorf("Expected HasMore to be true")
	}

	// Test 4: /history missing key error
	c := ts.Client()
	resp, _ := c.Get(ts.URL + "/history")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request for missing key, got %d", resp.StatusCode)
	}

	// Test 5: client commit/abort non-existent transaction
	concreteStoreClient := storage.NewInMemoryStorage()
	concreteSlotClient := slots.NewMemorySlots("test-slot")
	concreteStore, err := NewFileKeyValueStore(ctx, concreteSlotClient, "btree-slot-err2", nil, "journal-slot-err2", nil, concreteStoreClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer concreteStore.Close()

	tsConcrete := httptest.NewServer(NewServer(concreteStore))
	defer tsConcrete.Close()
	clientConcrete := NewClient(tsConcrete.URL, nil)

	err = clientConcrete.CommitTransaction(ctx, 99999)
	if err == nil {
		t.Errorf("Expected commit on invalid tx to fail, got nil")
	}
	err = clientConcrete.AbortTransaction(ctx, 99999)
	if err == nil {
		t.Errorf("Expected abort on invalid tx to fail, got nil")
	}

	// Test 6: Server handlers error response paths using mock error store
	mockErrorStore := &mockKeyValueStoreWithError{}
	serverErr := NewServer(mockErrorStore)
	tsErr := httptest.NewServer(serverErr)
	defer tsErr.Close()

	clientErr := NewClient(tsErr.URL, nil)

	cErr := tsErr.Client()
	respErr, _ := cErr.Post(tsErr.URL+"/put?key=k", "application/octet-stream", nil)
	if respErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected 500, got %d", respErr.StatusCode)
	}

	respErr, _ = cErr.Get(tsErr.URL + "/get?key=k")
	if respErr.StatusCode != http.StatusNotFound {
		t.Errorf("Expected 404, got %d", respErr.StatusCode)
	}

	respErr, _ = cErr.Get(tsErr.URL + "/history?key=k")
	if respErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected 500, got %d", respErr.StatusCode)
	}

	_, err = clientErr.BatchPut(ctx, nil, map[string][]byte{"k": []byte("v")})
	if err == nil {
		t.Errorf("Expected BatchPut error, got nil")
	}

	_, err = clientErr.BatchGet(ctx, nil, []string{"k"})
	if err == nil {
		t.Errorf("Expected BatchGet error, got nil")
	}

	_, err = clientErr.BatchGetHistory(ctx, nil, []string{"k"}, 0, 100, 10)
	if err == nil {
		t.Errorf("Expected BatchGetHistory error, got nil")
	}

	_, err = clientErr.StartTransaction(ctx, false)
	if err == nil {
		t.Errorf("Expected StartTransaction error, got nil")
	}

	_, err = clientErr.CreateCheckpoint(ctx)
	if err == nil {
		t.Errorf("Expected CreateCheckpoint error, got nil")
	}
}

type mockKeyValueStoreWithError struct {
	mockKeyValueStore
}

func (m *mockKeyValueStoreWithError) StartTransaction(ctx context.Context, sequential bool) (uint64, error) {
	return 0, fmt.Errorf("error start tx")
}
func (m *mockKeyValueStoreWithError) CreateCheckpoint(ctx context.Context) (uint64, error) {
	return 0, fmt.Errorf("error checkpoint")
}
func (m *mockKeyValueStoreWithError) Put(ctx context.Context, txID *uint64, key string, value []byte) (uint64, error) {
	return 0, fmt.Errorf("error put")
}
func (m *mockKeyValueStoreWithError) Get(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error) {
	return nil, 0, fmt.Errorf("error get")
}
func (m *mockKeyValueStoreWithError) GetHistory(ctx context.Context, txID *uint64, key string, minTxID uint64, maxTxID uint64, pageSize int) (HistoryPage, error) {
	return HistoryPage{}, fmt.Errorf("error get history")
}
func (m *mockKeyValueStoreWithError) BatchPut(ctx context.Context, txID *uint64, kvs map[string][]byte) (uint64, error) {
	return 0, fmt.Errorf("error batch put")
}
func (m *mockKeyValueStoreWithError) BatchGet(ctx context.Context, txID *uint64, keys []string) (map[string]ValueWithTransaction, error) {
	return nil, fmt.Errorf("error batch get")
}
func (m *mockKeyValueStoreWithError) BatchGetHistory(ctx context.Context, txID *uint64, keys []string, minTxID uint64, maxTxID uint64, pageSize int) (map[string]HistoryPage, error) {
	return nil, fmt.Errorf("error batch get history")
}
