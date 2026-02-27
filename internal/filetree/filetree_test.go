package filetree

import (
	"encoding/json"
	"testing"

	"invariant/internal/content"
)

func ptr[T any](v T) *T {
	return &v
}

func TestEntryValidation(t *testing.T) {
	tests := []struct {
		name    string
		entry   Entry
		wantErr bool
	}{
		{
			name: "valid file entry",
			entry: &FileEntry{
				BaseEntry: BaseEntry{
					Kind:       FileKind,
					Name:       "test.txt",
					CreateTime: ptr(uint64(1000)),
					ModifyTime: ptr(uint64(1000)),
					Mode:       ptr("0644"),
				},
				Content: content.ContentLink{Address: "hash123"},
				Size:    1024,
				Type:    "text/plain",
			},
			wantErr: false,
		},
		{
			name: "invalid name (empty)",
			entry: &FileEntry{
				BaseEntry: BaseEntry{Kind: FileKind, Name: ""},
				Content:   content.ContentLink{Address: "hash123"},
			},
			wantErr: true,
		},
		{
			name: "invalid name (.)",
			entry: &FileEntry{
				BaseEntry: BaseEntry{Kind: FileKind, Name: "."},
				Content:   content.ContentLink{Address: "hash123"},
			},
			wantErr: true,
		},
		{
			name: "invalid name (..)",
			entry: &FileEntry{
				BaseEntry: BaseEntry{Kind: FileKind, Name: ".."},
				Content:   content.ContentLink{Address: "hash123"},
			},
			wantErr: true,
		},
		{
			name: "invalid name (slash)",
			entry: &FileEntry{
				BaseEntry: BaseEntry{Kind: FileKind, Name: "a/b"},
				Content:   content.ContentLink{Address: "hash123"},
			},
			wantErr: true,
		},
		{
			name: "invalid name (DOS special)",
			entry: &FileEntry{
				BaseEntry: BaseEntry{Kind: FileKind, Name: "con.txt"},
				Content:   content.ContentLink{Address: "hash123"},
			},
			wantErr: true,
		},
		{
			name: "invalid mode",
			entry: &FileEntry{
				BaseEntry: BaseEntry{Kind: FileKind, Name: "test", Mode: ptr("888")},
				Content:   content.ContentLink{Address: "hash123"},
			},
			wantErr: true,
		},
		{
			name: "invalid mime type",
			entry: &FileEntry{
				BaseEntry: BaseEntry{Kind: FileKind, Name: "test"},
				Content:   content.ContentLink{Address: "hash123"},
				Type:      "invalid-mime",
			},
			wantErr: true,
		},
		{
			name: "valid directory entry",
			entry: &DirectoryEntry{
				BaseEntry: BaseEntry{Kind: DirectoryKind, Name: "dir"},
				Content:   content.ContentLink{Address: "hash123"},
			},
			wantErr: false,
		},
		{
			name: "valid symlink entry",
			entry: &SymbolicLinkEntry{
				BaseEntry: BaseEntry{Kind: SymbolicLinkKind, Name: "link"},
				Target:    "target/path",
			},
			wantErr: false,
		},
		{
			name: "invalid symlink target",
			entry: &SymbolicLinkEntry{
				BaseEntry: BaseEntry{Kind: SymbolicLinkKind, Name: "link"},
				Target:    "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.entry.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDirectoryMarshalUnmarshal(t *testing.T) {
	original := Directory{
		&FileEntry{
			BaseEntry: BaseEntry{
				Kind:       FileKind,
				Name:       "file.txt",
				CreateTime: ptr(uint64(12345)),
				Mode:       ptr("0644"),
			},
			Content: content.ContentLink{Address: "addr1"},
			Size:    100,
			Type:    "text/plain",
		},
		&DirectoryEntry{
			BaseEntry: BaseEntry{
				Kind: DirectoryKind,
				Name: "subdir",
			},
			Content: content.ContentLink{Address: "addr2"},
			Size:    200,
		},
		&SymbolicLinkEntry{
			BaseEntry: BaseEntry{
				Kind: SymbolicLinkKind,
				Name: "symlink",
			},
			Target: "../target",
		},
	}

	// Test Validating original directory
	if err := original.Validate(); err != nil {
		t.Fatalf("Original directory failed validation: %v", err)
	}

	// Test Marshal
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Failed to marshal directory: %v", err)
	}

	// Test Unmarshal
	var unmarshaled Directory
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal directory: %v", err)
	}

	if len(unmarshaled) != len(original) {
		t.Fatalf("Unmarshaled length = %d, want %d", len(unmarshaled), len(original))
	}

	// Compare items
	for i, entry := range unmarshaled {
		if entry.GetKind() != original[i].GetKind() {
			t.Errorf("Item %d kind = %v, want %v", i, entry.GetKind(), original[i].GetKind())
		}
		if entry.GetName() != original[i].GetName() {
			t.Errorf("Item %d name = %v, want %v", i, entry.GetName(), original[i].GetName())
		}
	}
}
