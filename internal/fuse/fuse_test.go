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

func TestFuseNodeCachingTimeouts(t *testing.T) {
	filesrv := setupTestFuseFiles(t)
	defer filesrv.Close()

	ctx := context.Background()
	_ = filesrv.CreateEntry(ctx, 1, "cached.txt", "File", "", nil, nil)

	rootNode := NewNode(filesrv, 1)
	_ = fs.NewNodeFS(rootNode, &fs.Options{})

	// 1. Check Getattr sets timeout
	var attrOut fuse.AttrOut
	errno := rootNode.Getattr(ctx, nil, &attrOut)
	if errno != 0 {
		t.Fatalf("Getattr failed: %d", errno)
	}
	if attrOut.Timeout() == 0 {
		t.Errorf("Expected non-zero Timeout in Getattr, got %v", attrOut.Timeout())
	}

	// 2. Check Lookup sets timeout
	var entryOut fuse.EntryOut
	_, errno = rootNode.Lookup(ctx, "cached.txt", &entryOut)
	if errno != 0 {
		t.Fatalf("Lookup failed: %d", errno)
	}
	if entryOut.AttrTimeout() == 0 {
		t.Errorf("Expected non-zero AttrTimeout in Lookup, got %v", entryOut.AttrTimeout())
	}
	if entryOut.EntryTimeout() == 0 {
		t.Errorf("Expected non-zero EntryTimeout in Lookup, got %v", entryOut.EntryTimeout())
	}
}

func TestFuseFastPathAttributes(t *testing.T) {
	filesrv := setupTestFuseFiles(t)
	defer filesrv.Close()

	ctx := context.Background()
	_ = filesrv.CreateEntry(ctx, 1, "fastpath.txt", "File", "", nil, nil)

	rootNode := NewNode(filesrv, 1)
	_ = fs.NewNodeFS(rootNode, &fs.Options{})

	var entryOut fuse.EntryOut
	childInode, errno := rootNode.Lookup(ctx, "fastpath.txt", &entryOut)
	if errno != 0 {
		t.Fatalf("Lookup failed: %d", errno)
	}

	childNode, ok := childInode.Operations().(*Node)
	if !ok {
		t.Fatalf("Expected *Node operations, got %T", childInode.Operations())
	}

	// 1. Initial cached state
	childNode.mu.RLock()
	isCached := childNode.cached
	childNode.mu.RUnlock()
	if !isCached {
		t.Errorf("Expected childNode to be cached after Lookup")
	}

	// 2. Getattr should hit in-memory cache directly
	var attrOut fuse.AttrOut
	errno = childNode.Getattr(ctx, nil, &attrOut)
	if errno != 0 {
		t.Fatalf("Getattr failed: %d", errno)
	}
	if attrOut.Ino != entryOut.Ino {
		t.Errorf("Expected Ino %d, got %d", entryOut.Ino, attrOut.Ino)
	}

	// 3. InvalidateCache resets cached flag
	childNode.InvalidateCache()
	childNode.mu.RLock()
	isCached = childNode.cached
	childNode.mu.RUnlock()
	if isCached {
		t.Errorf("Expected childNode cache to be invalidated")
	}
}

func TestFuseTimestamps_NonZero(t *testing.T) {
	storageClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("fuse-slot-times")
	_ = slotClient.Create(context.Background(), "time-slot", "", "")

	// Create directory with one entry with explicit times and one with nil times
	modTime := uint64(1700000000)
	testDir := filetree.Directory{
		&filetree.FileEntry{
			BaseEntry: filetree.BaseEntry{
				Name:       "explicit.txt",
				Kind:       filetree.FileKind,
				ModifyTime: &modTime,
				CreateTime: &modTime,
			},
			Content: content.ContentLink{Address: "mock-content-1"},
			Size:    10,
		},
		&filetree.FileEntry{
			BaseEntry: filetree.BaseEntry{
				Name: "legacy.txt",
				Kind: filetree.FileKind,
			},
			Content: content.ContentLink{Address: "mock-content-2"},
			Size:    20,
		},
	}
	dirBytes, err := testDir.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	link, err := content.Write(bytes.NewReader(dirBytes), storageClient, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = slotClient.Update(context.Background(), "time-slot", link.Address, "", nil)

	opts := files.Options{
		Storage:  storageClient,
		Slots:    slotClient,
		RootLink: content.ContentLink{Slot: true, Address: "time-slot"},
	}

	filesrv, err := files.NewInMemoryFiles(opts)
	if err != nil {
		t.Fatalf("Failed to initialize files service: %v", err)
	}
	defer filesrv.Close()

	ctx := context.Background()
	rootNode := NewNode(filesrv, 1)
	_ = fs.NewNodeFS(rootNode, &fs.Options{})

	// 1. Root directory timestamps
	var rootAttr fuse.AttrOut
	if errno := rootNode.Getattr(ctx, nil, &rootAttr); errno != 0 {
		t.Fatalf("Root Getattr failed: %d", errno)
	}
	if rootAttr.Mtime == 0 || rootAttr.Ctime == 0 || rootAttr.Atime == 0 {
		t.Errorf("Root directory has zero timestamp: Mtime=%d, Ctime=%d, Atime=%d", rootAttr.Mtime, rootAttr.Ctime, rootAttr.Atime)
	}

	// 2. Explicit file timestamps
	var explicitEntry fuse.EntryOut
	explicitInode, errno := rootNode.Lookup(ctx, "explicit.txt", &explicitEntry)
	if errno != 0 {
		t.Fatalf("Lookup explicit.txt failed: %d", errno)
	}
	if explicitEntry.Attr.Mtime != modTime || explicitEntry.Attr.Ctime != modTime {
		t.Errorf("Expected explicit.txt Mtime %d, got Mtime=%d Ctime=%d", modTime, explicitEntry.Attr.Mtime, explicitEntry.Attr.Ctime)
	}
	explicitNode := explicitInode.Operations().(*Node)
	var explicitAttr fuse.AttrOut
	if errno := explicitNode.Getattr(ctx, nil, &explicitAttr); errno != 0 {
		t.Fatalf("Getattr explicit.txt failed: %d", errno)
	}
	if explicitAttr.Mtime != modTime {
		t.Errorf("Expected explicit.txt Getattr Mtime %d, got %d", modTime, explicitAttr.Mtime)
	}

	// 3. Legacy file without timestamps (must NOT be 0)
	var legacyEntry fuse.EntryOut
	legacyInode, errno := rootNode.Lookup(ctx, "legacy.txt", &legacyEntry)
	if errno != 0 {
		t.Fatalf("Lookup legacy.txt failed: %d", errno)
	}
	if legacyEntry.Attr.Mtime == 0 || legacyEntry.Attr.Ctime == 0 || legacyEntry.Attr.Atime == 0 {
		t.Errorf("legacy.txt has zero timestamp (Dec 31 1969 bug!): Mtime=%d, Ctime=%d, Atime=%d", legacyEntry.Attr.Mtime, legacyEntry.Attr.Ctime, legacyEntry.Attr.Atime)
	}
	legacyNode := legacyInode.Operations().(*Node)
	var legacyAttr fuse.AttrOut
	if errno := legacyNode.Getattr(ctx, nil, &legacyAttr); errno != 0 {
		t.Fatalf("Getattr legacy.txt failed: %d", errno)
	}
	if legacyAttr.Mtime == 0 || legacyAttr.Ctime == 0 || legacyAttr.Atime == 0 {
		t.Errorf("legacy.txt Getattr has zero timestamp: Mtime=%d, Ctime=%d, Atime=%d", legacyAttr.Mtime, legacyAttr.Ctime, legacyAttr.Atime)
	}
}
