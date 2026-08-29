package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// LayerDependency represents a pinned sub-repository layer in a workspace.
type LayerDependency struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Commit     string `json:"commit,omitempty"`
}

// RepositoryConfig represents the configuration stored for a repository.
type RepositoryConfig struct {
	DefaultBranch  string            `json:"defaultBranch"` // e.g., "main"
	MainSlotID     string            `json:"mainSlotId"`    // Slot ID backing main branch
	Encrypted      bool              `json:"encrypted,omitempty"`
	Compressed     bool              `json:"compressed,omitempty"`
	WriteTag       string            `json:"writeTag,omitempty"`
	ReviewRequired bool              `json:"reviewRequired,omitempty"`
	Layers         []LayerDependency `json:"layers,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
	PeerBranches   map[string]string `json:"peerBranches,omitempty"`
	Settings       map[string]string `json:"settings,omitempty"`
	CreatedAt      int64             `json:"createdAt"`
}

// WriteRepositoryConfig serializes and writes a RepositoryConfig into CAS storage.
func WriteRepositoryConfig(ctx context.Context, store storage.Storage, cfg *RepositoryConfig) (string, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal repository config: %w", err)
	}
	link, err := content.Write(bytes.NewReader(data), store, content.WriterOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to store repository config: %w", err)
	}
	return link.Address, nil
}

// ReadRepositoryConfig reads and deserializes a RepositoryConfig from CAS storage.
func ReadRepositoryConfig(ctx context.Context, store storage.Storage, slotsClient slots.Slots, address string) (*RepositoryConfig, error) {
	link := content.ContentLink{Address: address}
	reader, err := content.Read(link, store, slotsClient)
	if err != nil {
		return nil, fmt.Errorf("failed to read repository config at %s: %w", address, err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read repository config data: %w", err)
	}

	var cfg RepositoryConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal repository config: %w", err)
	}
	return &cfg, nil
}
