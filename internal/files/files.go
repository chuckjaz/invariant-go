package files

import (
	"context"
	"io"
	"time"

	"invariant/internal/content"
	"invariant/internal/filetree"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// Files defines the interface for the files protocol
type Files interface {
	// CreateEntry creates a new file, directory, or symbolic link
	CreateEntry(ctx context.Context, parentID uint64, name string, kind filetree.EntryKind, target string, contentLink *content.ContentLink, contentReader io.Reader) error

	// ReadFile reads the content of a file
	ReadFile(ctx context.Context, nodeID uint64, offset, length int64) (io.ReadCloser, error)

	// WriteFile overwrites or appends to a file
	WriteFile(ctx context.Context, nodeID uint64, offset int64, appendFlag bool, r io.Reader) error

	// ReadDirectory reads the directory entries
	ReadDirectory(ctx context.Context, nodeID uint64, offset, length int64) (filetree.Directory, error)

	// GetAttributes gets the attributes of a node
	GetAttributes(ctx context.Context, nodeID uint64) (EntryAttributes, error)

	// SetAttributes sets the attributes of a node
	SetAttributes(ctx context.Context, nodeID uint64, attrs EntryAttributes) (EntryAttributes, error)

	// GetContent gets the content link of a file
	GetContent(ctx context.Context, nodeID uint64) (content.ContentLink, error)

	// GetInfo gets the content information of a node
	GetInfo(ctx context.Context, nodeID uint64) (ContentInformationCommon, error)

	// Lookup looks up a name in a directory
	Lookup(ctx context.Context, parentID uint64, name string) (ContentInformationCommon, error)

	// Remove removes an entry from a directory
	Remove(ctx context.Context, parentID uint64, name string) error

	// Rename renames an entry
	Rename(ctx context.Context, parentID uint64, oldName string, newParentID uint64, newName string) error

	// Link creates a hard link
	Link(ctx context.Context, parentID uint64, name string, targetNodeID uint64) error

	// Sync forces a synchronization
	Sync(ctx context.Context, nodeID uint64, wait bool) error
}

// Options configuring the internal Files service.
type Options struct {
	Slots            slots.Slots
	Storage          storage.Storage
	RootLink         content.ContentLink
	AutoSyncTimeout  time.Duration
	SlotPollInterval time.Duration
	WriterOptions    content.WriterOptions
}

// ContentInformationCommon represents the info returned by GET /info/:node
type ContentInformationCommon struct {
	Node       uint64 `json:"node"`
	Kind       string `json:"kind"`
	ModifyTime uint64 `json:"modifyTime"`
	CreateTime uint64 `json:"createTime"`
	Executable bool   `json:"executable"`
	Writable   bool   `json:"writable"`
	Etag       string `json:"etag"`
}

// EntryAttributes represents the attributes returned by GET /attributes/:node
type EntryAttributes struct {
	Writable   *bool   `json:"writable,omitempty"`
	ModifyTime *uint64 `json:"modifyTime,omitempty"`
	CreateTime *uint64 `json:"createTime,omitempty"`
	Mode       *string `json:"mode,omitempty"`
	Size       *uint64 `json:"size,omitempty"`
	Type       *string `json:"type,omitempty"`
}
