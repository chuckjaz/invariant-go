package fuse

import (
	"context"
	"io"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"invariant/internal/files"
	"invariant/internal/filetree"
)

// Default FUSE kernel caching timeouts for high performance VFS stat/lookup resolution.
const (
	DefaultAttrTimeout     = 10 * time.Second
	DefaultEntryTimeout    = 10 * time.Second
	DefaultNegativeTimeout = 2 * time.Second
)

type Node struct {
	fs.Inode
	filesrv files.Files
	nodeID  uint64
	mu      sync.RWMutex
	cached  bool
	info    files.NodeInfo
}

var _ = (fs.NodeGetattrer)((*Node)(nil))
var _ = (fs.NodeSetattrer)((*Node)(nil))
var _ = (fs.NodeLookuper)((*Node)(nil))
var _ = (fs.NodeReaddirer)((*Node)(nil))
var _ = (fs.NodeOpener)((*Node)(nil))
var _ = (fs.NodeReader)((*Node)(nil))
var _ = (fs.NodeWriter)((*Node)(nil))
var _ = (fs.NodeCreater)((*Node)(nil))
var _ = (fs.NodeMkdirer)((*Node)(nil))
var _ = (fs.NodeSymlinker)((*Node)(nil))
var _ = (fs.NodeReadlinker)((*Node)(nil))
var _ = (fs.NodeUnlinker)((*Node)(nil))
var _ = (fs.NodeRmdirer)((*Node)(nil))
var _ = (fs.NodeRenamer)((*Node)(nil))
var _ = (fs.NodeFsyncer)((*Node)(nil))
var _ = (fs.NodeAllocater)((*Node)(nil))

func NewNode(filesrv files.Files, nodeID uint64) *Node {
	return &Node{
		filesrv: filesrv,
		nodeID:  nodeID,
	}
}

// InvalidateCache clears the fast-path attribute cache for this node.
func (n *Node) InvalidateCache() {
	n.mu.Lock()
	n.cached = false
	n.mu.Unlock()
}

func (n *Node) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.RLock()
	if n.cached {
		out.Ino = n.info.Node
		out.Size = n.info.Size
		out.Mode = n.info.Mode
		out.Ctime = n.info.CreateTime
		out.Mtime = n.info.ModifyTime
		n.mu.RUnlock()
		out.SetTimeout(DefaultAttrTimeout)
		return 0
	}
	n.mu.RUnlock()

	info, err := n.filesrv.GetNodeInfo(ctx, n.nodeID)
	if err != nil {
		return syscall.ENOENT
	}

	mode := info.Mode
	switch info.Kind {
	case string(filetree.DirectoryKind):
		mode |= fuse.S_IFDIR
	case string(filetree.FileKind):
		mode |= fuse.S_IFREG
	case string(filetree.SymbolicLinkKind):
		mode |= fuse.S_IFLNK
	}
	info.Mode = mode

	n.mu.Lock()
	n.info = info
	n.cached = true
	n.mu.Unlock()

	out.Ino = info.Node
	out.Size = info.Size
	out.Mode = mode
	out.Ctime = info.CreateTime
	out.Mtime = info.ModifyTime

	out.SetTimeout(DefaultAttrTimeout)
	return 0
}

func (n *Node) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	var update files.EntryAttributes

	if size, ok := in.GetSize(); ok {
		update.Size = &size
	}
	if mode, ok := in.GetMode(); ok {
		smode := strconv.FormatUint(uint64(mode&07777), 8)
		update.Mode = &smode
	}
	if mtime, ok := in.GetMTime(); ok {
		sec := uint64(mtime.Unix())
		update.ModifyTime = &sec
	}

	_, err := n.filesrv.SetAttributes(ctx, n.nodeID, update)
	if err != nil {
		return syscall.EIO
	}

	n.InvalidateCache()
	return n.Getattr(ctx, f, out)
}

func (n *Node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	info, err := n.filesrv.LookupNodeInfo(ctx, n.nodeID, name)
	if err != nil {
		return nil, syscall.ENOENT
	}

	mode := info.Mode
	switch info.Kind {
	case string(filetree.DirectoryKind):
		mode |= fuse.S_IFDIR
	case string(filetree.FileKind):
		mode |= fuse.S_IFREG
	case string(filetree.SymbolicLinkKind):
		mode |= fuse.S_IFLNK
	}
	info.Mode = mode

	childNode := &Node{
		filesrv: n.filesrv,
		nodeID:  info.Node,
		cached:  true,
		info:    info,
	}
	inode := n.NewInode(ctx, childNode, fs.StableAttr{Ino: info.Node, Mode: mode})

	out.Ino = info.Node
	out.Attr.Size = info.Size
	out.Attr.Mode = mode
	out.Attr.Ctime = info.CreateTime
	out.Attr.Mtime = info.ModifyTime

	out.SetEntryTimeout(DefaultEntryTimeout)
	out.SetAttrTimeout(DefaultAttrTimeout)
	return inode, 0
}

func (n *Node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	dir, err := n.filesrv.ReadDirectory(ctx, n.nodeID, 0, 0)
	if err != nil {
		return nil, syscall.EIO
	}

	var entries []fuse.DirEntry
	for _, entry := range dir {
		kind := uint32(0)
		switch entry.GetKind() {
		case filetree.DirectoryKind:
			kind = fuse.S_IFDIR
		case filetree.FileKind:
			kind = fuse.S_IFREG
		case filetree.SymbolicLinkKind:
			kind = fuse.S_IFLNK
		}

		entries = append(entries, fuse.DirEntry{
			Mode: kind,
			Name: entry.GetName(),
		})
	}

	return fs.NewListDirStream(entries), 0
}

type fileHandle struct {
	mu           sync.Mutex
	node         *Node
	f            *os.File
	dirty        bool
	reader       io.ReadCloser
	readerOffset int64
}

var _ = (fs.FileReader)((*fileHandle)(nil))
var _ = (fs.FileWriter)((*fileHandle)(nil))
var _ = (fs.FileFlusher)((*fileHandle)(nil))
var _ = (fs.FileReleaser)((*fileHandle)(nil))
var _ = (fs.FileFsyncer)((*fileHandle)(nil))
var _ = (fs.FileAllocater)((*fileHandle)(nil))
var _ = (fs.FileSetattrer)((*fileHandle)(nil))

func (fh *fileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if fh.f != nil {
		n, err := fh.f.ReadAt(dest, off)
		if err != nil && err != io.EOF {
			return nil, syscall.EIO
		}
		return fuse.ReadResultData(dest[:n]), 0
	}

	if fh.reader == nil {
		r, err := fh.node.filesrv.ReadFile(ctx, fh.node.nodeID, 0, 0)
		if err != nil {
			return nil, syscall.EIO
		}
		fh.reader = r
		fh.readerOffset = 0
	}

	if fh.readerOffset != off {
		if seeker, ok := fh.reader.(io.Seeker); ok {
			_, err := seeker.Seek(off, io.SeekStart)
			if err != nil {
				return nil, syscall.EIO
			}
			fh.readerOffset = off
		} else {
			fh.reader.Close()
			r, err := fh.node.filesrv.ReadFile(ctx, fh.node.nodeID, off, 0)
			if err != nil {
				fh.reader = nil
				return nil, syscall.EIO
			}
			fh.reader = r
			fh.readerOffset = off
		}
	}

	nread, err := io.ReadFull(fh.reader, dest)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, syscall.EIO
	}
	fh.readerOffset += int64(nread)

	return fuse.ReadResultData(dest[:nread]), 0
}

func (fh *fileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	fh.mu.Lock()
	defer fh.mu.Unlock()

	if fh.f == nil {
		f, err := os.CreateTemp("", "invariant-fuse-*")
		if err != nil {
			return 0, syscall.EIO
		}
		os.Remove(f.Name())
		fh.f = f

		reader, err := fh.node.filesrv.ReadFile(ctx, fh.node.nodeID, 0, 0)
		if err == nil {
			io.Copy(f, reader)
			reader.Close()
		}
	}

	_, err := fh.f.WriteAt(data, off)
	if err != nil {
		return 0, syscall.EIO
	}
	fh.dirty = true

	return uint32(len(data)), 0
}

func (fh *fileHandle) Flush(ctx context.Context) syscall.Errno {
	fh.mu.Lock()
	defer fh.mu.Unlock()

	if fh.f != nil && fh.dirty {
		fh.f.Seek(0, io.SeekStart)
		err := fh.node.filesrv.WriteFile(ctx, fh.node.nodeID, 0, false, fh.f)
		if err != nil {
			return syscall.EIO
		}
		fh.dirty = false
		fh.node.InvalidateCache()
	}
	return 0
}

func (fh *fileHandle) Release(ctx context.Context) syscall.Errno {
	fh.Flush(ctx)

	fh.mu.Lock()
	defer fh.mu.Unlock()
	if fh.f != nil {
		fh.f.Close()
		fh.f = nil
	}
	if fh.reader != nil {
		fh.reader.Close()
		fh.reader = nil
	}
	return 0
}

func (fh *fileHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	return fh.Flush(ctx)
}

func (fh *fileHandle) Allocate(ctx context.Context, off uint64, size uint64, mode uint32) syscall.Errno {
	// Pretend allocation succeeded to satisfy editors using fallocate(2) to preemptively reserve space.
	// Since we stream dynamically based on backend storage, actual sparse file allocation on the temporary handle
	// isn't strictly necessary mechanically unless doing huge local buffering, but it prevents ENOTSUP crashing.
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if fh.f != nil {
		// optionally do fh.f.Truncate(int64(off + size))? Ignore for now to limit disk spikes.
	}
	return 0
}

func (fh *fileHandle) Setattr(ctx context.Context, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if fh.node != nil {
		return fh.node.Setattr(ctx, fh, in, out)
	}
	return 0
}

func (n *Node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return &fileHandle{node: n}, 0, 0
}

func (n *Node) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if fh, ok := f.(*fileHandle); ok {
		return fh.Read(ctx, dest, off)
	}
	reader, err := n.filesrv.ReadFile(ctx, n.nodeID, off, int64(len(dest)))
	if err != nil {
		return nil, syscall.EIO
	}
	defer reader.Close()

	nread, err := io.ReadFull(reader, dest)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, syscall.EIO
	}

	return fuse.ReadResultData(dest[:nread]), 0
}

type bytesReader struct {
	b []byte
}

func (r *bytesReader) Read(p []byte) (n int, err error) {
	n = copy(p, r.b)
	r.b = r.b[n:]
	if len(r.b) == 0 {
		return n, io.EOF
	}
	return n, nil
}

func (n *Node) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	if fh, ok := f.(*fileHandle); ok {
		return fh.Write(ctx, data, off)
	}
	err := n.filesrv.WriteFile(ctx, n.nodeID, off, false, &bytesReader{b: data})
	if err != nil {
		return 0, syscall.EIO
	}

	n.InvalidateCache()
	return uint32(len(data)), 0
}

func (n *Node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	err := n.filesrv.CreateEntry(ctx, n.nodeID, name, filetree.FileKind, "", nil, nil)
	if err != nil {
		return nil, nil, 0, syscall.EIO
	}

	inode, errno := n.Lookup(ctx, name, out)
	if errno != 0 {
		return nil, nil, 0, errno
	}

	// Update mode
	smode := strconv.FormatUint(uint64(mode&07777), 8)
	_, _ = n.filesrv.SetAttributes(ctx, out.Ino, files.EntryAttributes{Mode: &smode})

	out.SetEntryTimeout(DefaultEntryTimeout)
	out.SetAttrTimeout(DefaultAttrTimeout)

	fh := &fileHandle{node: inode.Operations().(*Node)}

	return inode, fh, 0, 0
}

func (n *Node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	err := n.filesrv.CreateEntry(ctx, n.nodeID, name, filetree.DirectoryKind, "", nil, nil)
	if err != nil {
		return nil, syscall.EIO
	}

	inode, errno := n.Lookup(ctx, name, out)
	if errno != 0 {
		return nil, errno
	}

	// Update mode
	smode := strconv.FormatUint(uint64(mode&07777), 8)
	_, _ = n.filesrv.SetAttributes(ctx, out.Ino, files.EntryAttributes{Mode: &smode})

	out.SetEntryTimeout(DefaultEntryTimeout)
	out.SetAttrTimeout(DefaultAttrTimeout)

	return inode, 0
}

func (n *Node) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	err := n.filesrv.CreateEntry(ctx, n.nodeID, name, filetree.SymbolicLinkKind, target, nil, nil)
	if err != nil {
		return nil, syscall.EIO
	}

	inode, errno := n.Lookup(ctx, name, out)
	if errno != 0 {
		return nil, errno
	}

	out.SetEntryTimeout(DefaultEntryTimeout)
	out.SetAttrTimeout(DefaultAttrTimeout)

	return inode, 0
}

func (n *Node) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	reader, err := n.filesrv.ReadFile(ctx, n.nodeID, 0, 0)
	if err != nil {
		return nil, syscall.EIO
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, syscall.EIO
	}

	return data, 0
}

func (n *Node) Unlink(ctx context.Context, name string) syscall.Errno {
	err := n.filesrv.Remove(ctx, n.nodeID, name)
	if err != nil {
		return syscall.ENOENT
	}
	return 0
}

func (n *Node) Rmdir(ctx context.Context, name string) syscall.Errno {
	err := n.filesrv.Remove(ctx, n.nodeID, name)
	if err != nil {
		return syscall.ENOENT
	}
	return 0
}

func (n *Node) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	newParentNode, ok := newParent.(*Node)
	if !ok {
		return syscall.EXDEV
	}

	err := n.filesrv.Rename(ctx, n.nodeID, name, newParentNode.nodeID, newName)
	if err != nil {
		return syscall.EIO
	}

	return 0
}

func (n *Node) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	if fh, ok := f.(*fileHandle); ok {
		return fh.Fsync(ctx, flags)
	}
	return 0
}

func (n *Node) Allocate(ctx context.Context, f fs.FileHandle, off uint64, size uint64, mode uint32) syscall.Errno {
	if fh, ok := f.(*fileHandle); ok {
		return fh.Allocate(ctx, off, size, mode)
	}
	return 0
}
