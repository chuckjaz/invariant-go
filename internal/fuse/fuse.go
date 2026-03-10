package fuse

import (
	"context"
	"io"
	"strconv"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"invariant/internal/files"
	"invariant/internal/filetree"
)

type Node struct {
	fs.Inode
	filesrv files.Files
	nodeID  uint64
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

func NewNode(filesrv files.Files, nodeID uint64) *Node {
	return &Node{
		filesrv: filesrv,
		nodeID:  nodeID,
	}
}

func (n *Node) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	info, err := n.filesrv.GetInfo(ctx, n.nodeID)
	if err != nil {
		return syscall.ENOENT
	}

	attrs, err := n.filesrv.GetAttributes(ctx, n.nodeID)
	if err != nil {
		return syscall.ENOENT
	}

	out.Ino = n.nodeID

	if attrs.Size != nil {
		out.Size = *attrs.Size
	}

	mode := uint32(0)
	switch info.Kind {
	case string(filetree.DirectoryKind):
		mode |= fuse.S_IFDIR
	case string(filetree.FileKind):
		mode |= fuse.S_IFREG
	case string(filetree.SymbolicLinkKind):
		mode |= fuse.S_IFLNK
	}

	if attrs.Mode != nil {
		parsed, _ := strconv.ParseUint(*attrs.Mode, 8, 32)
		mode |= uint32(parsed)
	} else {
		if info.Kind == string(filetree.DirectoryKind) {
			mode |= 0755
		} else {
			mode |= 0644
		}
	}

	out.Mode = mode
	if attrs.CreateTime != nil {
		out.Ctime = *attrs.CreateTime
	}
	if attrs.ModifyTime != nil {
		out.Mtime = *attrs.ModifyTime
	}

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

	return n.Getattr(ctx, f, out)
}

func (n *Node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	info, err := n.filesrv.Lookup(ctx, n.nodeID, name)
	if err != nil {
		return nil, syscall.ENOENT
	}

	attrs, err := n.filesrv.GetAttributes(ctx, info.Node)
	if err != nil {
		return nil, syscall.ENOENT
	}

	mode := uint32(0)
	switch info.Kind {
	case string(filetree.DirectoryKind):
		mode |= fuse.S_IFDIR
	case string(filetree.FileKind):
		mode |= fuse.S_IFREG
	case string(filetree.SymbolicLinkKind):
		mode |= fuse.S_IFLNK
	}

	if attrs.Mode != nil {
		parsed, _ := strconv.ParseUint(*attrs.Mode, 8, 32)
		mode |= uint32(parsed)
	} else {
		if info.Kind == string(filetree.DirectoryKind) {
			mode |= 0755
		} else {
			mode |= 0644
		}
	}

	childNode := NewNode(n.filesrv, info.Node)
	inode := n.NewInode(ctx, childNode, fs.StableAttr{Ino: info.Node, Mode: mode})

	out.Ino = info.Node

	if attrs.Size != nil {
		out.Attr.Size = *attrs.Size
	}

	out.Attr.Mode = mode
	if attrs.CreateTime != nil {
		out.Attr.Ctime = *attrs.CreateTime
	}
	if attrs.ModifyTime != nil {
		out.Attr.Mtime = *attrs.ModifyTime
	}

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

func (n *Node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// We don't need a custom file handle. NodeReader/NodeWriter handle data.
	return nil, 0, 0
}

func (n *Node) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
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
	err := n.filesrv.WriteFile(ctx, n.nodeID, off, false, &bytesReader{b: data})
	if err != nil {
		return 0, syscall.EIO
	}

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

	return inode, nil, 0, 0
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

	return inode, 0
}

func (n *Node) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	err := n.filesrv.CreateEntry(ctx, n.nodeID, name, filetree.SymbolicLinkKind, target, nil, nil)
	if err != nil {
		return nil, syscall.EIO
	}

	return n.Lookup(ctx, name, out)
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
