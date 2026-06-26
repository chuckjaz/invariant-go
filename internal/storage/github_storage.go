package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// KVGetter specifies the subset of the KV client needed by GitHubStorage.
type KVGetter interface {
	Get(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error)
}

// GitHubStorage implements the Storage interface by reading blobs from GitHub.
type GitHubStorage struct {
	owner      string
	repo       string
	token      string
	kvClient   KVGetter
	httpClient *http.Client
	apiURL     string
}

// Assert that GitHubStorage implements the Storage interface
var _ Storage = (*GitHubStorage)(nil)

// NewGitHubStorage creates a new GitHubStorage instance.
func NewGitHubStorage(owner, repo, token string, kvClient KVGetter, httpClient *http.Client) *GitHubStorage {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &GitHubStorage{
		owner:      owner,
		repo:       repo,
		token:      token,
		kvClient:   kvClient,
		httpClient: httpClient,
		apiURL:     "https://api.github.com",
	}
}

// ID returns a unique identifier for this storage instance.
func (s *GitHubStorage) ID() string {
	return fmt.Sprintf("github-%s-%s", s.owner, s.repo)
}

// Has checks if a blob exists on GitHub.
func (s *GitHubStorage) Has(ctx context.Context, address string) bool {
	sha1Hex, ok := s.getBlobSHA1(ctx, address)
	if !ok {
		return false
	}

	url := fmt.Sprintf("%s/repos/%s/%s/git/blobs/%s", s.apiURL, s.owner, s.repo, sha1Hex)
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return false
	}

	req.Header.Set("Accept", "application/vnd.github.v3.raw")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// Get retrieves the raw content of a blob from GitHub.
func (s *GitHubStorage) Get(ctx context.Context, address string) (io.ReadCloser, bool) {
	sha1Hex, ok := s.getBlobSHA1(ctx, address)
	if !ok {
		return nil, false
	}

	url := fmt.Sprintf("%s/repos/%s/%s/git/blobs/%s", s.apiURL, s.owner, s.repo, sha1Hex)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, false
	}

	req.Header.Set("Accept", "application/vnd.github.v3.raw")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, false
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, false
	}

	return resp.Body, true
}

// Store is not supported as GitHub storage is read-only.
func (s *GitHubStorage) Store(ctx context.Context, r io.Reader) (string, error) {
	return "", errors.New("GitHub storage is read-only")
}

// StoreAt is not supported as GitHub storage is read-only.
func (s *GitHubStorage) StoreAt(ctx context.Context, address string, r io.Reader) (bool, error) {
	return false, errors.New("GitHub storage is read-only")
}

// Size returns the size of the blob in bytes.
func (s *GitHubStorage) Size(ctx context.Context, address string) (int64, bool) {
	sha1Hex, ok := s.getBlobSHA1(ctx, address)
	if !ok {
		return 0, false
	}

	url := fmt.Sprintf("%s/repos/%s/%s/git/blobs/%s", s.apiURL, s.owner, s.repo, sha1Hex)
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return 0, false
	}

	req.Header.Set("Accept", "application/vnd.github.v3.raw")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, false
	}

	if sizeStr := resp.Header.Get("Content-Length"); sizeStr != "" {
		if size, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
			return size, true
		}
	}
	return 0, false
}

// getBlobSHA1 looks up the Git SHA1 mapping from the KV client.
func (s *GitHubStorage) getBlobSHA1(ctx context.Context, address string) (string, bool) {
	key := "SHA256:" + address
	val, _, err := s.kvClient.Get(ctx, nil, key)
	if err != nil {
		return "", false
	}

	return string(val), true
}
