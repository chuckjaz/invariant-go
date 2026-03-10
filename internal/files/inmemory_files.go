package files

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

	layerDependencies map[string]bool
	lastSlotAddresses map[int]string

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

	LayerContents   map[int]content.ContentLink
	LayerMembership map[int]bool

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

	// Migrate singular root into first layer
	if len(opts.Layers) == 0 && opts.RootLink.Address != "" {
		opts.Layers = append(opts.Layers, Layer{
			RootLink: opts.RootLink,
		})
	}

	ctx, cancel := context.WithCancel(context.Background())

	s := &InMemoryFiles{
		opts:              opts,
		nodes:             make(map[uint64]*Node),
		root:              1,
		next:              2,
		dirtyNodes:        make(map[uint64]bool),
		layerDependencies: make(map[string]bool),
		lastSlotAddresses: make(map[int]string),
		ctx:               ctx,
		cancel:            cancel,
	}

	now := uint64(time.Now().Unix())

	membership := make(map[int]bool)
	contents := make(map[int]content.ContentLink)
	loaded := true

	for i, l := range opts.Layers {
		membership[i] = true
		contents[i] = l.RootLink
		if l.RootLink.Address != "" {
			loaded = false
		}
	}

	if len(opts.Layers) == 0 {
		loaded = true
	}

	rootNode := &Node{
		ID:              1,
		Name:            "",
		Kind:            filetree.DirectoryKind,
		Parents:         make(map[uint64]bool),
		CreateTime:      &now,
		ModifyTime:      &now,
		Content:         opts.RootLink,
		LayerContents:   contents,
		LayerMembership: membership,
		Children:        make(map[string]uint64),
		IsLoaded:        loaded,
	}

	s.nodes[1] = rootNode

	go s.autoSyncLoop()
	if opts.Slots != nil {
		pollSlots := false
		for _, l := range opts.Layers {
			if l.RootLink.Slot {
				pollSlots = true
				break
			}
		}
		if pollSlots || opts.RootLink.Slot {
			go s.pollSlotLoop()
		}
	}

	// Make sure we resolve any $ substitutions on startup synchronously
	// Since opts.Layers specifies the actual layer configs (minus trailing rootLink appended by applyNewLayers typically)
	// We extract it locally then zero the internal ones down allowing pure reload.
	initialLayers := opts.Layers
	// For standard configs without layers, they just want 0 rules, which means nil.
	if len(initialLayers) == 1 && initialLayers[0].RootLink.Address == opts.RootLink.Address && len(initialLayers[0].Includes) == 0 && len(initialLayers[0].Excludes) == 0 {
		initialLayers = nil
	}

	s.applyNewLayers(initialLayers)

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
	if s.opts.Slots == nil || len(s.opts.Layers) == 0 {
		return false
	}
	return s.opts.Layers[0].RootLink.Slot
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

	// For legacy support when nodes were not layer-addressed explicitly
	if len(node.LayerMembership) == 0 && node.Content.Address == "" {
		node.IsLoaded = true
		return nil
	}

	// We load each layer's directory content that this node belongs to
	for layerIdx := range node.LayerMembership {
		contentLink, ok := node.LayerContents[layerIdx]
		if !ok || contentLink.Address == "" {
			continue // This layer might not have this directory instantiated remotely yet
		}

		reader, err := content.Read(contentLink, s.opts.Storage, s.opts.Slots)
		if err != nil {
			return fmt.Errorf("failed to create reader for directory %d layer %d: %w", id, layerIdx, err)
		}

		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return fmt.Errorf("failed to read directory %d content layer %d: %w", id, layerIdx, err)
		}

		var d filetree.Directory
		if err := json.Unmarshal(data, &d); err != nil {
			return fmt.Errorf("failed to unmarshal directory %d content layer %d: %w", id, layerIdx, err)
		}

		for _, entry := range d {
			name := entry.GetName()
			var childNode *Node

			// Type switch to safely extract concrete content links from interface definitions
			var entryContent content.ContentLink
			switch e := entry.(type) {
			case *filetree.FileEntry:
				entryContent = e.Content
			case *filetree.DirectoryEntry:
				entryContent = e.Content
			}

			// Check if another layer already generated this child node here
			if existingID, exists := node.Children[name]; exists {
				childNode = s.nodes[existingID]
				childNode.LayerMembership[layerIdx] = true

				// Keep reference to this layer's content link.
				if childNode.LayerContents == nil {
					childNode.LayerContents = make(map[int]content.ContentLink)
				}
				childNode.LayerContents[layerIdx] = entryContent
				continue
			}

			childID := s.getNextID()
			childNode = &Node{
				ID:              childID,
				Name:            name,
				Kind:            entry.GetKind(),
				Parents:         map[uint64]bool{id: true},
				LayerMembership: map[int]bool{layerIdx: true},
				LayerContents:   map[int]content.ContentLink{layerIdx: entryContent},
			}

			switch e := entry.(type) {
			case *filetree.FileEntry:
				childNode.CreateTime = e.CreateTime
				childNode.ModifyTime = e.ModifyTime
				childNode.Mode = e.Mode
				childNode.Content = e.Content // Legacy compat fallback
				childNode.Size = e.Size
				childNode.Type = e.Type
			case *filetree.DirectoryEntry:
				childNode.CreateTime = e.CreateTime
				childNode.ModifyTime = e.ModifyTime
				childNode.Mode = e.Mode
				childNode.Content = e.Content // Legacy compat fallback
				childNode.Size = e.Size
				childNode.Children = make(map[string]uint64)
			case *filetree.SymbolicLinkEntry:
				childNode.CreateTime = e.CreateTime
				childNode.ModifyTime = e.ModifyTime
				childNode.Mode = e.Mode
				childNode.Target = e.Target
			}

			s.nodes[childID] = childNode
			node.Children[name] = childID
		}
	}

	node.IsLoaded = true
	return nil
}

func (s *InMemoryFiles) getFullPath(id uint64) string {
	if id == 1 {
		return "" // Root
	}

	node, ok := s.nodes[id]
	if !ok {
		return ""
	}

	if len(node.Parents) == 0 {
		return node.Name
	}

	// Assuming a single dominant parent for simple path resolution
	for parentID := range node.Parents {
		parentPath := s.getFullPath(parentID)
		if parentPath == "" {
			return node.Name
		}
		return parentPath + "/" + node.Name
	}
	return node.Name
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

	layerMembership := make(map[int]bool)
	childPath := s.getFullPath(parentID)
	if childPath == "" {
		childPath = name
	} else {
		childPath = childPath + "/" + name
	}

	for i, layer := range s.opts.Layers {
		isDir := kind == filetree.DirectoryKind

		included := len(layer.Includes) == 0
		if !included {
			included = filetree.IgnoreRules(layer.Includes).Matches(childPath, isDir)
		}

		if included && len(layer.Excludes) > 0 {
			if filetree.IgnoreRules(layer.Excludes).Matches(childPath, isDir) {
				included = false
			}
		}

		if included {
			layerMembership[i] = true

			// Implicitly include parent directories
			currParent := parentNode
			currID := parentID
			for currID != 1 && !currParent.LayerMembership[i] {
				currParent.LayerMembership[i] = true
				s.markDirty(currID)

				// Grab first parent
				for pID := range currParent.Parents {
					currID = pID
					currParent = s.nodes[currID]
					break
				}
			}
		}
	}

	childNode := &Node{
		ID:              childID,
		Name:            name,
		Kind:            kind,
		Parents:         map[uint64]bool{parentID: true},
		CreateTime:      &now,
		ModifyTime:      &now,
		LayerMembership: layerMembership,
		LayerContents:   make(map[int]content.ContentLink),
		IsDirty:         true,
	}

	switch kind {
	case filetree.FileKind, filetree.DirectoryKind:
		if contentLink != nil {
			childNode.Content = *contentLink
		} else {
			if contentReader == nil {
				contentReader = io.LimitReader(nil, 0)
			}
			data, err := io.ReadAll(contentReader)
			if err != nil {
				return fmt.Errorf("failed to read content: %v", err)
			}
			if kind == filetree.FileKind {
				childNode.Size = uint64(len(data))
			}
			link, err := content.Write(bytes.NewReader(data), s.opts.Storage, s.opts.WriterOptions)
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

	go s.checkAndReload(parentID, name)

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
		if seeker, ok := reader.(io.Seeker); ok {
			_, err := seeker.Seek(offset, io.SeekStart)
			if err != nil {
				reader.Close()
				return nil, err
			}
		} else {
			_, err := io.CopyN(io.Discard, reader, offset)
			if err != nil && err != io.EOF {
				reader.Close()
				return nil, err
			}
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

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type dynamicSkipReader struct {
	r      io.Reader
	skipFn func() int64
	done   bool
}

func (s *dynamicSkipReader) Read(p []byte) (int, error) {
	if !s.done {
		skip := s.skipFn()
		if skip > 0 {
			_, err := io.CopyN(io.Discard, s.r, skip)
			if err != nil {
				if err == io.EOF {
					s.done = true
					return 0, io.EOF
				}
				return 0, err
			}
		}
		s.done = true
	}
	return s.r.Read(p)
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

	var startOffset int64
	if appendFlag {
		startOffset = int64(node.Size)
	} else if offset > 0 {
		startOffset = offset
	} else {
		startOffset = 0
	}

	var existingReader io.ReadCloser
	if node.Content.Address != "" {
		var err error
		existingReader, err = content.Read(node.Content, s.opts.Storage, s.opts.Slots)
		if err != nil {
			return fmt.Errorf("failed to read existing content: %w", err)
		}
		defer existingReader.Close()
	}

	var parts []io.Reader

	if existingReader != nil {
		if startOffset <= int64(node.Size) {
			parts = append(parts, io.LimitReader(existingReader, startOffset))
		} else {
			parts = append(parts, existingReader)
			parts = append(parts, io.LimitReader(zeroReader{}, startOffset-int64(node.Size)))
		}
	} else if startOffset > 0 {
		parts = append(parts, io.LimitReader(zeroReader{}, startOffset))
	}

	cr := &countReader{r: r}
	parts = append(parts, cr)

	if existingReader != nil {
		parts = append(parts, &dynamicSkipReader{
			r: existingReader,
			skipFn: func() int64 {
				return cr.n
			},
		})
	}

	link, err := content.Write(io.MultiReader(parts...), s.opts.Storage, s.opts.WriterOptions)
	if err != nil {
		return err
	}

	node.Content = link
	node.Size = uint64(max(int64(node.Size), startOffset+cr.n))
	s.markDirty(nodeID)

	go s.checkAndReloadNode(nodeID)

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

	go s.checkAndReload(parentID, name)

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

	go s.checkAndReload(parentID, oldName)
	go s.checkAndReload(newParentID, newName)

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

	for i, l := range s.opts.Layers {
		if !l.RootLink.Slot {
			continue
		}

		address, err := s.opts.Slots.Get(l.RootLink.Address)
		if err != nil {
			continue
		}

		if address == s.lastSlotAddresses[i] || address == s.nodes[1].LayerContents[i].Address {
			continue
		}

		newRootLink := l.RootLink
		newRootLink.Address = address

		reader, err := content.Read(newRootLink, s.opts.Storage, s.opts.Slots)
		if err != nil {
			continue
		}

		data, err := io.ReadAll(reader)
		if err != nil {
			continue
		}

		var d filetree.Directory
		if err := json.Unmarshal(data, &d); err != nil {
			continue
		}

		remoteEntries := make(map[string]filetree.Entry)
		for _, entry := range d {
			remoteEntries[entry.GetName()] = entry
		}

		s.mergeRemoteIntoLocal(1, remoteEntries, i)
		s.lastSlotAddresses[i] = address
	}
}

func (s *InMemoryFiles) mergeRemoteIntoLocal(localID uint64, remoteEntries map[string]filetree.Entry, layerIdx int) {
	localNode, ok := s.nodes[localID]
	if !ok || localNode.Kind != filetree.DirectoryKind {
		return
	}

	if localNode.IsDirty {
		return
	}

	for name, childID := range localNode.Children {
		childNode := s.nodes[childID]
		// If child belongs to this layer and remote doesn't have it, remove it
		if childNode.LayerMembership[layerIdx] {
			if _, exists := remoteEntries[name]; !exists {
				if !childNode.IsDirty {
					delete(childNode.LayerMembership, layerIdx)
					if len(childNode.LayerMembership) == 0 {
						delete(localNode.Children, name)
						s.deleteNodeRecursively(childID, localID)
					}
					s.markDirty(localID)
				}
			}
		}
	}

	// For pulling down newly added entries dynamically, we'd theoretically construct new Nodes
	// based off remote properties matching `Layer.Includes` rules here if we wished to deeply implement
	// reactive layered synchronization from remote peers. For now we only implement polling to avoid
	// deleting explicitly layered entities.
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

		// Write a variant of the directory for each layer the directory belongs to.
		for layerIdx := range node.LayerMembership {
			var entries filetree.Directory
			for name, childID := range node.Children {
				child := s.nodes[childID]
				if !child.LayerMembership[layerIdx] {
					continue
				}

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
						Content: child.LayerContents[layerIdx], // Use layer specific content if exists
						Size:    child.Size,
						Type:    child.Type,
					})
					// Fallback for flat files without divergence
					if child.LayerContents[layerIdx].Address == "" && child.Content.Address != "" {
						last := entries[len(entries)-1].(*filetree.FileEntry)
						last.Content = child.Content
					}
				case filetree.DirectoryKind:
					entries = append(entries, &filetree.DirectoryEntry{
						BaseEntry: filetree.BaseEntry{
							Kind:       filetree.DirectoryKind,
							Name:       name,
							CreateTime: child.CreateTime,
							ModifyTime: child.ModifyTime,
							Mode:       child.Mode,
						},
						Content: child.LayerContents[layerIdx],
						Size:    child.Size, // Size is basically approximate for directories
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
				opts = applyTransformsToOptions(s.opts.Layers[layerIdx].RootLink.Transforms, opts)
			}

			link, err := content.Write(bytes.NewReader(data), s.opts.Storage, opts)
			if err != nil {
				return err
			}

			node.LayerContents[layerIdx] = link
			node.Content = link // Maintain legacy backward compat interface fallback
		}
	}

	node.IsDirty = false
	delete(s.dirtyNodes, id)

	if id == 1 && s.opts.Slots != nil {
		for layerIdx := range node.LayerMembership {
			l := s.opts.Layers[layerIdx]
			if l.RootLink.Slot {
				err := s.opts.Slots.Update(l.RootLink.Address, node.LayerContents[layerIdx].Address, s.lastSlotAddresses[layerIdx], nil)
				if err == nil {
					s.lastSlotAddresses[layerIdx] = node.LayerContents[layerIdx].Address
				}
			}
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

func (s *InMemoryFiles) checkAndReload(parentID uint64, name string) {
	if parentID == 1 && name == ".invariant-layer" {
		go s.handleLayerChange()
		return
	}
	s.mu.RLock()
	var fullPath string
	if parentPath := s.getFullPath(parentID); parentPath != "" {
		fullPath = parentPath + "/" + name
	} else {
		fullPath = name
	}
	var isDep bool
	if s.layerDependencies != nil {
		isDep = s.layerDependencies[fullPath]
	}
	s.mu.RUnlock()
	if isDep {
		go s.handleLayerChange()
	}
}

func (s *InMemoryFiles) checkAndReloadNode(nodeID uint64) {
	s.mu.RLock()
	node, ok := s.nodes[nodeID]
	var isDep bool
	if ok {
		if node.Name == ".invariant-layer" && node.Parents[1] {
			isDep = true
		} else {
			fullPath := s.getFullPath(nodeID)
			if s.layerDependencies != nil {
				isDep = s.layerDependencies[fullPath]
			}
		}
	}
	s.mu.RUnlock()

	if isDep {
		go s.handleLayerChange()
	}
}

func (s *InMemoryFiles) handleLayerChange() {
	s.mu.RLock()
	rootNode, ok := s.nodes[1]
	if !ok {
		s.mu.RUnlock()
		return
	}
	layerNodeID, childOk := rootNode.Children[".invariant-layer"]
	s.mu.RUnlock()

	if !childOk {
		s.applyNewLayers(nil)
		return
	}

	rc, err := s.ReadFile(s.ctx, layerNodeID, 0, 0)
	if err != nil {
		log.Printf("handleLayerChange ReadFile error: %v", err)
		return
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		log.Printf("handleLayerChange ReadAll error: %v", err)
		return
	}

	var layers []Layer
	if err := json.Unmarshal(data, &layers); err != nil {
		log.Printf("handleLayerChange Unmarshal error: %v, data: %s", err, string(data))
		return
	}

	s.applyNewLayers(layers)
}

func (s *InMemoryFiles) applyNewLayers(layers []Layer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var newLayers []Layer
	if len(layers) > 0 {
		newLayers = append(newLayers, layers...)
	}
	newLayers = append(newLayers, Layer{
		RootLink: s.opts.RootLink,
	})

	s.layerDependencies = make(map[string]bool)

	for i := range newLayers {
		newLayers[i].Includes = s.resolveLayerRulesLocked(newLayers[i].Includes)
		newLayers[i].Excludes = s.resolveLayerRulesLocked(newLayers[i].Excludes)
	}

	s.opts.Layers = newLayers

	membership := make(map[int]bool)
	contents := make(map[int]content.ContentLink)
	for i, l := range s.opts.Layers {
		membership[i] = true
		contents[i] = l.RootLink
	}

	rootNode, ok := s.nodes[1]
	if !ok {
		return
	}

	rootNode.LayerMembership = membership
	rootNode.LayerContents = contents
	rootNode.IsLoaded = false

	var toDelete []uint64
	for id, node := range s.nodes {
		if id == 1 {
			continue
		}
		if !node.IsDirty {
			toDelete = append(toDelete, id)
		} else if node.Kind == filetree.DirectoryKind {
			node.IsLoaded = false
		}
	}

	for _, node := range s.nodes {
		if node.Kind == filetree.DirectoryKind {
			for name, childID := range node.Children {
				if childNode, childExists := s.nodes[childID]; !childExists || !childNode.IsDirty {
					delete(node.Children, name)
				}
			}
		}
	}

	for _, id := range toDelete {
		delete(s.nodes, id)
	}
}

func (s *InMemoryFiles) resolveLayerRulesLocked(rules []string) []string {
	var resolved []string
	for _, rule := range rules {
		if !strings.HasPrefix(rule, "$") {
			resolved = append(resolved, rule)
			continue
		}

		path := strings.TrimPrefix(rule, "$")
		if path == "" {
			continue
		}

		// Ensure path doesn't start with leading slashes structurally
		path = strings.TrimPrefix(path, "/")

		s.layerDependencies[path] = true

		if s.nodes[1] == nil {
			log.Printf("root node nil")
			continue
		}

		parts := strings.Split(path, "/")
		currID := uint64(1)
		currNode := s.nodes[currID]
		found := true

		for _, part := range parts {
			if part == "" {
				continue
			}
			s.ensureLoaded(currID)
			if childID, ok := currNode.Children[part]; ok {
				currID = childID
				currNode = s.nodes[childID]
			} else {
				log.Printf("child part %q not found", part)
				found = false
				break
			}
		}

		if !found || currNode.Kind != filetree.FileKind {
			log.Printf("file not found or not filekind (found=%v, kind=%v)", found, currNode.Kind)
			continue
		}

		// We must read it bypassing mutex read locks since applyNewLayers holds s.mu.Lock(),
		// but ReadFile requires s.mu.RLock() which will deadlock if called directly.
		// Instead we directly utilize content.Read.

		// If node layer contents aren't populated natively, read standard Content
		var link content.ContentLink
		if currNode.Content.Address != "" {
			link = currNode.Content
		} else {
			// Find first concrete layer mapping if flat content is missing
			for _, layerLink := range currNode.LayerContents {
				if layerLink.Address != "" {
					link = layerLink
					break
				}
			}
		}

		if link.Address == "" {
			continue
		}

		rc, err := content.Read(link, s.opts.Storage, s.opts.Slots)
		if err != nil {
			continue
		}

		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}

		lines := strings.SplitSeq(string(data), "\n")
		for line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				resolved = append(resolved, line)
			}
		}
	}
	return resolved
}
