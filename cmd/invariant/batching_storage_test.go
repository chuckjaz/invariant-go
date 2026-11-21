package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync/atomic"
	"testing"

	"github.com/chuckjaz/invariant-go/internal/storage"
)

func TestBatchingTrackingStorage_StoreHasAndClose(t *testing.T) {
	ctx := context.Background()
	mem := storage.NewInMemoryStorage()
	var uploaded uint64

	bts := NewBatchingTrackingStorage(mem, &uploaded)
	defer bts.Close()

	payload1 := []byte("block-one-data")
	addr1, err := bts.Store(ctx, bytes.NewReader(payload1))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if atomic.LoadUint64(&uploaded) != uint64(len(payload1)) {
		t.Fatalf("expected uploaded=%d, got %d", len(payload1), atomic.LoadUint64(&uploaded))
	}
	if !bts.Has(ctx, addr1) {
		t.Fatalf("expected Has(%s) to be true", addr1)
	}

	payload2 := []byte("block-two-data")
	sum2 := sha256.Sum256(payload2)
	addr2 := hex.EncodeToString(sum2[:])
	ok, err := bts.StoreAt(ctx, addr2, bytes.NewReader(payload2))
	if err != nil || !ok {
		t.Fatalf("StoreAt failed: ok=%v err=%v", ok, err)
	}
	bts.FlushStore(ctx)

	rc, exists := bts.Get(ctx, addr2)
	if !exists {
		t.Fatalf("expected Get(%s) to succeed after FlushStore", addr2)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(got, payload2) {
		t.Fatalf("got %q, want %q", got, payload2)
	}

	// Close should be idempotent
	bts.Close()
}
