// Package repository provides core types, commit graph models, naming schemas,
// and service interfaces for the Invariant Repository (ir) version control system.
package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// Identity captures user identity and authorization credentials.
type Identity struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	Token string `json:"token,omitempty"`
}

// Commit represents an immutable snapshot and history node in an Invariant repository.
type Commit struct {
	// Tree is the content link pointing to the root file tree of this commit snapshot.
	Tree content.ContentLink `json:"tree"`

	// Parents contains the SHA256 hashes of parent commit(s).
	Parents []string `json:"parents,omitempty"`

	// Author is the identity of the commit author.
	Author Identity `json:"author"`

	// Committer is the identity of the committer.
	Committer Identity `json:"committer"`

	// Message is the commit message description.
	Message string `json:"message"`

	// Timestamp is the Unix timestamp (seconds) when the commit was created.
	Timestamp int64 `json:"timestamp"`

	// Tags is an arbitrary string-to-string map for metadata tags and labels.
	Tags map[string]string `json:"tags,omitempty"`

	// Refs maps reference names to commit SHA256 hashes.
	Refs map[string]string `json:"refs,omitempty"`
}

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
	Settings       map[string]string `json:"settings,omitempty"`
	CreatedAt      int64             `json:"createdAt"`
}

// Comment represents a single comment entry in a review thread.
type Comment struct {
	Comment string `json:"comment"`
	Author  string `json:"author,omitempty"`
	Branch  string `json:"branch,omitempty"`
}

// ReviewComment represents a targeted review comment thread on a file or line range.
type ReviewComment struct {
	Comments  []Comment `json:"comments"`
	File      string    `json:"file,omitempty"`
	Offset    *int      `json:"offset,omitempty"`
	Len       *int      `json:"len,omitempty"`
	StartLine *int      `json:"startLine,omitempty"`
	EndLine   *int      `json:"endLine,omitempty"`
	Resolved  bool      `json:"resolved,omitempty"`
}

// ReviewStatus represents the state of a code review.
type ReviewStatus string

const (
	ReviewStatusPending   ReviewStatus = "pending"
	ReviewStatusApproved  ReviewStatus = "approved"
	ReviewStatusRejected  ReviewStatus = "rejected"
	ReviewStatusAbandoned ReviewStatus = "abandoned"
)

// ReviewRecord holds the complete metadata and comment threads for a code review.
type ReviewRecord struct {
	Token        string          `json:"token"`
	RepoName     string          `json:"repoName"`
	BranchName   string          `json:"branchName"`
	ChangeSlotID string          `json:"changeSlotId"`
	BaseCommit   string          `json:"baseCommit"`
	HeadCommit   string          `json:"headCommit"`
	Status       ReviewStatus    `json:"status"`
	Reviewer     string          `json:"reviewer,omitempty"`
	Comments     []ReviewComment `json:"comments,omitempty"`
	CreatedAt    int64           `json:"createdAt"`
	UpdatedAt    int64           `json:"updatedAt"`
}

// BlameLine captures line-by-line commit attribution.
type BlameLine struct {
	LineNumber int      `json:"lineNumber"`
	Content    string   `json:"content"`
	CommitHash string   `json:"commitHash"`
	Author     Identity `json:"author"`
	Timestamp  int64    `json:"timestamp"`
}

// DiffStat summarizes the count of files changed and lines inserted or deleted.
type DiffStat struct {
	FilesChanged int      `json:"filesChanged"`
	Insertions   int      `json:"insertions"`
	Deletions    int      `json:"deletions"`
	Details      []string `json:"details,omitempty"`
}

// RebaseActionType specifies the action type in an interactive rebase plan.
type RebaseActionType string

const (
	RebasePick   RebaseActionType = "pick"
	RebaseSquash RebaseActionType = "squash"
	RebaseEdit   RebaseActionType = "edit"
	RebaseDrop   RebaseActionType = "drop"
	RebaseReword RebaseActionType = "reword"
)

// RebaseAction specifies an individual operation in an interactive rebase plan.
type RebaseAction struct {
	Type       RebaseActionType `json:"type"`
	CommitHash string           `json:"commitHash"`
	NewMessage string           `json:"newMessage,omitempty"`
}

// CanonicalCommitJSON marshals a Commit struct into deterministic canonical JSON.
func CanonicalCommitJSON(c *Commit) ([]byte, error) {
	// Normalize nil maps/slices for canonical encoding
	clone := *c
	if clone.Parents == nil {
		clone.Parents = []string{}
	}
	if clone.Tags == nil {
		clone.Tags = make(map[string]string)
	}
	if clone.Refs == nil {
		clone.Refs = make(map[string]string)
	}

	data, err := json.Marshal(clone)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal commit canonically: %w", err)
	}
	return data, nil
}

// CalculateCommitHash calculates the SHA256 hex digest of a commit's canonical JSON.
func CalculateCommitHash(c *Commit) (string, error) {
	data, err := CanonicalCommitJSON(c)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// WriteCommit writes a commit to Invariant CAS storage and returns its SHA256 hash.
func WriteCommit(ctx context.Context, store storage.Storage, c *Commit) (string, error) {
	data, err := CanonicalCommitJSON(c)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	addr := hex.EncodeToString(hash[:])

	stored, err := store.StoreAt(ctx, addr, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to store commit %s in CAS: %w", addr, err)
	}
	if !stored {
		return "", fmt.Errorf("storage rejected commit %s", addr)
	}
	return addr, nil
}

// ReadCommit reads and unmarshals a commit object from Invariant CAS storage by hash.
func ReadCommit(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitHash string) (*Commit, error) {
	if commitHash == "" {
		return nil, fmt.Errorf("empty commit hash")
	}

	link := content.ContentLink{Address: commitHash}
	r, err := content.Read(link, store, slotsClient)
	if err != nil {
		return nil, fmt.Errorf("failed to read commit %s: %w", commitHash, err)
	}
	defer r.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read commit content for %s: %w", commitHash, err)
	}

	var commit Commit
	if err := json.Unmarshal(data, &commit); err != nil {
		return nil, fmt.Errorf("failed to unmarshal commit %s: %w", commitHash, err)
	}
	return &commit, nil
}

// WriteRepositoryConfig writes a RepositoryConfig to Invariant CAS storage.
func WriteRepositoryConfig(ctx context.Context, store storage.Storage, cfg *RepositoryConfig) (string, error) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal repository config: %w", err)
	}
	hash := sha256.Sum256(data)
	addr := hex.EncodeToString(hash[:])

	stored, err := store.StoreAt(ctx, addr, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to store repository config %s in CAS: %w", addr, err)
	}
	if !stored {
		return "", fmt.Errorf("storage rejected repository config %s", addr)
	}
	return addr, nil
}

// ReadRepositoryConfig reads and unmarshals a RepositoryConfig from Invariant CAS storage.
func ReadRepositoryConfig(ctx context.Context, store storage.Storage, slotsClient slots.Slots, configHash string) (*RepositoryConfig, error) {
	if configHash == "" {
		return nil, fmt.Errorf("empty repository config hash")
	}

	link := content.ContentLink{Address: configHash}
	r, err := content.Read(link, store, slotsClient)
	if err != nil {
		return nil, fmt.Errorf("failed to read repository config %s: %w", configHash, err)
	}
	defer r.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read repository config content for %s: %w", configHash, err)
	}

	var cfg RepositoryConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal repository config %s: %w", configHash, err)
	}
	return &cfg, nil
}

// SortTags returns the keys of a commit's tags in alphabetical order.
func (c *Commit) SortTags() []string {
	keys := make([]string, 0, len(c.Tags))
	for k := range c.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SortRefs returns the keys of a commit's refs in alphabetical order.
func (c *Commit) SortRefs() []string {
	keys := make([]string, 0, len(c.Refs))
	for k := range c.Refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
