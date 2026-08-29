package commit

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
	"invariant/internal/repository/identity"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// Commit represents an immutable snapshot and history node in an Invariant repository.
type Commit struct {
	// Tree is the content link pointing to the root file tree of this commit snapshot.
	Tree content.ContentLink `json:"tree"`

	// Parents contains the SHA256 hashes of parent commit(s).
	Parents []string `json:"parents,omitempty"`

	// Author is the identity of the commit author.
	Author identity.Identity `json:"author"`

	// Committer is the identity of the committer.
	Committer identity.Identity `json:"committer"`

	// Message is the commit message description.
	Message string `json:"message"`

	// Timestamp is the Unix timestamp (seconds) when the commit was created.
	Timestamp int64 `json:"timestamp"`

	// Tags is an arbitrary string-to-string map for metadata tags and labels.
	Tags map[string]string `json:"tags,omitempty"`

	// Refs maps reference names to commit SHA256 hashes.
	Refs map[string]string `json:"refs,omitempty"`
}

// CanonicalTag represents a key-value pair for deterministic serialization.
type CanonicalTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// CanonicalRef represents a key-value ref mapping for deterministic serialization.
type CanonicalRef struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

// CanonicalCommit represents the normalized struct used for deterministic commit hashing.
type CanonicalCommit struct {
	Tree      content.ContentLink `json:"tree"`
	Parents   []string            `json:"parents,omitempty"`
	Author    identity.Identity   `json:"author"`
	Committer identity.Identity   `json:"committer"`
	Message   string              `json:"message"`
	Timestamp int64               `json:"timestamp"`
	Tags      []CanonicalTag      `json:"tags,omitempty"`
	Refs      []CanonicalRef      `json:"refs,omitempty"`
}

// SortTags returns a slice of CanonicalTag sorted alphabetically by Key.
func SortTags(tags map[string]string) []CanonicalTag {
	if len(tags) == 0 {
		return nil
	}
	var keys []string
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	res := make([]CanonicalTag, len(keys))
	for i, k := range keys {
		res[i] = CanonicalTag{Key: k, Value: tags[k]}
	}
	return res
}

// SortRefs returns a slice of CanonicalRef sorted alphabetically by Name.
func SortRefs(refs map[string]string) []CanonicalRef {
	if len(refs) == 0 {
		return nil
	}
	var names []string
	for n := range refs {
		names = append(names, n)
	}
	sort.Strings(names)
	res := make([]CanonicalRef, len(names))
	for i, n := range names {
		res[i] = CanonicalRef{Name: n, Hash: refs[n]}
	}
	return res
}

// CanonicalCommitJSON formats a Commit into deterministic canonical JSON bytes.
func CanonicalCommitJSON(c *Commit) ([]byte, error) {
	canonical := CanonicalCommit{
		Tree:      c.Tree,
		Parents:   c.Parents,
		Author:    c.Author,
		Committer: c.Committer,
		Message:   c.Message,
		Timestamp: c.Timestamp,
		Tags:      SortTags(c.Tags),
		Refs:      SortRefs(c.Refs),
	}
	return json.Marshal(canonical)
}

// CalculateCommitHash computes the deterministic SHA256 hex hash of a Commit.
func CalculateCommitHash(c *Commit) (string, error) {
	data, err := CanonicalCommitJSON(c)
	if err != nil {
		return "", fmt.Errorf("failed to canonicalize commit: %w", err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// WriteCommit serializes and writes a Commit into CAS storage and returns its deterministic SHA-256 hash address.
func WriteCommit(ctx context.Context, store storage.Storage, c *Commit) (string, error) {
	data, err := CanonicalCommitJSON(c)
	if err != nil {
		return "", fmt.Errorf("failed to canonicalize commit: %w", err)
	}

	link, err := content.Write(bytes.NewReader(data), store, content.WriterOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to store commit in CAS: %w", err)
	}

	return link.Address, nil
}

// ReadCommit reads and deserializes a Commit from CAS storage by its content address hash.
func ReadCommit(ctx context.Context, store storage.Storage, slotsClient slots.Slots, address string) (*Commit, error) {
	link := content.ContentLink{Address: address}
	reader, err := content.Read(link, store, slotsClient)
	if err != nil {
		return nil, fmt.Errorf("failed to read commit at address %s: %w", address, err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read commit data: %w", err)
	}

	var canonical CanonicalCommit
	if err := json.Unmarshal(data, &canonical); err != nil {
		// Fallback to standard Commit JSON unmarshal
		var fallback Commit
		if errFallback := json.Unmarshal(data, &fallback); errFallback != nil {
			return nil, fmt.Errorf("failed to unmarshal commit: %w (fallback err: %v)", err, errFallback)
		}
		return &fallback, nil
	}

	tagsMap := make(map[string]string)
	for _, t := range canonical.Tags {
		tagsMap[t.Key] = t.Value
	}
	refsMap := make(map[string]string)
	for _, r := range canonical.Refs {
		refsMap[r.Name] = r.Hash
	}

	return &Commit{
		Tree:      canonical.Tree,
		Parents:   canonical.Parents,
		Author:    canonical.Author,
		Committer: canonical.Committer,
		Message:   canonical.Message,
		Timestamp: canonical.Timestamp,
		Tags:      tagsMap,
		Refs:      refsMap,
	}, nil
}
