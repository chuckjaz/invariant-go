package httputil

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewDiagnosticClient_NilBase(t *testing.T) {
	client := NewDiagnosticClient(nil)
	if client == nil {
		t.Fatalf("Expected non-nil client")
	}

	transport, ok := client.Transport.(*DiagnosticTransport)
	if !ok {
		t.Fatalf("Expected DiagnosticTransport, got %T", client.Transport)
	}
	if transport.Transport != http.DefaultTransport {
		t.Errorf("Expected inner transport to be http.DefaultTransport")
	}
}

func TestNewDiagnosticClient_CustomBase(t *testing.T) {
	customTransport := &http.Transport{}
	customClient := &http.Client{Transport: customTransport}

	client := NewDiagnosticClient(customClient)
	if client == nil {
		t.Fatalf("Expected non-nil client")
	}

	transport, ok := client.Transport.(*DiagnosticTransport)
	if !ok {
		t.Fatalf("Expected DiagnosticTransport, got %T", client.Transport)
	}
	if transport.Transport != customTransport {
		t.Errorf("Expected inner transport to match customTransport")
	}
}

func TestDiagnosticClient_RequestSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := NewDiagnosticClient(server.Client())
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("Expected body 'ok', got %q", string(body))
	}
}

func TestDiagnosticClient_RequestError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	serverURL := server.URL
	server.Close() // Close immediately to trigger connection refused error

	client := NewDiagnosticClient(nil)
	_, err := client.Get(serverURL)
	if err == nil {
		t.Errorf("Expected connection error on closed server, got nil")
	}
}

func TestLogDiagnostic(t *testing.T) {
	// Exercise logDiagnostic
	logDiagnostic("test diagnostic message\n")
}
