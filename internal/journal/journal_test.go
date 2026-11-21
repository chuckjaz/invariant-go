package journal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_PutGetDeleteAndReplay(t *testing.T) {
	dir := t.TempDir()

	s, err := NewStore[string, string](dir, 0)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	if err := s.Put("k1", "v1", nil); err != nil {
		t.Fatalf("Put k1 failed: %v", err)
	}
	if err := s.Put("k2", "v2", nil); err != nil {
		t.Fatalf("Put k2 failed: %v", err)
	}
	if err := s.Delete("k1", nil); err != nil {
		t.Fatalf("Delete k1 failed: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	// Verify Close is idempotent
	if err := s.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}

	s2, err := NewStore[string, string](dir, 0)
	if err != nil {
		t.Fatalf("reopen NewStore failed: %v", err)
	}
	defer s2.Close()

	if _, ok := s2.Get("k1"); ok {
		t.Fatalf("expected k1 to be deleted after replay")
	}
	if v, ok := s2.Get("k2"); !ok || v != "v2" {
		t.Fatalf("expected k2=v2, got %q (ok=%v)", v, ok)
	}
}

func TestStore_SnapshotAndCompaction(t *testing.T) {
	dir := t.TempDir()

	s, err := NewStore[string, int](dir, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	if err := s.Put("a", 1, nil); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "snapshot.json")); err != nil {
		t.Fatalf("expected snapshot.json to exist: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	s2, err := NewStore[string, int](dir, 0)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer s2.Close()

	if v, ok := s2.Get("a"); !ok || v != 1 {
		t.Fatalf("expected a=1, got %d (ok=%v)", v, ok)
	}
}
