package main

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"invariant/internal/config"
	"invariant/internal/discovery"
	"invariant/internal/finder"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestRunPrint_Slot(t *testing.T) {
	ctx := context.Background()

	// 1. Setup in-memory Discovery
	inmemDisc := discovery.NewInMemoryDiscovery()
	discServer := discovery.NewDiscoveryServer(inmemDisc)
	discTS := httptest.NewServer(discServer.Handler())
	defer discTS.Close()

	// 2. Setup in-memory Storage
	inmemStore := storage.NewInMemoryStorage()
	storeServer := storage.NewStorageServer(inmemStore)
	storeTS := httptest.NewServer(storeServer.Handler())
	defer storeTS.Close()

	// 3. Setup in-memory Finder
	finderID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	inmemFinder, err := finder.NewMemoryFinder(finderID)
	if err != nil {
		t.Fatalf("Failed to create finder: %v", err)
	}
	finderServer := finder.NewFinderServer(inmemFinder, inmemDisc)
	finderTS := httptest.NewServer(finderServer.Handler())
	defer finderTS.Close()

	// 4. Setup in-memory Slots
	inmemSlots := slots.NewMemorySlots("slots-test-id")
	slotsServer := slots.NewServer(inmemSlots)
	slotsTS := httptest.NewServer(slotsServer.Handler())
	defer slotsTS.Close()

	// Register services in discovery
	_ = inmemDisc.Register(ctx, discovery.ServiceRegistration{
		ID:        "storage-id",
		Address:   storeTS.URL,
		Protocols: []string{"storage-v1", "batch-storage-v1"},
	})
	_ = inmemDisc.Register(ctx, discovery.ServiceRegistration{
		ID:        "finder-id",
		Address:   finderTS.URL,
		Protocols: []string{"finder-v1"},
	})
	_ = inmemDisc.Register(ctx, discovery.ServiceRegistration{
		ID:        "slots-id",
		Address:   slotsTS.URL,
		Protocols: []string{"slots-v1"},
	})

	// Put test content in storage
	contentData := []byte("Hello Slot Content!")
	blockAddr, err := inmemStore.Store(ctx, bytes.NewReader(contentData))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Register block location in finder
	_ = inmemFinder.Notify(ctx, "storage-id", []string{blockAddr})

	// Create a slot pointing to the block
	slotID := "1111222233334444555566667777888811112222333344445555666677778888"
	err = inmemSlots.Create(ctx, slotID, blockAddr, "")
	if err != nil {
		t.Fatalf("Create slot failed: %v", err)
	}

	// Capture stdout
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	globalCfg := &config.InvariantConfig{
		Discovery: discTS.URL,
	}

	runPrint(globalCfg, []string{"-s", slotID})

	w.Close()
	os.Stdout = oldStdout

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if string(out) != string(contentData) {
		t.Errorf("expected %q, got %q", string(contentData), string(out))
	}
}

func TestRunPrint_Block(t *testing.T) {
	ctx := context.Background()

	// 1. Setup in-memory Discovery
	inmemDisc := discovery.NewInMemoryDiscovery()
	discServer := discovery.NewDiscoveryServer(inmemDisc)
	discTS := httptest.NewServer(discServer.Handler())
	defer discTS.Close()

	// 2. Setup in-memory Storage
	inmemStore := storage.NewInMemoryStorage()
	storeServer := storage.NewStorageServer(inmemStore)
	storeTS := httptest.NewServer(storeServer.Handler())
	defer storeTS.Close()

	// 3. Setup in-memory Finder
	finderID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	inmemFinder, err := finder.NewMemoryFinder(finderID)
	if err != nil {
		t.Fatalf("Failed to create finder: %v", err)
	}
	finderServer := finder.NewFinderServer(inmemFinder, inmemDisc)
	finderTS := httptest.NewServer(finderServer.Handler())
	defer finderTS.Close()

	// Register services in discovery
	_ = inmemDisc.Register(ctx, discovery.ServiceRegistration{
		ID:        "storage-id-2",
		Address:   storeTS.URL,
		Protocols: []string{"storage-v1", "batch-storage-v1"},
	})
	_ = inmemDisc.Register(ctx, discovery.ServiceRegistration{
		ID:        "finder-id-2",
		Address:   finderTS.URL,
		Protocols: []string{"finder-v1"},
	})

	// Put test content in storage
	contentData := []byte("Hello Direct Block Content!")
	blockAddr, err := inmemStore.Store(ctx, bytes.NewReader(contentData))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Register block location in finder
	_ = inmemFinder.Notify(ctx, "storage-id-2", []string{blockAddr})

	// Capture stdout
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	globalCfg := &config.InvariantConfig{
		Discovery: discTS.URL,
	}

	runPrint(globalCfg, []string{blockAddr})

	w.Close()
	os.Stdout = oldStdout

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(out), "Hello Direct Block Content!") {
		t.Errorf("expected %q to contain %q", string(out), "Hello Direct Block Content!")
	}
}
