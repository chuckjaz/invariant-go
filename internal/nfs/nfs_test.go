package nfs

import (
	"bytes"
	"context"
	"os"
	"testing"

	"invariant/internal/content"
	"invariant/internal/files"
	"invariant/internal/filetree"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func setupTestFiles(t *testing.T) (*files.InMemoryFiles, *storage.InMemoryStorage) {
	storageClient := storage.NewInMemoryStorage()
	slotSvc := slots.NewMemorySlots("test")

	_ = slotSvc.Create(context.Background(), "test-slot", "", "unauth")

	// Write empty dir
	emptyDir := filetree.Directory{}
	dirBytes, stringerr := emptyDir.MarshalJSON()
	if stringerr != nil {
		t.Fatalf("MarshalJSON: %v", stringerr)
	}
	link, err := content.Write(bytes.NewReader(dirBytes), storageClient, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	_ = slotSvc.Update(context.Background(), "test-slot", link.Address, "", nil)

	opts := files.Options{
		Storage:  storageClient,
		Slots:    slotSvc,
		RootLink: content.ContentLink{Slot: true, Address: "test-slot"},
	}

	filesrv, err := files.NewInMemoryFiles(opts)
	if err != nil {
		t.Fatalf("Failed to initialize files service: %v", err)
	}

	return filesrv, storageClient
}

func TestFSNodeCreation(t *testing.T) {
	filesrv, _ := setupTestFiles(t)
	defer filesrv.Close()

	ctx := context.Background()
	err := filesrv.CreateEntry(ctx, 1, "test.txt", filetree.FileKind, "", nil, nil)
	if err != nil {
		t.Fatalf("Failed to create entry: %v", err)
	}

	nfsFs := NewFS(filesrv, 1) // 1 is the root node ID
	if nfsFs == nil {
		t.Fatal("Expected non-nil FS")
	}

	stat, err := nfsFs.Stat("test.txt")
	if err != nil {
		t.Errorf("Stat failed: %v", err)
	}

	if stat.Name() != "test.txt" {
		t.Errorf("Expected Name test.txt, got %s", stat.Name())
	}
	if stat.IsDir() {
		t.Error("Expected IsDir to be false")
	}
}

func TestFSFileWriteRead(t *testing.T) {
	filesrv, _ := setupTestFiles(t)
	defer filesrv.Close()

	nfsFs := NewFS(filesrv, 1)

	// Test Create
	f, err := nfsFs.Create("hello.txt")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	n, err := f.Write([]byte("world"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != 5 {
		t.Errorf("Expected 5 bytes written, got %d", n)
	}

	err = f.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Test Open
	f2, err := nfsFs.Open("hello.txt")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	buf := make([]byte, 10)
	n, err = f2.Read(buf)
	if err != nil && err.Error() != "EOF" {
		t.Fatalf("Read failed: %v", err)
	}
	if string(buf[:n]) != "world" {
		t.Errorf("Expected 'world', got %q", buf[:n])
	}
	f2.Close()
}

func TestFSManageDirectories(t *testing.T) {
	filesrv, _ := setupTestFiles(t)
	defer filesrv.Close()

	nfsFs := NewFS(filesrv, 1)

	err := nfsFs.MkdirAll("sub/dir", 0755)
	if err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	stat, err := nfsFs.Stat("sub/dir")
	if err != nil {
		t.Fatalf("Stat sub/dir failed: %v", err)
	}
	if !stat.IsDir() {
		t.Error("Expected sub/dir to be directory")
	}

	dirEntries, err := nfsFs.ReadDir("sub")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(dirEntries) != 1 || dirEntries[0].Name() != "dir" {
		t.Errorf("Expected 1 dir entry 'dir'")
	}
}

func TestFSRemoveRename(t *testing.T) {
	filesrv, _ := setupTestFiles(t)
	defer filesrv.Close()

	nfsFs := NewFS(filesrv, 1)

	f, err := nfsFs.Create("rm_test.txt")
	if err != nil {
		t.Fatalf("Create rm_test.txt failed: %v", err)
	}
	f.Close()

	err = nfsFs.Remove("rm_test.txt")
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	_, err = nfsFs.Stat("rm_test.txt")
	if !os.IsNotExist(err) && err != os.ErrNotExist {
		t.Errorf("Expected NotExist error, got: %v", err)
	}

	f, err = nfsFs.Create("mv_test.txt")
	if err != nil {
		t.Fatalf("Create mv_test.txt failed: %v", err)
	}
	f.Close()

	err = nfsFs.Rename("mv_test.txt", "moved_test.txt")
	if err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	_, err = nfsFs.Stat("moved_test.txt")
	if err != nil {
		t.Errorf("Moved file not found: %v", err)
	}
}
