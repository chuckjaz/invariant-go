package repository

import (
	"context"
	"strings"

	"invariant/internal/kv"
)

// GitKVIndex provides bidirectional object mapping between Git SHA1 and Invariant SHA256 hashes.
type GitKVIndex struct {
	kvClient kv.BatchKeyValueStore
}

// NewGitKVIndex creates a new GitKVIndex. If kvClient is nil, operations gracefully succeed without persistence.
func NewGitKVIndex(kvClient kv.BatchKeyValueStore) *GitKVIndex {
	return &GitKVIndex{
		kvClient: kvClient,
	}
}

// GetBlobInvariantSHA256 returns the Invariant SHA256 hex address for a Git blob SHA1.
func (idx *GitKVIndex) GetBlobInvariantSHA256(ctx context.Context, gitBlobSHA1 string) (string, error) {
	if idx.kvClient == nil || gitBlobSHA1 == "" {
		return "", nil
	}
	key := "SHA1:" + strings.ToLower(gitBlobSHA1)
	res, err := idx.kvClient.BatchGet(ctx, nil, []string{key})
	if err != nil {
		return "", err
	}
	if val, ok := res[key]; ok {
		return string(val.Value), nil
	}
	return "", nil
}

// GetBlobGitSHA1 returns the Git blob SHA1 for an Invariant SHA256 hex address.
func (idx *GitKVIndex) GetBlobGitSHA1(ctx context.Context, invSHA256 string) (string, error) {
	if idx.kvClient == nil || invSHA256 == "" {
		return "", nil
	}
	key := "SHA256:" + strings.ToLower(invSHA256)
	res, err := idx.kvClient.BatchGet(ctx, nil, []string{key})
	if err != nil {
		return "", err
	}
	if val, ok := res[key]; ok {
		return string(val.Value), nil
	}
	return "", nil
}

// GetTreeInvariantAddress returns the Invariant ContentLink address for a Git tree SHA1.
func (idx *GitKVIndex) GetTreeInvariantAddress(ctx context.Context, gitTreeSHA1 string) (string, error) {
	if idx.kvClient == nil || gitTreeSHA1 == "" {
		return "", nil
	}
	gSha1 := strings.ToLower(gitTreeSHA1)
	keys := []string{"tree:sha1:" + gSha1, "SHA1:" + gSha1}
	res, err := idx.kvClient.BatchGet(ctx, nil, keys)
	if err != nil {
		return "", err
	}
	if val, ok := res["tree:sha1:"+gSha1]; ok {
		return string(val.Value), nil
	}
	if val, ok := res["SHA1:"+gSha1]; ok {
		return string(val.Value), nil
	}
	return "", nil
}

// GetTreeGitSHA1 returns the Git tree SHA1 for an Invariant ContentLink address.
func (idx *GitKVIndex) GetTreeGitSHA1(ctx context.Context, invTreeAddr string) (string, error) {
	if idx.kvClient == nil || invTreeAddr == "" {
		return "", nil
	}
	iAddr := strings.ToLower(invTreeAddr)
	keys := []string{"tree:sha256:" + iAddr, "SHA256:" + iAddr}
	res, err := idx.kvClient.BatchGet(ctx, nil, keys)
	if err != nil {
		return "", err
	}
	if val, ok := res["tree:sha256:"+iAddr]; ok {
		return string(val.Value), nil
	}
	if val, ok := res["SHA256:"+iAddr]; ok {
		return string(val.Value), nil
	}
	return "", nil
}

// GetCommitInvariantHash returns the Invariant commit hash for a Git commit SHA1.
func (idx *GitKVIndex) GetCommitInvariantHash(ctx context.Context, gitCommitSHA1 string) (string, error) {
	if idx.kvClient == nil || gitCommitSHA1 == "" {
		return "", nil
	}
	gSha1 := strings.ToLower(gitCommitSHA1)
	keys := []string{"commit:sha1:" + gSha1, "SHA1:" + gSha1}
	res, err := idx.kvClient.BatchGet(ctx, nil, keys)
	if err != nil {
		return "", err
	}
	if val, ok := res["commit:sha1:"+gSha1]; ok {
		return string(val.Value), nil
	}
	if val, ok := res["SHA1:"+gSha1]; ok {
		return string(val.Value), nil
	}
	return "", nil
}

// GetCommitGitSHA1 returns the Git commit SHA1 for an Invariant commit hash.
func (idx *GitKVIndex) GetCommitGitSHA1(ctx context.Context, invCommitHash string) (string, error) {
	if idx.kvClient == nil || invCommitHash == "" {
		return "", nil
	}
	iHash := strings.ToLower(invCommitHash)
	keys := []string{"commit:sha256:" + iHash, "SHA256:" + iHash}
	res, err := idx.kvClient.BatchGet(ctx, nil, keys)
	if err != nil {
		return "", err
	}
	if val, ok := res["commit:sha256:"+iHash]; ok {
		return string(val.Value), nil
	}
	if val, ok := res["SHA256:"+iHash]; ok {
		return string(val.Value), nil
	}
	return "", nil
}

// RecordBlobMapping stores bidirectional mappings between Git blob SHA1 and Invariant SHA256.
func (idx *GitKVIndex) RecordBlobMapping(ctx context.Context, gitBlobSHA1, invSHA256 string) error {
	if idx.kvClient == nil || gitBlobSHA1 == "" || invSHA256 == "" {
		return nil
	}
	gSha1 := strings.ToLower(gitBlobSHA1)
	iSha256 := strings.ToLower(invSHA256)

	entries := map[string][]byte{
		"SHA1:" + gSha1:     []byte(iSha256),
		"SHA256:" + iSha256: []byte(gSha1),
	}
	_, err := idx.kvClient.BatchPut(ctx, nil, entries)
	return err
}

// RecordTreeMapping stores bidirectional mappings between Git tree SHA1 and Invariant tree address.
func (idx *GitKVIndex) RecordTreeMapping(ctx context.Context, gitTreeSHA1, invTreeAddr string) error {
	if idx.kvClient == nil || gitTreeSHA1 == "" || invTreeAddr == "" {
		return nil
	}
	gSha1 := strings.ToLower(gitTreeSHA1)
	iAddr := strings.ToLower(invTreeAddr)

	entries := map[string][]byte{
		"tree:sha1:" + gSha1:    []byte(iAddr),
		"tree:sha256:" + iAddr:  []byte(gSha1),
		"SHA1:" + gSha1:         []byte(iAddr),
		"SHA256:" + iAddr:       []byte(gSha1),
		"tree:scanned:" + gSha1: []byte("true"),
	}
	_, err := idx.kvClient.BatchPut(ctx, nil, entries)
	return err
}

// RecordCommitMapping stores bidirectional mappings between Git commit SHA1 and Invariant commit hash.
func (idx *GitKVIndex) RecordCommitMapping(ctx context.Context, gitCommitSHA1, invCommitHash string) error {
	if idx.kvClient == nil || gitCommitSHA1 == "" || invCommitHash == "" {
		return nil
	}
	gSha1 := strings.ToLower(gitCommitSHA1)
	iHash := strings.ToLower(invCommitHash)

	entries := map[string][]byte{
		"commit:sha1:" + gSha1:   []byte(iHash),
		"commit:sha256:" + iHash: []byte(gSha1),
		"SHA1:" + gSha1:          []byte(iHash),
		"SHA256:" + iHash:        []byte(gSha1),
	}
	_, err := idx.kvClient.BatchPut(ctx, nil, entries)
	return err
}

// BatchRecord records multiple arbitrary key-value mappings into KV store.
func (idx *GitKVIndex) BatchRecord(ctx context.Context, entries map[string]string) error {
	if idx.kvClient == nil || len(entries) == 0 {
		return nil
	}
	byteEntries := make(map[string][]byte, len(entries))
	for k, v := range entries {
		byteEntries[k] = []byte(v)
	}
	_, err := idx.kvClient.BatchPut(ctx, nil, byteEntries)
	return err
}
