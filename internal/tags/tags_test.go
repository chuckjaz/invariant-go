package tags

import (
	"flag"
	"slices"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		inputs   []string
		expected []string
	}{
		{
			name:     "empty inputs",
			inputs:   nil,
			expected: nil,
		},
		{
			name:     "single tag",
			inputs:   []string{"cache"},
			expected: []string{"cache"},
		},
		{
			name:     "comma separated tags",
			inputs:   []string{"cache, source, ephemeral"},
			expected: []string{"cache", "source", "ephemeral"},
		},
		{
			name:     "multiple inputs with duplicates and whitespace",
			inputs:   []string{"cache, source", "  source , backup  ", "", "cache"},
			expected: []string{"cache", "source", "backup"},
		},
		{
			name:     "empty and whitespace only elements",
			inputs:   []string{" , , ", "tag1, ,,tag2"},
			expected: []string{"tag1", "tag2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := Parse(tc.inputs...)
			if !slices.Equal(result, tc.expected) {
				t.Errorf("expected %v, got %v", tc.expected, result)
			}
		})
	}
}

func TestFlags_RegisterFlagSet(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := RegisterFlagSet(fs)

	err := fs.Parse([]string{"-tags", "cache, source", "-tag", "primary"})
	if err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	tags := f.Tags()
	expected := []string{"cache", "source", "primary"}
	if !slices.Equal(tags, expected) {
		t.Errorf("expected %v, got %v", expected, tags)
	}
}

func TestFlags_Nil(t *testing.T) {
	var f *Flags
	if tags := f.Tags(); tags != nil {
		t.Errorf("expected nil for nil Flags, got %v", tags)
	}
}
