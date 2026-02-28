package files

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"invariant/internal/content"
	"invariant/internal/filetree"
)

// InMemoryFiles represents the files service.
type InMemoryFiles struct {
	opts Options

	mu    sync.RWMutex
	nodes map[uint64]*Node
	root  uint64
	next  uint64

	dirtyNodes map[uint64]bool

	lastSlotAddress string

	ctx    context.Context
	cancel context.CancelFunc
}

// Node represents a single entry in the file tree.
type Node struct {
	ID      uint64
	Name    string
	Kind    filetree.EntryKind
	Parents map[uint64]bool

	CreateTime *uint64
	ModifyTime *uint64
	Mode       *string
	Size       uint64
	Type       string

	Content content.ContentLink

	Children map[string]uint64
	Target   string

	IsDirty  bool
	IsLoaded bool
}

// NewInMemoryFiles creates a new InMemoryFiles.
func NewInMemoryFiles(opts Options) (*InMemoryFiles, error) {
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

	s := &InMemoryFiles{
		opts:       opts,
		nodes:      make(map[uint64]*Node),
		root:       1,
		next:       2,
		dirtyNodes: make(map[uint64]bool),
		ctx:        ctx,
		cancel:     cancel,
	}

	now := uint64(time.Now().Unix())
	rootNode := &Node{
		ID:         1,
		Name:       "",
		Kind:       filetree.DirectoryKind,
		Parents:    make(map[uint64]bool),
		CreateTime: &now,
		ModifyTime: &now,
		Content:    opts.RootLink,
		Children:   make(map[string]uint64),
		IsLoaded:   false,
	}

	if opts.RootLink.Address == "" {
		rootNode.IsLoaded = true
	}

	s.nodes[1] = rootNode

	go s.autoSyncLoop()
	if opts.Slots != nil && opts.RootLink.Slot {
		go s.pollSlotLoop()
	}

	return s, nil
}

// Close stops the background tasks.
func (s *InMemoryFiles) Close() {
	s.cancel()
}

func (s *InMemoryFiles) getNextID() uint64 {
	id := s.next
	s.next++
	return id
}

func (s *InMemoryFiles) markDirty(id uint64) {
	s.dirtyNodes[id] = true
	if node, ok := s.nodes[id]; ok {
		node.IsDirty = true
		now := uint64(time.Now().Unix())
		node.ModifyTime = &now
		for parentID := range node.Parents {
			if parentID != 0 {
				s.markDirty(parentID)
			}
		}
	}
}

func (s *InMemoryFiles) isWritable() bool {
	return s.opts.Slots != nil && s.opts.RootLink.Slot
}

func (s *InMemoryFiles) ensureLoaded(id uint64) error {
	node, ok := s.nodes[id]
	if !ok {
		return fmt.Errorf("node %d not found", id)
	}

	if node.Kind != filetree.DirectoryKind {
		return fmt.Errorf("node %d is not a directory", id)
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
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("failed to read directory %d content: %w", id, err)
	}

	var d filetree.Directory
	if err := json.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("failed to unmarshal directory %d content: %w", id, err)
	}

	for _, entry := range d {
		childID := s.getNextID()
		childNode := &Node{
			ID:      childID,
			Name:    entry.GetName(),
			Kind:    entry.GetKind(),
			Parents: map[uint64]bool{id: true},
		}

		switch e := entry.(type) {
		case *filetree.FileEntry:
			childNode.CreateTime = e.CreateTime
			childNode.ModifyTime = e.ModifyTime
			childNode.Mode = e.Mode
			childNode.Content = e.Content
			childNode.Size = e.Size
			childNode.Type = e.Type
		case *filetree.DirectoryEntry:
			childNode.CreateTime = e.CreateTime
			childNode.ModifyTime = e.ModifyTime
			childNode.Mode = e.Mode
			childNode.Content = e.Content
			childNode.Size = e.Size
			childNode.Children = make(map[string]uint64)
		case *filetree.SymbolicLinkEntry:
			childNode.CreateTime = e.CreateTime
			childNode.ModifyTime = e.ModifyTime
			childNode.Mode = e.Mode
			childNode.Target = e.Target
		}

		s.nodes[childID] = childNode
		node.Children[childNode.Name] = childID
	}

	node.IsLoaded = true
	return nil
}

func (s *InMemoryFiles) deleteNodeRecursively(id uint64, parentID uint64) {
	node, ok := s.nodes[id]
	if !ok {
		return
	}
	if parentID != 0 {
		delete(node.Parents, parentID)
		if len(node.Parents) > 0 {
			return
		}
	}
	if node.Kind == filetree.DirectoryKind {
		for _, childID := range node.Children {
			s.deleteNodeRecursively(childID, id)
		}
	}
	delete(s.nodes, id)
	delete(s.dirtyNodes, id)
}

func (s *InMemoryFiles) CreateEntry(ctx context.Context, parentID uint64, name string, kind filetree.EntryKind, target string, contentLink *content.ContentLink, contentReader io.Reader) error {
	if !s.isWritable() {
		return errors.New("file system is read-only")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoaded(parentID); err != nil {
		return err
	}

	parentNode := s.nodes[parentID]
	childID := s.getNextID()
	now := uint64(time.Now().Unix())

	childNode := &Node{
		ID:         childID,
		Name:       name,
		Kind:       kind,
		Parents:    map[uint64]bool{parentID: true},
		CreateTime: &now,
		ModifyTime: &now,
	}

	switch kind {
	case filetree.FileKind, filetree.DirectoryKind:
		if contentLink != nil {
			childNode.Content = *contentLink
		} else {
			if contentReader == nil {
				contentReader = io.LimitReader(nil, 0)
			}
			link, err := content.Write(contentReader, s.opts.Storage, s.opts.WriterOptions)
			if err != nil {
				return fmt.Errorf("failed to save file: %v", err)
			}
			childNode.Content = link
		}

		if kind == filetree.DirectoryKind {
			childNode.Children = make(map[string]uint64)
			childNode.IsLoaded = true
		}

	case filetree.SymbolicLinkKind:
		if target == "" {
			return errors.New("target is required for SymbolicLink")
		}
		childNode.Target = target
	}

	s.nodes[childID] = childNode
	parentNode.Children[name] = childID
	s.markDirty(parentID)

	return nil
}

func (s *InMemoryFiles) ReadFile(ctx context.Context, nodeID uint64, offset, length int64) (io.ReadCloser, error) {
	s.mu.RLock()
	node, ok := s.nodes[nodeID]
	if !ok || node.Kind == filetree.DirectoryKind {
		s.mu.RUnlock()
		return nil, errors.New("invalid file node")
	}

	var link content.ContentLink
	if node.Kind == filetree.SymbolicLinkKind {
		s.mu.RUnlock()
		return io.NopCloser(bytes.NewReader([]byte(node.Target))), nil
	} else {
		link = node.Content
	}
	s.mu.RUnlock()

	reader, err := content.Read(link, s.opts.Storage, s.opts.Slots)
	if err != nil {
		return nil, err
	}

	if offset > 0 {
		_, err := io.CopyN(io.Discard, reader, offset)
		if err != nil && err != io.EOF {
			reader.Close()
			return nil, err
		}
	}

	if length > 0 {
		return struct {
			io.Reader
			io.Closer
		}{io.LimitReader(reader, length), reader}, nil
	}

	return reader, nil
}

func (s *InMemoryFiles) WriteFile(ctx context.Context, nodeID uint64, offset int64, appendFlag bool, r io.Reader) error {
	if !s.isWritable() {
		return errors.New("file system is read-only")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.nodes[nodeID]
	if !ok || node.Kind != filetree.FileKind {
		return errors.New("invalid file node")
	}

	link, err := content.Write(r, s.opts.Storage, s.opts.WriterOptions)
	if err != nil {
		return err
	}

	node.Content = link
	s.markDirty(nodeID)

	return nil
}

func (s *InMemoryFiles) ReadDirectory(ctx context.Context, nodeID uint64, offset, length int64) (filetree.Directory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoaded(nodeID); err != nil {
		return nil, err
	}

	node := s.nodes[nodeID]

	var entries filetree.Directory
	for name, childID := range node.Children {
		child := s.nodes[childID]
		switch child.Kind {
		case filetree.FileKind:
			entries = append(entries, &filetree.FileEntry{
				BaseEntry: filetree.BaseEntry{
					Kind:       filetree.FileKind,
					Name:       name,
					CreateTime: child.CreateTime,
					ModifyTime: child.ModifyTime,
					Mode:       child.Mode,
				},
				Content: child.Content,
				Size:    child.Size,
				Type:    child.Type,
			})
		case filetree.DirectoryKind:
			entries = append(entries, &filetree.DirectoryEntry{
				BaseEntry: filetree.BaseEntry{
					Kind:       filetree.DirectoryKind,
					Name:       name,
					CreateTime: child.CreateTime,
					ModifyTime: child.ModifyTime,
					Mode:       child.Mode,
				},
				Content: child.Content,
				Size:    child.Size,
			})
		case filetree.SymbolicLinkKind:
			entries = append(entries, &filetree.SymbolicLinkEntry{
				BaseEntry: filetree.BaseEntry{
					Kind:       filetree.SymbolicLinkKind,
					Name:       name,
					CreateTime: child.CreateTime,
					ModifyTime: child.ModifyTime,
					Mode:       child.Mode,
				},
				Target: child.Target,
			})
		}
	}

	return entries, nil
}

func (s *InMemoryFiles) GetAttributes(ctx context.Context, nodeID uint64) (EntryAttributes, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, ok := s.nodes[nodeID]
	if !ok {
		return EntryAttributes{}, errors.New("node not found")
	}

	writable := s.isWritable()
	attrs := EntryAttributes{
		Writable:   &writable,
		ModifyTime: node.ModifyTime,
		CreateTime: node.CreateTime,
		Mode:       node.Mode,
	}

	if node.Kind == filetree.FileKind {
		attrs.Size = &node.Size
		attrs.Type = &node.Type
	}

	return attrs, nil
}

func (s *InMemoryFiles) SetAttributes(ctx context.Context, nodeID uint64, attrs EntryAttributes) (EntryAttributes, error) {
	if !s.isWritable() {
		return EntryAttributes{}, errors.New("file system is read-only")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.nodes[nodeID]
	if !ok {
		return EntryAttributes{}, errors.New("node not found")
	}

	if attrs.CreateTime != nil {
		node.CreateTime = attrs.CreateTime
	}
	if attrs.ModifyTime != nil {
		node.ModifyTime = attrs.ModifyTime
	}
	if attrs.Mode != nil {
		node.Mode = attrs.Mode
	}
	if attrs.Size != nil && node.Kind == filetree.FileKind {
		node.Size = *attrs.Size
	}
	if attrs.Type != nil && node.Kind == filetree.FileKind {
		if *attrs.Type == "-" {
			node.Type = ""
		} else {
			node.Type = *attrs.Type
		}
	}

	s.markDirty(nodeID)
	return s.getAttributesLocked(nodeID)
}

func (s *InMemoryFiles) getAttributesLocked(nodeID uint64) (EntryAttributes, error) {
	node := s.nodes[nodeID]
	writable := s.isWritable()
	attrs := EntryAttributes{
		Writable:   &writable,
		ModifyTime: node.ModifyTime,
		CreateTime: node.CreateTime,
		Mode:       node.Mode,
	}

	if node.Kind == filetree.FileKind {
		attrs.Size = &node.Size
		attrs.Type = &node.Type
	}
	return attrs, nil
}

func (s *InMemoryFiles) GetContent(ctx context.Context, nodeID uint64) (content.ContentLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, ok := s.nodes[nodeID]
	if !ok || node.Kind == filetree.SymbolicLinkKind {
		return content.ContentLink{}, errors.New("invalid node")
	}

	return node.Content, nil
}

func (s *InMemoryFiles) GetInfo(ctx context.Context, nodeID uint64) (ContentInformationCommon, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, ok := s.nodes[nodeID]
	if !ok {
		return ContentInformationCommon{}, errors.New("node not found")
	}

	return s.getInfoLocked(nodeID, node)
}

func (s *InMemoryFiles) getInfoLocked(nodeID uint64, node *Node) (ContentInformationCommon, error) {
	info := ContentInformationCommon{
		Node:     nodeID,
		Kind:     string(node.Kind),
		Writable: s.isWritable(),
	}

	if node.ModifyTime != nil {
		info.ModifyTime = *node.ModifyTime
	}
	if node.CreateTime != nil {
		info.CreateTime = *node.CreateTime
	}

	if node.Content.Expected != "" {
		info.Etag = node.Content.Expected
	} else if node.Content.Address != "" {
		info.Etag = node.Content.Address
	} else {
		info.Etag = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	}

	return info, nil
}

func (s *InMemoryFiles) Lookup(ctx context.Context, parentID uint64, name string) (ContentInformationCommon, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoaded(parentID); err != nil {
		return ContentInformationCommon{}, err
	}

	parentNode, ok := s.nodes[parentID]
	if !ok || parentNode.Kind != filetree.DirectoryKind {
		return ContentInformationCommon{}, fmt.Errorf("parent directory %d not found or invalid", parentID)
	}

	childID, ok := parentNode.Children[name]
	if !ok {
		return ContentInformationCommon{}, fmt.Errorf("entry %q not found in directory %d", name, parentID)
	}

	childNode, ok := s.nodes[childID]
	if !ok {
		return ContentInformationCommon{}, fmt.Errorf("internal error: child node %d not found", childID)
	}

	return s.getInfoLocked(childNode.ID, childNode)
}

func (s *InMemoryFiles) Remove(ctx context.Context, parentID uint64, name string) error {
	if !s.isWritable() {
		return errors.New("file system is read-only")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoaded(parentID); err != nil {
		return err
	}

	parentNode := s.nodes[parentID]

	childID, ok := parentNode.Children[name]
	if !ok {
		return fmt.Errorf("entry %q not found", name)
	}

	delete(parentNode.Children, name)
	s.markDirty(parentID)
	s.deleteNodeRecursively(childID, parentID)
	return nil
}

func (s *InMemoryFiles) Rename(ctx context.Context, parentID uint64, oldName string, newParentID uint64, newName string) error {
	if !s.isWritable() {
		return errors.New("file system is read-only")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoaded(parentID); err != nil {
		return err
	}
	if err := s.ensureLoaded(newParentID); err != nil {
		return err
	}

	parentNode := s.nodes[parentID]
	newParentNode := s.nodes[newParentID]

	childID, ok := parentNode.Children[oldName]
	if !ok {
		return fmt.Errorf("entry %q not found", oldName)
	}

	if _, exists := newParentNode.Children[newName]; exists {
		// Target exists, remove it first
		targetChildID := newParentNode.Children[newName]
		delete(newParentNode.Children, newName)
		s.deleteNodeRecursively(targetChildID, newParentID)
	}

	node := s.nodes[childID]
	node.Name = newName
	delete(node.Parents, parentID)
	if node.Parents == nil {
		node.Parents = make(map[uint64]bool)
	}
	node.Parents[newParentID] = true
	now := uint64(time.Now().Unix())
	node.ModifyTime = &now

	delete(parentNode.Children, oldName)
	newParentNode.Children[newName] = childID

	s.markDirty(parentID)
	s.markDirty(newParentID)
	s.markDirty(childID)

	return nil
}

func (s *InMemoryFiles) Link(ctx context.Context, parentID uint64, name string, targetNodeID uint64) error {
	if !s.isWritable() {
		return errors.New("file system is read-only")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoaded(parentID); err != nil {
		return err
	}

	parentNode := s.nodes[parentID]
	targetNode, ok := s.nodes[targetNodeID]
	if !ok {
		return errors.New("target node not found")
	}

	parentNode.Children[name] = targetNodeID
	if targetNode.Parents == nil {
		targetNode.Parents = make(map[uint64]bool)
	}
	targetNode.Parents[parentID] = true
	s.markDirty(parentID)

	return nil
}

func (s *InMemoryFiles) Sync(ctx context.Context, nodeID uint64, wait bool) error {
	s.mu.Lock()
	if !wait {
		go func() {
			defer s.mu.Unlock()
			_ = s.writeNodeLocked(nodeID)
		}()
		return nil
	}
	defer s.mu.Unlock()
	return s.writeNodeLocked(nodeID)
}

func (s *InMemoryFiles) parseNodeID(nodeStr string) (uint64, error) {
	nodeStr = strings.TrimPrefix(nodeStr, "/")
	nodeID, err := strconv.ParseUint(nodeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid node ID: %v", err)
	}
	return nodeID, nil
}

func (s *InMemoryFiles) autoSyncLoop() {
	ticker := time.NewTicker(s.opts.AutoSyncTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.Sync(context.Background(), 1, false)
		}
	}
}

func (s *InMemoryFiles) pollSlotLoop() {
	if s.opts.Slots == nil {
		return
	}

	ticker := time.NewTicker(s.opts.SlotPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.pollSlot()
		}
	}
}

func (s *InMemoryFiles) pollSlot() {
	s.mu.Lock()
	defer s.mu.Unlock()

	address, err := s.opts.Slots.Get(s.opts.RootLink.Address)
	if err != nil {
		return
	}

	if address == s.lastSlotAddress || address == s.nodes[1].Content.Address {
		return
	}

	newRootLink := s.opts.RootLink
	newRootLink.Address = address

	reader, err := content.Read(newRootLink, s.opts.Storage, s.opts.Slots)
	if err != nil {
		return
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return
	}

	var d filetree.Directory
	if err := json.Unmarshal(data, &d); err != nil {
		return
	}

	remoteEntries := make(map[string]filetree.Entry)
	for _, entry := range d {
		remoteEntries[entry.GetName()] = entry
	}

	s.mergeRemoteIntoLocal(1, remoteEntries)
	s.lastSlotAddress = address
}

func (s *InMemoryFiles) mergeRemoteIntoLocal(localID uint64, remoteEntries map[string]filetree.Entry) {
	localNode, ok := s.nodes[localID]
	if !ok || localNode.Kind != filetree.DirectoryKind {
		return
	}

	if localNode.IsDirty {
		return
	}

	for name, childID := range localNode.Children {
		if _, exists := remoteEntries[name]; !exists {
			if !s.nodes[childID].IsDirty {
				delete(localNode.Children, name)
				s.markDirty(localID)
				s.deleteNodeRecursively(childID, localID)
			}
		}
	}

	for name, _ := range remoteEntries {
		_ = name
	}
}

func (s *InMemoryFiles) writeNodeLocked(id uint64) error {
	node, ok := s.nodes[id]
	if !ok {
		return fmt.Errorf("node %d not found", id)
	}

	if !node.IsDirty {
		return nil
	}

	if node.Kind == filetree.DirectoryKind {
		for _, childID := range node.Children {
			if err := s.writeNodeLocked(childID); err != nil {
				return err
			}
		}

		var entries filetree.Directory
		for name, childID := range node.Children {
			child := s.nodes[childID]

			switch child.Kind {
			case filetree.FileKind:
				entries = append(entries, &filetree.FileEntry{
					BaseEntry: filetree.BaseEntry{
						Kind:       filetree.FileKind,
						Name:       name,
						CreateTime: child.CreateTime,
						ModifyTime: child.ModifyTime,
						Mode:       child.Mode,
					},
					Content: child.Content,
					Size:    child.Size,
					Type:    child.Type,
				})
			case filetree.DirectoryKind:
				entries = append(entries, &filetree.DirectoryEntry{
					BaseEntry: filetree.BaseEntry{
						Kind:       filetree.DirectoryKind,
						Name:       name,
						CreateTime: child.CreateTime,
						ModifyTime: child.ModifyTime,
						Mode:       child.Mode,
					},
					Content: child.Content,
					Size:    child.Size,
				})
			case filetree.SymbolicLinkKind:
				entries = append(entries, &filetree.SymbolicLinkEntry{
					BaseEntry: filetree.BaseEntry{
						Kind:       filetree.SymbolicLinkKind,
						Name:       name,
						CreateTime: child.CreateTime,
						ModifyTime: child.ModifyTime,
						Mode:       child.Mode,
					},
					Target: child.Target,
				})
			}
		}

		data, err := entries.MarshalJSON()
		if err != nil {
			return err
		}

		opts := s.opts.WriterOptions
		if id == 1 {
			opts = applyTransformsToOptions(s.opts.RootLink.Transforms, opts)
		}

		link, err := content.Write(bytes.NewReader(data), s.opts.Storage, opts)
		if err != nil {
			return err
		}

		node.Content = link
	}

	node.IsDirty = false
	delete(s.dirtyNodes, id)

	if id == 1 && s.opts.Slots != nil && s.opts.RootLink.Slot {
		err := s.opts.Slots.Update(s.opts.RootLink.Address, node.Content.Address, s.lastSlotAddress)
		if err == nil {
			s.lastSlotAddress = node.Content.Address
		}
	}

	return nil
}

func applyTransformsToOptions(transforms []content.ContentTransform, base content.WriterOptions) content.WriterOptions {
	opts := base
	for _, t := range transforms {
		switch t.Kind {
		case "Decipher":
			if t.Algorithm == "aes-256-cbc" {
				opts.EncryptAlgorithm = "aes-256-cbc"
			}
		case "Decompress":
			opts.CompressAlgorithm = t.Algorithm
		}
	}
	return opts
}
