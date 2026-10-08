package itp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tailscale.com/tstest/integration"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

func TestKeyPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "test.key")

	pk1, err := LoadOrGenerateKey(keyPath)
	if err != nil {
		t.Fatalf("LoadOrGenerateKey failed: %v", err)
	}
	if pk1.Private.IsZero() {
		t.Fatal("expected non-zero private key")
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file not created: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 permissions, got %v", info.Mode().Perm())
	}

	// Load again
	pk2, err := LoadOrGenerateKey(keyPath)
	if err != nil {
		t.Fatalf("LoadOrGenerateKey reload failed: %v", err)
	}
	if !pk1.Private.Equal(pk2.Private) {
		t.Fatal("expected loaded key to equal generated key")
	}
	if !pk1.Public.PresharedKey.Equal(pk2.Public.PresharedKey) {
		t.Fatal("expected loaded pre-shared key to equal generated pre-shared key")
	}
}

func TestITPListenerAndTransport(t *testing.T) {
	t.Parallel()

	// Hermetic in-memory DERP server for offline unit testing
	logf := func(format string, args ...any) {
		t.Logf(format, args...)
	}
	dm := integration.RunDERPAndSTUN(t, logger.Logf(logf), "127.0.0.1")
	reg := dm.Regions[1]
	if reg == nil {
		t.Fatal("expected region 1 in DERPMap")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ln, err := Listen(ctx, ListenOptions{
		DERPRegion: reg,
	})
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	if !ln.IsTailcat {
		t.Fatal("expected IsTailcat to be true")
	}
	if ln.ITPAddr == "" {
		t.Fatal("expected non-empty ITPAddr")
	}

	// Serve HTTP on ITP listener
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		pk, ok := ln.PeerKey(r.RemoteAddr)
		if !ok || pk.IsZero() {
			http.Error(w, "missing peer key", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "Hello ITP from %s", pk.String())
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	// Client transport
	clientKey := key.NewNode()
	transport := NewTransport(clientKey, "")
	defer transport.Close()

	client := &http.Client{Transport: transport}

	reqURL := fmt.Sprintf("%s/hello", ln.ITPAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext failed: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body failed: %v", err)
	}

	expectedPrefix := fmt.Sprintf("Hello ITP from %s", clientKey.Public().String())
	if string(body) != expectedPrefix {
		t.Fatalf("expected body %q, got %q", expectedPrefix, string(body))
	}
}

func TestITPAdmissionControl(t *testing.T) {
	t.Parallel()

	logf := func(format string, args ...any) {
		t.Logf(format, args...)
	}
	dm := integration.RunDERPAndSTUN(t, logger.Logf(logf), "127.0.0.1")
	reg := dm.Regions[1]
	if reg == nil {
		t.Fatal("expected region 1 in DERPMap")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	allowedKey := key.NewNode()
	disallowedKey := key.NewNode()

	ln, err := Listen(ctx, ListenOptions{
		DERPRegion:     reg,
		AllowedClients: []key.NodePublic{allowedKey.Public()},
	})
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong"))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	// 1. Allowed client succeeds
	trAllowed := NewTransport(allowedKey, "")
	defer trAllowed.Close()
	cAllowed := &http.Client{Transport: trAllowed}

	reqAllowed, _ := http.NewRequestWithContext(ctx, http.MethodGet, ln.ITPAddr+"/ping", nil)
	respAllowed, err := cAllowed.Do(reqAllowed)
	if err != nil {
		t.Fatalf("allowed client failed: %v", err)
	}
	respAllowed.Body.Close()
	if respAllowed.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", respAllowed.StatusCode)
	}

	// 2. Disallowed client fails
	trDisallowed := NewTransport(disallowedKey, "")
	defer trDisallowed.Close()
	cDisallowed := &http.Client{Transport: trDisallowed}

	shortCtx, shortCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer shortCancel()

	reqDisallowed, _ := http.NewRequestWithContext(shortCtx, http.MethodGet, ln.ITPAddr+"/ping", nil)
	_, err = cDisallowed.Do(reqDisallowed)
	if err == nil {
		t.Fatal("expected disallowed client to fail, got nil error")
	}
}

func TestIPListener(t *testing.T) {
	ctx := context.Background()
	ln, err := Listen(ctx, ListenOptions{
		ListenIP: true,
		Host:     "127.0.0.1",
		Port:     0,
	})
	if err != nil {
		t.Fatalf("ListenIP failed: %v", err)
	}
	defer ln.Close()

	if ln.IsTailcat {
		t.Fatal("expected IsTailcat to be false for IP listener")
	}
	if ln.Addr() == nil {
		t.Fatal("expected non-nil Addr for IP listener")
	}
}
