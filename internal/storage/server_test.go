package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStorageServer(t *testing.T) {
	storage := NewInMemoryStorage()
	server := NewStorageServer(storage)
	ts := httptest.NewServer(server)
	defer ts.Close()

	// 1. GET /id
	res, err := http.Get(ts.URL + "/id")
	if err != nil {
		t.Fatal(err)
	}
	idBytes, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if len(idBytes) != 64 {
		t.Errorf("expected 64 char hex string for /id, got length %d", len(idBytes))
	}

	// 2. POST /storage/
	content := []byte("hello world")
	res, err = http.Post(ts.URL+"/storage/", "text/plain", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	addressBytes, _ := io.ReadAll(res.Body)
	res.Body.Close()
	address := string(addressBytes)

	hash1 := sha256.Sum256(content)
	expectedHash := hex.EncodeToString(hash1[:])
	if address != expectedHash {
		t.Errorf("expected hash %s, got %s", expectedHash, address)
	}

	// 3. GET /storage/:address
	res, err = http.Get(ts.URL + "/storage/" + address)
	if err != nil {
		t.Fatal(err)
	}
	if res.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("expected application/octet-stream, got %s", res.Header.Get("Content-Type"))
	}
	if res.Header.Get("ETag") != expectedHash {
		t.Errorf("expected ETag %s, got %s", expectedHash, res.Header.Get("ETag"))
	}
	valBytes, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(valBytes) != string(content) {
		t.Errorf("expected %s, got %s", content, valBytes)
	}

	// 4. HEAD /storage/:address
	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/storage/"+address, nil)
	client := &http.Client{}
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}
	if res.Header.Get("Content-Length") != "11" {
		t.Errorf("expected Content-Length 11, got %s", res.Header.Get("Content-Length"))
	}

	// 5. PUT /storage/:address
	newContent := []byte("new content")
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/storage/"+address, bytes.NewReader(newContent))
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", res.StatusCode)
	}

	hash2 := sha256.Sum256(newContent)
	newExpectedHash := hex.EncodeToString(hash2[:])
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/storage/"+newExpectedHash, bytes.NewReader(newContent))
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}
	if string(body) != newExpectedHash {
		t.Errorf("expected %s, got %s", newExpectedHash, string(body))
	}

	// 6. Test /storage/fetch optional endpoints
	res, _ = http.Post(ts.URL+"/storage/fetch", "application/json", strings.NewReader(`{}`))
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 Not Found, got %d", res.StatusCode)
	}

	req, _ = http.NewRequest("HEAD", ts.URL+"/storage/fetch", nil)
	res, _ = client.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 Not Found, got %d", res.StatusCode)
	}
}
