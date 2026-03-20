package nfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"invariant/internal/files"
	"invariant/internal/filetree"

	billy "github.com/go-git/go-billy/v5"
)

// FS implements billy.Filesystem, billy.Change, billy.Symlink
type FS struct {
	fsrv files.Files
	root uint64
}

var _ billy.Filesystem = (*FS)(nil)
var _ billy.Change = (*FS)(nil)
var _ billy.Symlink = (*FS)(nil)

func NewFS(fsrv files.Files, root uint64) *FS {
	return &FS{fsrv: fsrv, root: root}
}

func (f *FS) resolve(ctx context.Context, path string) (files.ContentInformationCommon, error) {
	path = filepath.Clean(path)
	if path == "." || path == "/" {
		return f.fsrv.GetInfo(ctx, f.root)
	}

	parts := strings.Split(path, "/")
	currentID := f.root
	var info files.ContentInformationCommon
	var err error

	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		info, err = f.fsrv.Lookup(ctx, currentID, part)
		if err != nil {
			return info, os.ErrNotExist
		}
		currentID = info.Node
	}
	return info, nil
}

func (f *FS) resolveParent(ctx context.Context, path string) (uint64, string, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	info, err := f.resolve(ctx, dir)
	if err != nil {
		return 0, "", err
	}
	if info.Kind != string(filetree.DirectoryKind) {
		return 0, "", errors.New("not a directory")
	}
	return info.Node, base, nil
}

func (f *FS) Create(filename string) (billy.File, error) {
	return f.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
}

func (f *FS) Open(filename string) (billy.File, error) {
	return f.OpenFile(filename, os.O_RDONLY, 0)
}

func (f *FS) OpenFile(filename string, flag int, perm os.FileMode) (billy.File, error) {
	ctx := context.Background()

	parentID, base, err := f.resolveParent(ctx, filename)
	if err != nil {
		return nil, err
	}

	info, err := f.fsrv.Lookup(ctx, parentID, base)
	if err != nil {
		if flag&os.O_CREATE == 0 {
			return nil, os.ErrNotExist
		}
		err = f.fsrv.CreateEntry(ctx, parentID, base, filetree.FileKind, "", nil, nil)
		if err != nil {
			return nil, err
		}
		info, err = f.fsrv.Lookup(ctx, parentID, base)
		if err != nil {
			return nil, err
		}
		smode := strconv.FormatUint(uint64(perm&07777), 8)
		_, _ = f.fsrv.SetAttributes(ctx, info.Node, files.EntryAttributes{Mode: &smode})
	} else {
		if flag&os.O_EXCL != 0 {
			return nil, os.ErrExist
		}
		if flag&os.O_TRUNC != 0 {
			_ = f.fsrv.WriteFile(ctx, info.Node, 0, false, strings.NewReader(""))
		}
	}

	return &File{
		fs:       f,
		nodeID:   info.Node,
		name:     filename,
		flag:     flag,
		position: 0,
	}, nil
}

func (f *FS) Stat(filename string) (os.FileInfo, error) {
	ctx := context.Background()
	info, err := f.resolve(ctx, filename)
	if err != nil {
		return nil, err
	}
	attrs, err := f.fsrv.GetAttributes(ctx, info.Node)
	if err != nil {
		return nil, err
	}
	return fileInfo{info: info, attrs: attrs, name: filepath.Base(filename)}, nil
}

func (f *FS) Rename(oldpath, newpath string) error {
	ctx := context.Background()
	oldParentID, oldBase, err := f.resolveParent(ctx, oldpath)
	if err != nil {
		return err
	}
	newParentID, newBase, err := f.resolveParent(ctx, newpath)
	if err != nil {
		return err
	}
	return f.fsrv.Rename(ctx, oldParentID, oldBase, newParentID, newBase)
}

func (f *FS) Remove(filename string) error {
	ctx := context.Background()
	parentID, base, err := f.resolveParent(ctx, filename)
	if err != nil {
		return err
	}
	return f.fsrv.Remove(ctx, parentID, base)
}

func (f *FS) Join(elem ...string) string {
	return filepath.Join(elem...)
}

func (f *FS) TempFile(dir, prefix string) (billy.File, error) {
	return nil, errors.New("not supported")
}

func (f *FS) ReadDir(path string) ([]os.FileInfo, error) {
	ctx := context.Background()
	info, err := f.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	dirContent, err := f.fsrv.ReadDirectory(ctx, info.Node, 0, 0)
	if err != nil {
		return nil, err
	}

	var res []os.FileInfo
	for _, entry := range dirContent {
		fi := entryFileInfo{entry: entry}
		res = append(res, fi)
	}
	return res, nil
}

func (f *FS) MkdirAll(filename string, perm os.FileMode) error {
	ctx := context.Background()
	parts := strings.Split(filepath.Clean(filename), "/")
	currentID := f.root
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		info, err := f.fsrv.Lookup(ctx, currentID, part)
		if err != nil {
			err = f.fsrv.CreateEntry(ctx, currentID, part, filetree.DirectoryKind, "", nil, nil)
			if err != nil {
				return err
			}
			info, err = f.fsrv.Lookup(ctx, currentID, part)
			if err != nil {
				return err
			}
			smode := strconv.FormatUint(uint64(perm&07777), 8)
			_, _ = f.fsrv.SetAttributes(ctx, info.Node, files.EntryAttributes{Mode: &smode})
		} else if info.Kind != string(filetree.DirectoryKind) {
			return os.ErrExist
		}
		currentID = info.Node
	}
	return nil
}

func (f *FS) Lstat(filename string) (os.FileInfo, error) {
	return f.Stat(filename) // invariant files service lookup automatically resolves names, there is no separate lstat versus stat for basic attrs right now
}

func (f *FS) Symlink(target, link string) error {
	ctx := context.Background()
	parentID, base, err := f.resolveParent(ctx, link)
	if err != nil {
		return err
	}
	return f.fsrv.CreateEntry(ctx, parentID, base, filetree.SymbolicLinkKind, target, nil, nil)
}

func (f *FS) Readlink(link string) (string, error) {
	ctx := context.Background()
	info, err := f.resolve(ctx, link)
	if err != nil {
		return "", err
	}
	if info.Kind != string(filetree.SymbolicLinkKind) {
		return "", errors.New("not a link")
	}

	// Try reading content
	r, err := f.fsrv.ReadFile(ctx, info.Node, 0, 0)
	if err != nil {
		return "", err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	return string(data), err
}

func (f *FS) Chroot(path string) (billy.Filesystem, error) {
	ctx := context.Background()
	info, err := f.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	return NewFS(f.fsrv, info.Node), nil
}

func (f *FS) Root() string {
	return "/"
}

func (f *FS) Chmod(name string, mode os.FileMode) error {
	ctx := context.Background()
	info, err := f.resolve(ctx, name)
	if err != nil {
		return err
	}
	smode := strconv.FormatUint(uint64(mode&07777), 8)
	_, err = f.fsrv.SetAttributes(ctx, info.Node, files.EntryAttributes{Mode: &smode})
	return err
}

func (f *FS) Lchown(name string, uid, gid int) error {
	return nil // Not supported by invariant files protocol
}

func (f *FS) Chown(name string, uid, gid int) error {
	return nil // Not supported by invariant files protocol
}

func (f *FS) Chtimes(name string, atime time.Time, mtime time.Time) error {
	ctx := context.Background()
	info, err := f.resolve(ctx, name)
	if err != nil {
		return err
	}

	var update files.EntryAttributes
	if !mtime.IsZero() {
		sec := uint64(mtime.Unix())
		update.ModifyTime = &sec
	}
	_, err = f.fsrv.SetAttributes(ctx, info.Node, update)
	return err
}

// ----------------------------------------------------
// FileInfo mapped from GetInfo + GetAttributes
// ----------------------------------------------------
type fileInfo struct {
	info  files.ContentInformationCommon
	attrs files.EntryAttributes
	name  string
}

func (fi fileInfo) Name() string {
	if fi.name == "" {
		return "/"
	}
	return fi.name
}

func (fi fileInfo) Size() int64 {
	if fi.attrs.Size != nil {
		return int64(*fi.attrs.Size)
	}
	return 0
}

func (fi fileInfo) Mode() fs.FileMode {
	var mode fs.FileMode
	switch fi.info.Kind {
	case string(filetree.DirectoryKind):
		mode |= fs.ModeDir
	case string(filetree.SymbolicLinkKind):
		mode |= fs.ModeSymlink
	}

	if fi.attrs.Mode != nil {
		parsed, _ := strconv.ParseUint(*fi.attrs.Mode, 8, 32)
		mode |= fs.FileMode(parsed)
	} else {
		if fi.info.Kind == string(filetree.DirectoryKind) {
			mode |= 0755
		} else {
			mode |= 0644
		}
	}
	return mode
}

func (fi fileInfo) ModTime() time.Time {
	if fi.attrs.ModifyTime != nil {
		return time.Unix(int64(*fi.attrs.ModifyTime), 0)
	}
	if fi.attrs.CreateTime != nil {
		return time.Unix(int64(*fi.attrs.CreateTime), 0)
	}
	return time.Now()
}

func (fi fileInfo) IsDir() bool {
	return fi.info.Kind == string(filetree.DirectoryKind)
}

func (fi fileInfo) Sys() any {
	return fi.info
}

// ----------------------------------------------------
// FileInfo mapped from filetree.Entry during ReadDirectory
// ----------------------------------------------------
type entryFileInfo struct {
	entry filetree.Entry
}

func (efi entryFileInfo) Name() string {
	return efi.entry.GetName()
}

func (efi entryFileInfo) Size() int64 {
	switch e := efi.entry.(type) {
	case *filetree.FileEntry:
		return int64(e.Size)
	case *filetree.DirectoryEntry:
		return int64(e.Size)
	}
	return 0
}

func (efi entryFileInfo) Mode() fs.FileMode {
	var mode fs.FileMode
	switch efi.entry.GetKind() {
	case filetree.DirectoryKind:
		mode |= fs.ModeDir
	case filetree.SymbolicLinkKind:
		mode |= fs.ModeSymlink
	}

	var modeStr *string
	switch e := efi.entry.(type) {
	case *filetree.FileEntry:
		modeStr = e.Mode
	case *filetree.DirectoryEntry:
		modeStr = e.Mode
	case *filetree.SymbolicLinkEntry:
		modeStr = e.Mode
	}

	if modeStr != nil {
		parsed, _ := strconv.ParseUint(*modeStr, 8, 32)
		mode |= fs.FileMode(parsed)
	} else {
		if efi.entry.GetKind() == filetree.DirectoryKind {
			mode |= 0755
		} else {
			mode |= 0644
		}
	}

	return mode
}

func (efi entryFileInfo) ModTime() time.Time {
	var mtime *uint64
	var ctime *uint64

	switch e := efi.entry.(type) {
	case *filetree.FileEntry:
		mtime = e.ModifyTime
		ctime = e.CreateTime
	case *filetree.DirectoryEntry:
		mtime = e.ModifyTime
		ctime = e.CreateTime
	case *filetree.SymbolicLinkEntry:
		mtime = e.ModifyTime
		ctime = e.CreateTime
	}

	if mtime != nil {
		return time.Unix(int64(*mtime), 0)
	}
	if ctime != nil {
		return time.Unix(int64(*ctime), 0)
	}
	return time.Now()
}

func (efi entryFileInfo) IsDir() bool {
	return efi.entry.GetKind() == filetree.DirectoryKind
}

func (efi entryFileInfo) Sys() any {
	return efi.entry
}
