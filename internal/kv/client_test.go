package kv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_HTTPErrors(t *testing.T) {
	ctx := context.Background()

	// 1. Server returning 500 Internal Server Error
	tsErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer tsErr.Close()

	clientErr := NewClient(tsErr.URL, nil)

	_, err := clientErr.StartTransaction(ctx, false)
	if err == nil {
		t.Errorf("Expected StartTransaction error, got nil")
	}

	err = clientErr.CommitTransaction(ctx, 1)
	if err == nil {
		t.Errorf("Expected CommitTransaction error, got nil")
	}

	err = clientErr.AbortTransaction(ctx, 1)
	if err == nil {
		t.Errorf("Expected AbortTransaction error, got nil")
	}

	_, err = clientErr.CreateCheckpoint(ctx)
	if err == nil {
		t.Errorf("Expected CreateCheckpoint error, got nil")
	}

	_, err = clientErr.Put(ctx, nil, "k", []byte("v"))
	if err == nil {
		t.Errorf("Expected Put error, got nil")
	}

	_, _, err = clientErr.Get(ctx, nil, "k")
	if err == nil {
		t.Errorf("Expected Get error, got nil")
	}

	_, err = clientErr.BatchPut(ctx, nil, map[string][]byte{"k": []byte("v")})
	if err == nil {
		t.Errorf("Expected BatchPut error, got nil")
	}

	_, err = clientErr.BatchGet(ctx, nil, []string{"k"})
	if err == nil {
		t.Errorf("Expected BatchGet error, got nil")
	}

	_, err = clientErr.GetHistory(ctx, nil, "k", 0, 100, 10)
	if err == nil {
		t.Errorf("Expected GetHistory error, got nil")
	}

	_, err = clientErr.BatchGetHistory(ctx, nil, []string{"k"}, 0, 100, 10)
	if err == nil {
		t.Errorf("Expected BatchGetHistory error, got nil")
	}

	// 2. Server returning valid 200 but bad payloads
	tsBadPayload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("bad json payload{"))
	}))
	defer tsBadPayload.Close()

	clientBad := NewClient(tsBadPayload.URL, nil)

	_, err = clientBad.StartTransaction(ctx, false)
	if err == nil {
		t.Errorf("Expected StartTransaction JSON decode error, got nil")
	}

	_, err = clientBad.CreateCheckpoint(ctx)
	if err == nil {
		t.Errorf("Expected CreateCheckpoint JSON decode error, got nil")
	}

	// 3. Put and Get with missing/invalid header
	tsMissingHeader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Transaction-ID", "invalid-uint")
		w.WriteHeader(http.StatusOK)
	}))
	defer tsMissingHeader.Close()

	clientMissing := NewClient(tsMissingHeader.URL, nil)
	_, err = clientMissing.Put(ctx, nil, "k", []byte("v"))
	if err == nil {
		t.Errorf("Expected Put invalid header error, got nil")
	}

	_, _, err = clientMissing.Get(ctx, nil, "k")
	if err == nil {
		t.Errorf("Expected Get invalid header error, got nil")
	}

	// 4. BatchGet with invalid multipart format
	tsBadMultipart := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/form-data; boundary=bad-boundary")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("invalid multipart body"))
	}))
	defer tsBadMultipart.Close()

	clientBadMultipart := NewClient(tsBadMultipart.URL, nil)
	_, err = clientBadMultipart.BatchGet(ctx, nil, []string{"k"})
	if err == nil {
		t.Errorf("Expected BatchGet multipart decode error, got nil")
	}
}

func TestClient_NetworkAndUrlErrors(t *testing.T) {
	ctx := context.Background()

	// 1. Invalid URL (triggers NewRequestWithContext error)
	clientBadURL := NewClient("http://invalid url with spaces", nil)

	if _, err := clientBadURL.StartTransaction(ctx, false); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if err := clientBadURL.CommitTransaction(ctx, 1); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if err := clientBadURL.AbortTransaction(ctx, 1); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, err := clientBadURL.CreateCheckpoint(ctx); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, err := clientBadURL.Put(ctx, nil, "k", []byte("v")); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, _, err := clientBadURL.Get(ctx, nil, "k"); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, err := clientBadURL.BatchPut(ctx, nil, map[string][]byte{"k": []byte("v")}); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, err := clientBadURL.BatchGet(ctx, nil, []string{"k"}); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, err := clientBadURL.GetHistory(ctx, nil, "k", 0, 100, 10); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, err := clientBadURL.BatchGetHistory(ctx, nil, []string{"k"}, 0, 100, 10); err == nil {
		t.Errorf("Expected error, got nil")
	}

	// 2. Offline / Connection error (triggers httpClient.Do error)
	clientOffline := NewClient("http://nonexistent.domain.invalid", nil)

	if _, err := clientOffline.StartTransaction(ctx, false); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if err := clientOffline.CommitTransaction(ctx, 1); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if err := clientOffline.AbortTransaction(ctx, 1); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, err := clientOffline.CreateCheckpoint(ctx); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, err := clientOffline.Put(ctx, nil, "k", []byte("v")); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, _, err := clientOffline.Get(ctx, nil, "k"); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, err := clientOffline.BatchPut(ctx, nil, map[string][]byte{"k": []byte("v")}); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, err := clientOffline.BatchGet(ctx, nil, []string{"k"}); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, err := clientOffline.GetHistory(ctx, nil, "k", 0, 100, 10); err == nil {
		t.Errorf("Expected error, got nil")
	}
	if _, err := clientOffline.BatchGetHistory(ctx, nil, []string{"k"}, 0, 100, 10); err == nil {
		t.Errorf("Expected error, got nil")
	}
}

func TestServer_TransactionHeaders(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryKeyValueStore()
	server := NewServer(store)
	defer server.Close()

	ts := httptest.NewServer(server)
	defer ts.Close()

	// 1. POST /tx/start should return X-Transaction-ID header
	resp, err := http.Post(ts.URL+"/tx/start", "application/json", nil)
	if err != nil {
		t.Fatalf("StartTransaction failed: %v", err)
	}
	defer resp.Body.Close()

	txHeader := resp.Header.Get("X-Transaction-ID")
	if txHeader == "" {
		t.Errorf("Expected X-Transaction-ID header on /tx/start, got empty string")
	}

	// 2. POST /tx/checkpoint should return X-Transaction-ID header
	respChk, err := http.Post(ts.URL+"/tx/checkpoint", "application/json", nil)
	if err != nil {
		t.Fatalf("CreateCheckpoint failed: %v", err)
	}
	defer respChk.Body.Close()

	chkHeader := respChk.Header.Get("X-Transaction-ID")
	if chkHeader == "" {
		t.Errorf("Expected X-Transaction-ID header on /tx/checkpoint, got empty string")
	}
	_ = ctx
}
