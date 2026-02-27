package filetree

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"invariant/internal/content"
)

// EntryKind represents the kind of a directory entry.
type EntryKind string

const (
	FileKind         EntryKind = "File"
	DirectoryKind    EntryKind = "Directory"
	SymbolicLinkKind EntryKind = "SymbolicLink"
)

// Entry is a common interface for all file tree entries.
type Entry interface {
	GetKind() EntryKind
	GetName() string
	Validate() error
}

// BaseEntry contains fields common to all entries.
type BaseEntry struct {
	Kind       EntryKind `json:"kind"`
	Name       string    `json:"name"`
	CreateTime *uint64   `json:"createTime,omitempty"`
	ModifyTime *uint64   `json:"modifyTime,omitempty"`
	Mode       *string   `json:"mode,omitempty"`
}

// FileEntry represents a file in the directory tree.
type FileEntry struct {
	BaseEntry
	Content content.ContentLink `json:"content"`
	Size    uint64              `json:"size"`
	Type    string              `json:"type,omitempty"`
}

// DirectoryEntry represents a directory in the directory tree.
type DirectoryEntry struct {
	BaseEntry
	Content content.ContentLink `json:"content"`
	Size    uint64              `json:"size"`
}

// SymbolicLinkEntry represents a symbolic link in the directory tree.
type SymbolicLinkEntry struct {
	BaseEntry
	Target string `json:"target"`
}

// Directory is an array of entries.
type Directory []Entry

// GetKind returns the entry kind.
func (b *BaseEntry) GetKind() EntryKind { return b.Kind }

// GetName returns the entry name.
func (b *BaseEntry) GetName() string { return b.Name }

var (
	dosSpecialNames = map[string]bool{
		"CON": true, "PRN": true, "AUX": true, "NUL": true,
		"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
		"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	}
	octalRegex = regexp.MustCompile(`^0[0-7]{1,4}$`)
	mimeRegex  = regexp.MustCompile(`^[^/]+/[^/]+$`)
)

func isValidName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsRune(name, '/') {
		return false
	}
	upperName := strings.ToUpper(name)
	if dotIndex := strings.IndexByte(upperName, '.'); dotIndex != -1 {
		upperName = upperName[:dotIndex]
	}
	if dosSpecialNames[upperName] {
		return false
	}
	return true
}

func (b *BaseEntry) validateBase() error {
	if !isValidName(b.Name) {
		return fmt.Errorf("invalid name: %q", b.Name)
	}
	if b.Mode != nil && !octalRegex.MatchString(*b.Mode) {
		return fmt.Errorf("invalid mode: %q", *b.Mode)
	}
	return nil
}

// Validate checks if the FileEntry follows the FileTree specifications.
func (e *FileEntry) Validate() error {
	if err := e.validateBase(); err != nil {
		return err
	}
	if e.Kind != FileKind {
		return fmt.Errorf("invalid kind for FileEntry: %v", e.Kind)
	}
	if e.Type != "" && !mimeRegex.MatchString(e.Type) {
		return fmt.Errorf("invalid mime type: %q", e.Type)
	}
	if e.Content.Address == "" {
		return errors.New("file content address is empty")
	}
	return nil
}

// Validate checks if the DirectoryEntry follows the FileTree specifications.
func (e *DirectoryEntry) Validate() error {
	if err := e.validateBase(); err != nil {
		return err
	}
	if e.Kind != DirectoryKind {
		return fmt.Errorf("invalid kind for DirectoryEntry: %v", e.Kind)
	}
	if e.Content.Address == "" {
		return errors.New("directory content address is empty")
	}
	return nil
}

// Validate checks if the SymbolicLinkEntry follows the FileTree specifications.
func (e *SymbolicLinkEntry) Validate() error {
	if err := e.validateBase(); err != nil {
		return err
	}
	if e.Kind != SymbolicLinkKind {
		return fmt.Errorf("invalid kind for SymbolicLinkEntry: %v", e.Kind)
	}
	if e.Target == "" {
		return errors.New("symbolic link target is empty")
	}
	return nil
}

// UnmarshalJSON correctly unmarshals a generic JSON array into polymorpic Entry items.
func (d *Directory) UnmarshalJSON(data []byte) error {
	var rawEntries []json.RawMessage
	if err := json.Unmarshal(data, &rawEntries); err != nil {
		return err
	}

	entries := make(Directory, 0, len(rawEntries))
	for _, raw := range rawEntries {
		var kindStruct struct {
			Kind EntryKind `json:"kind"`
		}
		if err := json.Unmarshal(raw, &kindStruct); err != nil {
			return fmt.Errorf("failed to extract kind: %w", err)
		}

		var entry Entry
		switch kindStruct.Kind {
		case FileKind:
			fe := &FileEntry{}
			if err := json.Unmarshal(raw, fe); err != nil {
				return err
			}
			entry = fe
		case DirectoryKind:
			de := &DirectoryEntry{}
			if err := json.Unmarshal(raw, de); err != nil {
				return err
			}
			entry = de
		case SymbolicLinkKind:
			sle := &SymbolicLinkEntry{}
			if err := json.Unmarshal(raw, sle); err != nil {
				return err
			}
			entry = sle
		default:
			return fmt.Errorf("unknown entry kind: %q", kindStruct.Kind)
		}
		entries = append(entries, entry)
	}
	*d = entries
	return nil
}

// MarshalJSON correctly marshals a Directory into a JSON array of polymorphic Entry items.
func (d Directory) MarshalJSON() ([]byte, error) {
	return json.Marshal([]Entry(d))
}

// Validate traverses the directory and validates all entries.
func (d Directory) Validate() error {
	for i, entry := range d {
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("entry %d (%q) invalid: %w", i, entry.GetName(), err)
		}
	}
	return nil
}
