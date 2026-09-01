package files

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"invariant/internal/content"
	"invariant/internal/discovery"
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

	// GetNodeInfo returns consolidated metadata and attributes in a single fast call
	GetNodeInfo(ctx context.Context, nodeID uint64) (NodeInfo, error)

	// Lookup looks up a name in a directory
	Lookup(ctx context.Context, parentID uint64, name string) (ContentInformationCommon, error)

	// LookupNodeInfo looks up a name and returns consolidated metadata and attributes in a single fast call
	LookupNodeInfo(ctx context.Context, parentID uint64, name string) (NodeInfo, error)

	// Remove removes an entry from a directory
	Remove(ctx context.Context, parentID uint64, name string) error

	// Rename renames an entry
	Rename(ctx context.Context, parentID uint64, oldName string, newParentID uint64, newName string) error

	// Link creates a hard link
	Link(ctx context.Context, parentID uint64, name string, targetNodeID uint64) error

	// Sync forces a synchronization
	Sync(ctx context.Context, nodeID uint64, wait bool) error
}

// Layer defines a composed filetree tier with inclusion/exclusion rules.
type Layer struct {
	RootLink           content.ContentLink
	Includes           []string
	Excludes           []string
	StorageDestination string `json:"storageDestination,omitempty"`
	ReadOnly           bool   `json:"readOnly,omitempty"`
	WriteTag           string `json:"writeTag,omitempty"`

	includesMatcher *filetree.IgnoreMatcher
	excludesMatcher *filetree.IgnoreMatcher
}

type rawLayer struct {
	RootLink           json.RawMessage `json:"rootLink"`
	Includes           []string        `json:"includes,omitempty"`
	Excludes           []string        `json:"excludes,omitempty"`
	StorageDestination string          `json:"storageDestination,omitempty"`
	ReadOnly           bool            `json:"readOnly,omitempty"`
	WriteTag           string          `json:"writeTag,omitempty"`
	Tag                string          `json:"tag,omitempty"`
}

func (l *Layer) UnmarshalJSON(data []byte) error {
	var raw rawLayer
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	l.Includes = raw.Includes
	l.Excludes = raw.Excludes
	l.StorageDestination = raw.StorageDestination
	l.ReadOnly = raw.ReadOnly
	l.WriteTag = raw.WriteTag
	if l.WriteTag == "" && raw.Tag != "" {
		l.WriteTag = raw.Tag
	}

	if len(raw.RootLink) > 0 {
		var s string
		if err := json.Unmarshal(raw.RootLink, &s); err == nil && s == "temporary" {
			l.RootLink = content.ContentLink{Slot: true}
		} else {
			if err := json.Unmarshal(raw.RootLink, &l.RootLink); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l Layer) MarshalJSON() ([]byte, error) {
	raw := rawLayer{
		Includes:           l.Includes,
		Excludes:           l.Excludes,
		StorageDestination: l.StorageDestination,
		ReadOnly:           l.ReadOnly,
		WriteTag:           l.WriteTag,
	}
	if l.RootLink.Address == "" && l.RootLink.Slot {
		raw.RootLink = json.RawMessage(`"temporary"`)
	} else {
		b, err := json.Marshal(l.RootLink)
		if err != nil {
			return nil, err
		}
		raw.RootLink = b
	}
	return json.Marshal(raw)
}

// MountConfig represents the configuration and metadata for an invariant mount.
type MountConfig struct {
	InvariantMount  bool            `json:"invariant_mount"`
	CacheDir        string          `json:"cache_dir"`
	IsWorkspace     bool            `json:"is_workspace"`
	DiscoveryURL    string          `json:"discovery,omitempty"`
	RootAddr        string          `json:"root,omitempty"`
	Slot            string          `json:"slot,omitempty"`
	CacheSizeMB     int             `json:"cache_size_mb,omitempty"`
	DiskCacheSizeMB int             `json:"disk_cache_size_mb,omitempty"`
	OverflowDir     string          `json:"overflow_dir,omitempty"`
	Compress        bool            `json:"compress,omitempty"`
	Encrypt         bool            `json:"encrypt,omitempty"`
	KeyPolicyStr    string          `json:"key_policy,omitempty"`
	WorkspaceInfo   json.RawMessage `json:"workspace_info,omitempty"`
}

// Options configuring the internal Files service.
type Options struct {
	Slots            slots.Slots
	Storage          storage.Storage
	LocalStorage     storage.Storage
	Discovery        discovery.Discovery
	RootLink         content.ContentLink
	Layers           []Layer
	AutoSyncTimeout  time.Duration
	SlotPollInterval time.Duration
	WriterOptions    content.WriterOptions
	MountConfig      *MountConfig
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

// NodeInfo contains consolidated metadata and attributes for a node in a single struct.
type NodeInfo struct {
	Node       uint64 `json:"node"`
	Kind       string `json:"kind"`
	Size       uint64 `json:"size"`
	Mode       uint32 `json:"mode"`
	CreateTime uint64 `json:"createTime"`
	ModifyTime uint64 `json:"modifyTime"`
	Executable bool   `json:"executable"`
	Writable   bool   `json:"writable"`
	Etag       string `json:"etag"`
}
