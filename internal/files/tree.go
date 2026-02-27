package files

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"invariant/internal/content"
	"invariant/internal/filetree"
)

// ensureLoaded ensures the directory contents are loaded from storage
func (s *Service) ensureLoaded(id uint64) error {
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

	// Read directory content from storage
	reader, err := content.Read(node.Content, s.opts.Storage, s.opts.Slots)
	if err != nil {
		return fmt.Errorf("failed to create reader for directory %d: %w", id, err)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("failed to read directory %d content: %w", id, err)
	}

	var d filetree.Directory
	if err := json.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("failed to unmarshal directory %d content: %w", id, err)
	}

	// Populate children map and create nodes
	for _, entry := range d {
		childID := s.getNextID()
		childNode := &Node{
			ID:     childID,
			Name:   entry.GetName(),
			Kind:   entry.GetKind(),
			Parent: id,
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

func (s *Service) lookup(parentID uint64, name string) (*Node, error) {
	if err := s.ensureLoaded(parentID); err != nil {
		return nil, err
	}

	parentNode, ok := s.nodes[parentID]
	if !ok || parentNode.Kind != filetree.DirectoryKind {
		return nil, fmt.Errorf("parent directory %d not found or invalid", parentID)
	}

	childID, ok := parentNode.Children[name]
	if !ok {
		return nil, fmt.Errorf("entry %q not found in directory %d", name, parentID)
	}

	childNode, ok := s.nodes[childID]
	if !ok {
		return nil, fmt.Errorf("internal error: child node %d not found", childID)
	}

	return childNode, nil
}

func (s *Service) remove(parentID uint64, name string) error {
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

	// Optional: deeply delete from s.nodes
	s.deleteNodeRecursively(childID)
	return nil
}

func (s *Service) deleteNodeRecursively(id uint64) {
	node, ok := s.nodes[id]
	if !ok {
		return
	}
	if node.Kind == filetree.DirectoryKind {
		for _, childID := range node.Children {
			s.deleteNodeRecursively(childID)
		}
	}
	delete(s.nodes, id)
	delete(s.dirtyNodes, id)
}

func (s *Service) rename(parentID uint64, oldName string, newParentID uint64, newName string) error {
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
		if err := s.remove(newParentID, newName); err != nil {
			return err
		}
	}

	node := s.nodes[childID]
	node.Name = newName
	node.Parent = newParentID
	now := uint64(time.Now().Unix())
	node.ModifyTime = &now

	delete(parentNode.Children, oldName)
	newParentNode.Children[newName] = childID

	s.markDirty(parentID)
	s.markDirty(newParentID)
	s.markDirty(childID) // Name and potentially parent changed

	return nil
}
