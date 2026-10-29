package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/chuckjaz/invariant-go/internal/config"
	"github.com/chuckjaz/invariant-go/internal/discovery"
)

func TestFormatStatusAddress(t *testing.T) {
	itpAddr := "itp://tcpGFwWCAyigmGfEXiFjhAdNhFmAQJA7TjYyXS1r89YkeKbSAGOGFrWCBDb9P064YswBhbl8uQt8WHQ"
	httpAddr := "http://127.0.0.1:4000"

	if got := formatStatusAddress(itpAddr, false); got != "<itp>" {
		t.Errorf("formatStatusAddress(itpAddr, false) = %q, want %q", got, "<itp>")
	}
	if got := formatStatusAddress(itpAddr, true); got != itpAddr {
		t.Errorf("formatStatusAddress(itpAddr, true) = %q, want %q", got, itpAddr)
	}
	if got := formatStatusAddress(httpAddr, false); got != httpAddr {
		t.Errorf("formatStatusAddress(httpAddr, false) = %q, want %q", got, httpAddr)
	}
	if got := formatStatusAddress(httpAddr, true); got != httpAddr {
		t.Errorf("formatStatusAddress(httpAddr, true) = %q, want %q", got, httpAddr)
	}
}

func TestRunStatusVerboseFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	imd := discovery.NewInMemoryDiscovery()
	srv := discovery.NewDiscoveryServer(imd)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	itpAddr := "itp://tcpGFwWCAyigmGfEXiFjhAdNhFmAQJA7TjYyXS1r89YkeKbSAGOGFrWCBDb9P064YswBhbl8uQt8WHQ"
	svcID := "0ddf8ffea58ab0edb90a7a74aeaf628e823978277e275d0d3888648f51ff90fb"
	if err := imd.Register(context.Background(), discovery.ServiceRegistration{
		ID:        svcID,
		Address:   itpAddr,
		Protocols: []string{"storage-v1"},
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Also register an HTTP service
	httpSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/id" {
			_, _ = w.Write([]byte("http-svc-id"))
		}
	}))
	defer httpSvc.Close()

	if err := imd.Register(context.Background(), discovery.ServiceRegistration{
		ID:        "http-svc-id",
		Address:   httpSvc.URL,
		Protocols: []string{"finder-v1"},
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	captureOutput := func(args []string) string {
		oldStdout := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe failed: %v", err)
		}
		os.Stdout = w

		runStatus(&config.InvariantConfig{Discovery: ts.URL}, args)

		_ = w.Close()
		os.Stdout = oldStdout
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		_ = r.Close()
		return buf.String()
	}

	defaultOut := captureOutput(nil)
	if !strings.Contains(defaultOut, "<itp>") {
		t.Errorf("expected default status output to contain <itp>, got:\n%s", defaultOut)
	}
	if strings.Contains(defaultOut, itpAddr) {
		t.Errorf("expected default status output NOT to contain full ITP address, got:\n%s", defaultOut)
	}
	if !strings.Contains(defaultOut, httpSvc.URL) {
		t.Errorf("expected default status output to contain HTTP address %s, got:\n%s", httpSvc.URL, defaultOut)
	}

	verboseOut := captureOutput([]string{"-verbose"})
	if !strings.Contains(verboseOut, itpAddr) {
		t.Errorf("expected -verbose status output to contain full ITP address %s, got:\n%s", itpAddr, verboseOut)
	}
	if strings.Contains(verboseOut, "<itp>") {
		t.Errorf("expected -verbose status output NOT to contain <itp>, got:\n%s", verboseOut)
	}
}
