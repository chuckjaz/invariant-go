package fuse

import (
	"bytes"
	"context"
	"testing"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"invariant/internal/content"
	"invariant/internal/files"
	"invariant/internal/filetree"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func setupTestFuseFiles(t *testing.T) *files.InMemoryFiles {
	t.Helper()
	storageClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("fuse-slot")
	_ = slotClient.Create(context.Background(), "test-slot", "", "")

	emptyDir := filetree.Directory{}
	dirBytes, err := emptyDir.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	link, err := content.Write(bytes.NewReader(dirBytes), storageClient, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = slotClient.Update(context.Background(), "test-slot", link.Address, "", nil)

	opts := files.Options{
		Storage:  storageClient,
		Slots:    slotClient,
		RootLink: content.ContentLink{Slot: true, Address: "test-slot"},
	}

	filesrv, err := files.NewInMemoryFiles(opts)
	if err != nil {
		t.Fatalf("Failed to initialize files service: %v", err)
	}
	return filesrv
}

func TestFuseNodeCreation(t *testing.T) {
	filesrv := setupTestFuseFiles(t)
	defer filesrv.Close()

	ctx := context.Background()
	_ = filesrv.CreateEntry(ctx, 1, "test.txt", "File", "", nil, nil)

	rootNode := NewNode(filesrv, 1)
	if rootNode == nil {
		t.Fatal("Expected non-nil Node")
	}

	attrOut := &fuse.AttrOut{}
	errno := rootNode.Getattr(ctx, nil, attrOut)
	if errno != 0 {
		t.Errorf("Getattr failed with errno %d", errno)
	}

	if attrOut.Ino != 1 {
		t.Errorf("Expected Ino 1, got %d", attrOut.Ino)
	}
}

func TestFuseOperations_NodeMethods(t *testing.T) {
	filesrv := setupTestFuseFiles(t)
	defer filesrv.Close()

	ctx := context.Background()
	rootNode := NewNode(filesrv, 1)
	_ = fs.NewNodeFS(rootNode, &fs.Options{})

	// 1. Mkdir
	var entryOut fuse.EntryOut
	_, errno := rootNode.Mkdir(ctx, "subfolder", 0755, &entryOut)
	if errno != 0 {
		t.Fatalf("Mkdir failed with errno: %d", errno)
	}
	if entryOut.Ino == 0 {
		t.Fatalf("Expected valid Ino for new directory")
	}
	dirIno := entryOut.Ino

	// 2. Create file inside subfolder
	subDirNode := NewNode(filesrv, dirIno)
	_ = fs.NewNodeFS(subDirNode, &fs.Options{})
	var fileEntryOut fuse.EntryOut
	_, fh, _, errno := subDirNode.Create(ctx, "hello.txt", 0, 0644, &fileEntryOut)
	if errno != 0 {
		t.Fatalf("Create failed with errno: %d", errno)
	}
	if fh == nil {
		t.Fatalf("Expected valid FileHandle")
	}
	fileIno := fileEntryOut.Ino

	// 3. Write via FileHandle
	fileNode := NewNode(filesrv, fileIno)
	fileH := &fileHandle{node: fileNode}
	data := []byte("hello fuse world")
	written, errno := fileH.Write(ctx, data, 0)
	if errno != 0 || written != uint32(len(data)) {
		t.Fatalf("Write failed: written=%d, errno=%d", written, errno)
	}

	// 4. Read via FileHandle
	readBuf := make([]byte, len(data))
	readRes, errno := fileH.Read(ctx, readBuf, 0)
	if errno != 0 {
		t.Fatalf("Read failed with errno: %d", errno)
	}
	readBytes, readStatus := readRes.Bytes(readBuf)
	if readStatus != fuse.OK || string(readBytes) != "hello fuse world" {
		t.Errorf("Read content mismatch: got %q, status=%v", string(readBytes), readStatus)
	}

	// 5. Setattr on file
	var setAttrIn fuse.SetAttrIn
	setAttrIn.Valid |= fuse.FATTR_SIZE | fuse.FATTR_MODE
	setAttrIn.Size = uint64(len(data))
	setAttrIn.Mode = 0755
	var attrOut fuse.AttrOut
	errno = fileNode.Setattr(ctx, fileH, &setAttrIn, &attrOut)
	if errno != 0 {
		t.Errorf("Setattr failed with errno: %d", errno)
	}

	// 6. Symlink & Readlink
	var symlinkOut fuse.EntryOut
	_, errno = subDirNode.Symlink(ctx, "hello.txt", "link.txt", &symlinkOut)
	if errno != 0 {
		t.Fatalf("Symlink failed with errno: %d", errno)
	}
	linkNode := NewNode(filesrv, symlinkOut.Ino)
	target, errno := linkNode.Readlink(ctx)
	if errno != 0 || string(target) != "hello.txt" {
		t.Errorf("Readlink target mismatch: got %q, errno=%d", string(target), errno)
	}

	// 7. Unlink
	errno = subDirNode.Unlink(ctx, "hello.txt")
	if errno != 0 {
		t.Fatalf("Unlink failed with errno: %d", errno)
	}
}
