package storage

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type cacheEntry struct {
	key   string
	value string
}

type shaCache struct {
	mu       sync.Mutex
	capacity int
	lruList  *list.List
	cacheMap map[string]*list.Element
}

func newSHACache(capacity int) *shaCache {
	return &shaCache{
		capacity: capacity,
		lruList:  list.New(),
		cacheMap: make(map[string]*list.Element),
	}
}

func (c *shaCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.cacheMap[key]; ok {
		c.lruList.MoveToFront(elem)
		return elem.Value.(*cacheEntry).value, true
	}
	return "", false
}

func (c *shaCache) put(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.cacheMap[key]; ok {
		c.lruList.MoveToFront(elem)
		elem.Value.(*cacheEntry).value = value
		return
	}

	if c.lruList.Len() >= c.capacity {
		back := c.lruList.Back()
		if back != nil {
			c.lruList.Remove(back)
			oldEntry := back.Value.(*cacheEntry)
			delete(c.cacheMap, oldEntry.key)
		}
	}

	entry := &cacheEntry{key: key, value: value}
	elem := c.lruList.PushFront(entry)
	c.cacheMap[key] = elem
}

// GitStorage implements the Storage interface by reading blobs from a local Git repository.
type GitStorage struct {
	gitDir   string
	repo     *git.Repository
	kvClient KVGetter
	cache    *shaCache
}

// Assert that GitStorage implements the Storage interface
var _ Storage = (*GitStorage)(nil)

// NewGitStorage creates a new GitStorage instance.
// If cacheCapacity is 0, the default capacity of 10,000 is used.
// If cacheCapacity is negative, caching is disabled.
func NewGitStorage(gitDir string, kvClient KVGetter, cacheCapacity int) (*GitStorage, error) {
	repo, err := git.PlainOpen(gitDir)
	if err != nil {
		var err2 error
		repo, err2 = git.PlainOpenWithOptions(gitDir, &git.PlainOpenOptions{DetectDotGit: true})
		if err2 != nil {
			return nil, fmt.Errorf("failed to open git repository at %s: %w (fallback: %v)", gitDir, err, err2)
		}
	}

	var cache *shaCache
	if cacheCapacity > 0 {
		cache = newSHACache(cacheCapacity)
	} else if cacheCapacity == 0 {
		cache = newSHACache(10000)
	}

	return &GitStorage{
		gitDir:   gitDir,
		repo:     repo,
		kvClient: kvClient,
		cache:    cache,
	}, nil
}

// ID returns a unique identifier for this storage instance.
func (s *GitStorage) ID() string {
	return fmt.Sprintf("git-%s", s.gitDir)
}

// Has checks if a blob exists in the git repository.
func (s *GitStorage) Has(ctx context.Context, address string) bool {
	sha1Hex, ok := s.getBlobSHA1(ctx, address)
	if !ok {
		return false
	}

	hash := plumbing.NewHash(sha1Hex)
	_, err := s.repo.BlobObject(hash)
	return err == nil
}

// Get retrieves the raw content of a blob from the local Git repository.
func (s *GitStorage) Get(ctx context.Context, address string) (io.ReadCloser, bool) {
	sha1Hex, ok := s.getBlobSHA1(ctx, address)
	if !ok {
		return nil, false
	}

	hash := plumbing.NewHash(sha1Hex)
	blob, err := s.repo.BlobObject(hash)
	if err != nil {
		return nil, false
	}

	reader, err := blob.Reader()
	if err != nil {
		return nil, false
	}

	return reader, true
}

// Store is not supported as Git storage is read-only.
func (s *GitStorage) Store(ctx context.Context, r io.Reader) (string, error) {
	return "", errors.New("Git storage is read-only")
}

// StoreAt is not supported as Git storage is read-only.
func (s *GitStorage) StoreAt(ctx context.Context, address string, r io.Reader) (bool, error) {
	return false, errors.New("Git storage is read-only")
}

// Size returns the size of the blob in bytes.
func (s *GitStorage) Size(ctx context.Context, address string) (int64, bool) {
	sha1Hex, ok := s.getBlobSHA1(ctx, address)
	if !ok {
		return 0, false
	}

	hash := plumbing.NewHash(sha1Hex)
	blob, err := s.repo.BlobObject(hash)
	if err != nil {
		return 0, false
	}

	return blob.Size, true
}

// getBlobSHA1 looks up the Git SHA1 mapping from the KV client.
func (s *GitStorage) getBlobSHA1(ctx context.Context, address string) (string, bool) {
	if s.cache != nil {
		if val, ok := s.cache.get(address); ok {
			return val, true
		}
	}

	key := "SHA256:" + address
	val, _, err := s.kvClient.Get(ctx, nil, key)
	if err != nil {
		return "", false
	}

	sha1Hex := string(val)
	if s.cache != nil {
		s.cache.put(address, sha1Hex)
	}
	return sha1Hex, true
}
