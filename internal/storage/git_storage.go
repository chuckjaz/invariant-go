package storage

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"

	"invariant/internal/gitscan"

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

// GitStorageOptions defines configuration options for GitStorage.
type GitStorageOptions struct {
	CacheCapacity   int
	ScanCommit      string
	ScanDepth       int
	ScanConcurrency int
	ScanLogWriter   io.Writer
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
// If opts.CacheCapacity is 0, the default capacity of 10,000 is used.
// If opts.CacheCapacity is negative, caching is disabled.
// If opts.ScanCommit is non-empty, a local repository scan is performed on startup.
func NewGitStorage(gitDir string, kvClient KVGetter, opts GitStorageOptions) (*GitStorage, error) {
	repo, err := git.PlainOpen(gitDir)
	if err != nil {
		var err2 error
		repo, err2 = git.PlainOpenWithOptions(gitDir, &git.PlainOpenOptions{DetectDotGit: true})
		if err2 != nil {
			return nil, fmt.Errorf("failed to open git repository at %s: %w (fallback: %v)", gitDir, err, err2)
		}
	}

	if opts.ScanCommit != "" {
		var scannerClient gitscan.KVScannerClient
		if sc, ok := kvClient.(gitscan.KVScannerClient); ok {
			scannerClient = sc
		} else {
			v := reflect.ValueOf(kvClient)
			if !v.MethodByName("BatchGet").IsValid() || !v.MethodByName("BatchPut").IsValid() {
				return nil, fmt.Errorf("kvClient does not support scanning (must implement BatchGet and BatchPut)")
			}
			scannerClient = kvScannerAdapter{client: kvClient}
		}

		concurrency := opts.ScanConcurrency
		if concurrency <= 0 {
			concurrency = 20
		}
		depth := opts.ScanDepth
		if depth == 0 {
			depth = 1
		}

		err = gitscan.ScanLocal(context.Background(), scannerClient, gitDir, opts.ScanCommit, depth, concurrency, opts.ScanLogWriter)
		if err != nil {
			return nil, fmt.Errorf("failed to scan repository on startup: %w", err)
		}
	}

	var cache *shaCache
	if opts.CacheCapacity > 0 {
		cache = newSHACache(opts.CacheCapacity)
	} else if opts.CacheCapacity == 0 {
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

type kvScannerAdapter struct {
	client KVGetter
}

func (a kvScannerAdapter) BatchGet(ctx context.Context, txID *uint64, keys []string) (interface{}, error) {
	method := reflect.ValueOf(a.client).MethodByName("BatchGet")
	if !method.IsValid() {
		return nil, errors.New("underlying kv client does not support BatchGet")
	}
	args := []reflect.Value{
		reflect.ValueOf(ctx),
		reflect.ValueOf(txID),
		reflect.ValueOf(keys),
	}
	res := method.Call(args)
	if !res[1].IsNil() {
		return nil, res[1].Interface().(error)
	}
	return res[0].Interface(), nil
}

func (a kvScannerAdapter) BatchPut(ctx context.Context, txID *uint64, kvs map[string][]byte) (uint64, error) {
	method := reflect.ValueOf(a.client).MethodByName("BatchPut")
	if !method.IsValid() {
		return 0, errors.New("underlying kv client does not support BatchPut")
	}
	args := []reflect.Value{
		reflect.ValueOf(ctx),
		reflect.ValueOf(txID),
		reflect.ValueOf(kvs),
	}
	res := method.Call(args)
	if !res[1].IsNil() {
		return 0, res[1].Interface().(error)
	}
	return res[0].Uint(), nil
}
