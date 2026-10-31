package itp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tailscale.com/tstest/integration"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"

	"github.com/chuckjaz/invariant-go/internal/discovery"
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

func TestITPControlPlaneDynamicAdmission(t *testing.T) {
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

	cp := discovery.NewInMemoryDiscovery()

	ln, err := Listen(ctx, ListenOptions{
		DERPRegion:   reg,
		ControlPlane: cp,
		SyncInterval: 10 * time.Minute, // Long interval to verify on-demand sync in Accept()
	})
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/id", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("service-id-123"))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	// Register a client key on the control plane AFTER the listener has already started
	clientKey := key.NewNode()
	if err := cp.RegisterClient(ctx, clientKey.Public().String(), discovery.NodeIdentity{Name: "cli"}); err != nil {
		t.Fatalf("RegisterClient failed: %v", err)
	}

	tr := NewTransport(clientKey, "")
	defer tr.Close()
	client := &http.Client{Transport: tr}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ln.ITPAddr+"/id", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("expected dynamically authorized client to succeed immediately, got: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "service-id-123" {
		t.Fatalf("expected service-id-123, got %q", string(body))
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

func TestITPConcurrentMultiServerDial(t *testing.T) {
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

	const numServers = 4
	addrs := make([]string, numServers)
	for i := range numServers {
		ln, err := Listen(ctx, ListenOptions{
			DERPRegion: reg,
		})
		if err != nil {
			t.Fatalf("Listen %d failed: %v", i, err)
		}
		defer ln.Close()

		id := fmt.Sprintf("svc-%d", i)
		mux := http.NewServeMux()
		mux.HandleFunc("/id", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(id))
		})
		srv := &http.Server{Handler: mux}
		go srv.Serve(ln)
		defer srv.Close()

		addrs[i] = ln.ITPAddr
	}

	clientKey := key.NewNode()
	tr := NewTransport(clientKey, "")
	defer tr.Close()
	client := &http.Client{Transport: tr}

	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, numServers)

	for i, addr := range addrs {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/id", nil)
			if err != nil {
				errs <- err
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				errs <- err
				return
			}
			want := fmt.Sprintf("svc-%d", i)
			if string(body) != want {
				errs <- fmt.Errorf("got %q, want %q", string(body), want)
			}
		}(i, addr)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent request failed: %v", err)
	}

	elapsed := time.Since(start)
	if elapsed >= 900*time.Millisecond {
		t.Fatalf("concurrent dials took %v (expected < 900ms without 1s DERP retry delay)", elapsed)
	}
}

func TestITPServerAlsoDialingAsClient(t *testing.T) {
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

	// Service B (e.g. inv-names)
	pkB, err := LoadOrGenerateKey(filepath.Join(t.TempDir(), "b.key"))
	if err != nil {
		t.Fatalf("LoadOrGenerateKey B failed: %v", err)
	}
	lnB, err := Listen(ctx, ListenOptions{
		PrivateKey: pkB,
		DERPRegion: reg,
	})
	if err != nil {
		t.Fatalf("Listen B failed: %v", err)
	}
	defer lnB.Close()

	muxB := http.NewServeMux()
	muxB.HandleFunc("/id", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("service-b"))
	})
	srvB := &http.Server{Handler: muxB}
	go srvB.Serve(lnB)
	defer srvB.Close()

	// Service A (e.g. inv-finder / inv-storage) listens on ITP and then dials Service B
	// using its own private key pkA.Private before Client C dials Service A.
	pkA, err := LoadOrGenerateKey(filepath.Join(t.TempDir(), "a.key"))
	if err != nil {
		t.Fatalf("LoadOrGenerateKey A failed: %v", err)
	}
	lnA, err := Listen(ctx, ListenOptions{
		PrivateKey: pkA,
		DERPRegion: reg,
	})
	if err != nil {
		t.Fatalf("Listen A failed: %v", err)
	}
	defer lnA.Close()

	muxA := http.NewServeMux()
	muxA.HandleFunc("/id", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("service-a"))
	})
	srvA := &http.Server{Handler: muxA}
	go srvA.Serve(lnA)
	defer srvA.Close()

	// Service A dials Service B using pkA.Private
	clientA := NewHTTPClient(pkA.Private, "")
	defer clientA.Transport.(*Transport).Close()

	reqAB, err := http.NewRequestWithContext(ctx, http.MethodGet, lnB.ITPAddr+"/id", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext A->B failed: %v", err)
	}
	respAB, err := clientA.Do(reqAB)
	if err != nil {
		t.Fatalf("Service A dialing Service B failed: %v", err)
	}
	bodyAB, _ := io.ReadAll(respAB.Body)
	respAB.Body.Close()
	if string(bodyAB) != "service-b" {
		t.Fatalf("expected service-b, got %q", string(bodyAB))
	}

	// Now Client C (e.g. invariant status) dials Service A.
	// Previously, Service A's outbound clientA connection replaced Service A's server on DERP
	// because both used pkA.Private, causing Client C's dial to Service A to time out.
	clientCKey := key.NewNode()
	clientC := NewHTTPClient(clientCKey, "")
	defer clientC.Transport.(*Transport).Close()

	dialCtx, dialCancel := context.WithTimeout(ctx, 3*time.Second)
	defer dialCancel()

	reqCA, err := http.NewRequestWithContext(dialCtx, http.MethodGet, lnA.ITPAddr+"/id", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext C->A failed: %v", err)
	}
	respCA, err := clientC.Do(reqCA)
	if err != nil {
		t.Fatalf("Client C dialing Service A failed: %v", err)
	}
	bodyCA, _ := io.ReadAll(respCA.Body)
	respCA.Body.Close()
	if string(bodyCA) != "service-a" {
		t.Fatalf("expected service-a, got %q", string(bodyCA))
	}
}
