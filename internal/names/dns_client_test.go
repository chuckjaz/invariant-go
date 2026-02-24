package names

import (
	"context"
	"net"
	"reflect"
	"testing"
)

type mockResolver struct {
	records map[string][]string
	err     error
}

func (m *mockResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	if records, ok := m.records[name]; ok {
		return records, nil
	}
	return nil, &net.DNSError{IsNotFound: true, Name: name}
}

func TestDNSClientGet(t *testing.T) {
	tests := []struct {
		name        string
		queryName   string
		resolver    *mockResolver
		expected    NameEntry
		expectedErr error
	}{
		{
			name:      "valid record with tokens",
			queryName: "test.example.com",
			resolver: &mockResolver{
				records: map[string][]string{
					"test.example.com": {"v=spf1 include:_spf.example.com ~all", "invariant:1234567890abcdef1234567890abcdef;names-v1,storage-v1"},
				},
			},
			expected: NameEntry{
				Value:  "1234567890abcdef1234567890abcdef",
				Tokens: []string{"names-v1", "storage-v1"},
			},
			expectedErr: nil,
		},
		{
			name:      "valid record without tokens",
			queryName: "block.example.com",
			resolver: &mockResolver{
				records: map[string][]string{
					"block.example.com": {"invariant:abcdef1234567890abcdef1234567890;block-v1"},
				},
			},
			expected: NameEntry{
				Value:  "abcdef1234567890abcdef1234567890",
				Tokens: []string{"block-v1"},
			},
			expectedErr: nil,
		},
		{
			name:      "no invariant record",
			queryName: "other.example.com",
			resolver: &mockResolver{
				records: map[string][]string{
					"other.example.com": {"v=spf1 include:_spf.example.com ~all"},
				},
			},
			expected:    NameEntry{},
			expectedErr: ErrNotFound,
		},
		{
			name:      "domain not found",
			queryName: "missing.example.com",
			resolver: &mockResolver{
				records: map[string][]string{},
			},
			expected:    NameEntry{},
			expectedErr: ErrNotFound,
		},
		{
			name:      "empty invariant record",
			queryName: "empty.example.com",
			resolver: &mockResolver{
				records: map[string][]string{
					"empty.example.com": {"invariant:"},
				},
			},
			expected:    NameEntry{},
			expectedErr: ErrNotFound,
		},
		{
			name:      "multiple tokens with spaces",
			queryName: "spaces.example.com",
			resolver: &mockResolver{
				records: map[string][]string{
					"spaces.example.com": {"invariant:myvalue;token1,token2"},
				},
			},
			expected: NameEntry{
				Value:  "myvalue",
				Tokens: []string{"token1", "token2"},
			},
			expectedErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewDNSClient(tt.resolver)
			entry, err := client.Get(tt.queryName)

			if err != tt.expectedErr {
				t.Fatalf("expected error: %v, got: %v", tt.expectedErr, err)
			}

			if !reflect.DeepEqual(entry, tt.expected) {
				t.Errorf("expected entry: %+v, got: %+v", tt.expected, entry)
			}
		})
	}
}

func TestDNSClientUnsupportedMethods(t *testing.T) {
	client := NewDNSClient(&mockResolver{})

	err := client.Put("test", "value", []string{"token1"})
	if err != ErrNotSupported {
		t.Errorf("expected Put to return ErrNotSupported, got: %v", err)
	}

	err = client.Delete("test", "value")
	if err != ErrNotSupported {
		t.Errorf("expected Delete to return ErrNotSupported, got: %v", err)
	}
}
