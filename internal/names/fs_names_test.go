package names

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileSystemNames_PutAndGet(t *testing.T) {
	dir := t.TempDir()
	fsn, err := NewFileSystemNames(dir, 0)
	if err != nil {
		t.Fatalf("Failed to create FileSystemNames: %v", err)
	}
	defer fsn.Close()

	err = fsn.Put("service-a", "1234", []string{"storage-v1"})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	entry, err := fsn.Get("service-a")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if entry.Value != "1234" {
		t.Errorf("Expected 1234, got %v", entry.Value)
	}
	if len(entry.Tokens) != 1 || entry.Tokens[0] != "storage-v1" {
		t.Errorf("Expected tokens [storage-v1], got %v", entry.Tokens)
	}
}

func TestFileSystemNames_DeleteAndPrecondition(t *testing.T) {
	dir := t.TempDir()
	fsn, err := NewFileSystemNames(dir, 0)
	if err != nil {
		t.Fatalf("Failed to create FileSystemNames: %v", err)
	}
	defer fsn.Close()

	fsn.Put("service-a", "1234", nil)

	// Precondition fail
	err = fsn.Delete("service-a", "wrong-etag")
	if err != ErrPreconditionFailed {
		t.Errorf("Expected ErrPreconditionFailed, got %v", err)
	}

	// Delete
	err = fsn.Delete("service-a", "1234")
	if err != nil {
		t.Errorf("Delete failed: %v", err)
	}

	_, err = fsn.Get("service-a")
	if err != ErrNotFound {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}
}

func TestFileSystemNames_Recovery(t *testing.T) {
	dir := t.TempDir()

	fsn1, err := NewFileSystemNames(dir, 0)
	if err != nil {
		t.Fatalf("Failed to create FileSystemNames: %v", err)
	}
	fsn1.Put("service-a", "1234", []string{"storage-v1"})
	fsn1.Put("service-b", "5678", []string{"names-v1"})

	// Some time passes so journals order properly
	time.Sleep(2 * time.Millisecond)

	fsn1.Delete("service-a", "")
	fsn1.Close()

	// Should recover from journal
	fsn2, err := NewFileSystemNames(dir, 0)
	if err != nil {
		t.Fatalf("Failed to create FileSystemNames: %v", err)
	}

	_, err = fsn2.Get("service-a")
	if err != ErrNotFound {
		t.Errorf("Expected ErrNotFound for service-a, got %v", err)
	}

	entry, err := fsn2.Get("service-b")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if entry.Value != "5678" {
		t.Errorf("Expected 5678, got %v", entry.Value)
	}
	fsn2.Close()
}

func countJournalFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "journal-") && strings.HasSuffix(e.Name(), ".jsonl") {
			count++
		}
	}
	return count
}

func TestFileSystemNames_SnapshotAndRotation(t *testing.T) {
	dir := t.TempDir()

	// Create with fast snapshot interval
	fsn, err := NewFileSystemNames(dir, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Failed to create FileSystemNames: %v", err)
	}

	fsn.Put("service-a", "1234", []string{"storage-v1"})

	// Wait for a snapshot or two
	time.Sleep(100 * time.Millisecond)

	// We might have multiple snapshots in 100ms. Since doSnapshot creates a new journal and deletes
	// all older ones, the number of journal files shouldn't grow boundlessly.
	count := countJournalFiles(t, dir)
	if count > 2 { // Give an allowance for timing and locking
		t.Errorf("Expected at most 2 journal files due to rotation, found %d", count)
	}

	fsn.Put("service-b", "5678", []string{"names-v1"})
	fsn.Close()

	// Verify snapshot exists
	if _, err := os.Stat(filepath.Join(dir, "snapshot.json")); os.IsNotExist(err) {
		t.Errorf("snapshot.json should exist after periodic snapshotting")
	}

	// Should start and load correctly (reads snapshot + remaining latest journal)
	fsn2, err := NewFileSystemNames(dir, 0)
	if err != nil {
		t.Fatalf("Failed to create FileSystemNames: %v", err)
	}
	defer fsn2.Close()

	entryA, err := fsn2.Get("service-a")
	if err != nil {
		t.Fatalf("Get service-a failed: %v", err)
	}
	if entryA.Value != "1234" {
		t.Errorf("Expected 1234 for service-a")
	}

	entryB, err := fsn2.Get("service-b")
	if err != nil {
		t.Fatalf("Get service-b failed: %v", err)
	}
	if entryB.Value != "5678" {
		t.Errorf("Expected 5678 for service-b")
	}
}
