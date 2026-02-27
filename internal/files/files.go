package files

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"invariant/internal/content"
	"invariant/internal/filetree"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// Options configuring the Files service.
type Options struct {
	Slots            slots.Slots
	Storage          storage.Storage
	RootLink         content.ContentLink
	AutoSyncTimeout  time.Duration
	SlotPollInterval time.Duration
	WriterOptions    content.WriterOptions
}

// Service represents the files service.
type Service struct {
	opts Options

	mu    sync.RWMutex
	nodes map[uint64]*Node
	root  uint64
	next  uint64

	// Sync state
	dirtyNodes map[uint64]bool

	// Slots polling state
	lastSlotAddress string

	// Background task context
	ctx    context.Context
	cancel context.CancelFunc
}

// Node represents a single entry in the file tree.
type Node struct {
	ID     uint64
	Name   string
	Kind   filetree.EntryKind
	Parent uint64

	// Metadata
	CreateTime *uint64
	ModifyTime *uint64
	Mode       *string
	Size       uint64
	Type       string

	// For files/directories that haven't been modified, point to origin content
	Content content.ContentLink

	// For directories: map of name to child node ID
	Children map[string]uint64

	// For symbolic links
	Target string

	// State flags
	IsDirty  bool
	IsLoaded bool // For directories: whether children have been fetched
}

// NewService creates a new Files service.
func NewService(opts Options) (*Service, error) {
	if opts.Storage == nil {
		return nil, errors.New("storage client is required")
	}

	if opts.AutoSyncTimeout == 0 {
		opts.AutoSyncTimeout = time.Minute
	}
	if opts.SlotPollInterval == 0 {
		opts.SlotPollInterval = 5 * time.Minute
	}

	ctx, cancel := context.WithCancel(context.Background())

	s := &Service{
		opts:       opts,
		nodes:      make(map[uint64]*Node),
		root:       1,
		next:       2,
		dirtyNodes: make(map[uint64]bool),
		ctx:        ctx,
		cancel:     cancel,
	}

	// Initialize the root node based on the provided RootLink
	now := uint64(time.Now().Unix())
	rootNode := &Node{
		ID:         1,
		Name:       "",
		Kind:       filetree.DirectoryKind,
		CreateTime: &now,
		ModifyTime: &now,
		Content:    opts.RootLink,
		Children:   make(map[string]uint64),
		IsLoaded:   false,
	}

	if opts.RootLink.Address == "" {
		// Empty root
		rootNode.IsLoaded = true
	}

	s.nodes[1] = rootNode

	// Start background tasks
	go s.autoSyncLoop()
	if opts.Slots != nil && opts.RootLink.Slot {
		go s.pollSlotLoop()
	}

	return s, nil
}

// Close stops the background tasks.
func (s *Service) Close() {
	s.cancel()
}

// Helpers for node management (requires caller to hold lock)

func (s *Service) getNextID() uint64 {
	id := s.next
	s.next++
	return id
}

func (s *Service) markDirty(id uint64) {
	s.dirtyNodes[id] = true
	if node, ok := s.nodes[id]; ok {
		node.IsDirty = true
		now := uint64(time.Now().Unix())
		node.ModifyTime = &now
		if node.Parent != 0 {
			s.markDirty(node.Parent)
		}
	}
}

// loadDirectory reads the directory content from storage and populates children
func (s *Service) loadDirectory(id uint64) error {
	node, ok := s.nodes[id]
	if !ok || node.Kind != filetree.DirectoryKind {
		return errors.New("invalid directory node")
	}

	if node.IsLoaded {
		return nil
	}

	if node.Content.Address == "" {
		node.IsLoaded = true
		return nil
	}

	reader, err := content.Read(node.Content, s.opts.Storage, s.opts.Slots)
	if err != nil {
		return fmt.Errorf("failed to create reader for directory %d: %w", id, err)
	}

	// ... reading directory ... (to be implemented)
	_ = reader

	node.IsLoaded = true
	return nil
}

func parseNodeID(nodeStr string) (uint64, error) {
	nodeStr = strings.TrimPrefix(nodeStr, "/")
	nodeID, err := strconv.ParseUint(nodeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid node ID: %v", err)
	}
	return nodeID, nil
}
