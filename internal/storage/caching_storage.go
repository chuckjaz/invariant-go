package storage

import (
	"container/list"
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"time"
)

var (
	ErrMaxSizeExceeded = errors.New("caching storage: max size exceeded")
)

type CachingStorage struct {
	local       ControlledStorage
	destination Storage

	maxSize     int64
	desiredSize int64

	mu          sync.Mutex
	lruList     *list.List
	lruMap      map[string]*list.Element
	currentSize int64

	ctx    context.Context
	cancel context.CancelFunc
	evict  chan struct{}
}

// Assert that CachingStorage implements the Storage interface
var _ Storage = (*CachingStorage)(nil)

func NewCachingStorage(local ControlledStorage, destination Storage, maxSize, desiredSize int64) *CachingStorage {
	ctx, cancel := context.WithCancel(context.Background())

	cs := &CachingStorage{
		local:       local,
		destination: destination,
		maxSize:     maxSize,
		desiredSize: desiredSize,
		lruList:     list.New(),
		lruMap:      make(map[string]*list.Element),
		ctx:         ctx,
		cancel:      cancel,
		evict:       make(chan struct{}, 1),
	}

	cs.init()
	go cs.evictionLoop()

	return cs
}

func (s *CachingStorage) init() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Load existing blocks from local storage into LRU
	for batch := range s.local.List(1000) {
		for _, addr := range batch {
			size, ok := s.local.Size(addr)
			if !ok {
				continue
			}

			// Add to back of LRU (least recently used)
			// Assuming the iteration order is somewhat arbitrary or we just consider
			// things already on disk as "older" until accessed.
			if _, exists := s.lruMap[addr]; !exists {
				elem := s.lruList.PushBack(addr)
				s.lruMap[addr] = elem
				s.currentSize += size
			}
		}
	}

	s.checkEviction()
}

func (s *CachingStorage) Close() {
	s.cancel()
}

func (s *CachingStorage) checkEviction() {
	if s.currentSize > s.desiredSize {
		select {
		case s.evict <- struct{}{}:
		default:
		}
	}
}

func (s *CachingStorage) markUsed(address string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if elem, ok := s.lruMap[address]; ok {
		s.lruList.MoveToFront(elem)
	}
}

func (s *CachingStorage) addUsed(address string, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if elem, ok := s.lruMap[address]; ok {
		s.lruList.MoveToFront(elem)
		return
	}

	elem := s.lruList.PushFront(address)
	s.lruMap[address] = elem
	s.currentSize += size

	s.checkEviction()
}

type trackingReadCloser struct {
	c  io.ReadCloser
	pw *io.PipeWriter
}

func (t *trackingReadCloser) Read(p []byte) (n int, err error) {
	n, err = t.c.Read(p)
	if n > 0 {
		_, _ = t.pw.Write(p[:n])
	}
	if err == io.EOF {
		t.pw.Close()
	} else if err != nil {
		t.pw.CloseWithError(err)
	}
	return n, err
}

func (t *trackingReadCloser) Close() error {
	t.pw.CloseWithError(io.ErrClosedPipe)
	return t.c.Close()
}

func (s *CachingStorage) Has(address string) bool {
	if ok := s.local.Has(address); ok {
		s.markUsed(address)
		return true
	}
	if s.destination != nil {
		return s.destination.Has(address)
	}
	return false
}

func (s *CachingStorage) Get(address string) (io.ReadCloser, bool) {
	rc, ok := s.local.Get(address)
	if ok {
		s.markUsed(address)
		return rc, true
	}

	if s.destination != nil {
		rc, ok = s.destination.Get(address)
		if ok {
			destSize, hasSize := s.destination.Size(address)
			if hasSize {
				s.mu.Lock()
				hasRoom := s.currentSize+destSize <= s.maxSize
				s.mu.Unlock()

				if hasRoom {
					pr, pw := io.Pipe()
					go func() {
						defer pr.Close()
						okL, errL := s.local.StoreAt(address, pr)
						if errL == nil && okL {
							actualSize, hasSizeL := s.local.Size(address)
							if hasSizeL {
								s.addUsed(address, actualSize)
							}
						}
					}()

					return &trackingReadCloser{
						c:  rc,
						pw: pw,
					}, true
				}
			}
			return rc, true
		}
	}
	return nil, false
}

func (s *CachingStorage) Size(address string) (int64, bool) {
	size, ok := s.local.Size(address)
	if ok {
		s.markUsed(address)
		return size, true
	}
	if s.destination != nil {
		return s.destination.Size(address)
	}
	return 0, false
}

type trackingReader struct {
	r         io.Reader
	size      int64
	onDone    func(int64)
	checkSize func(int64) error
	done      bool
}

func (tr *trackingReader) Read(p []byte) (n int, err error) {
	n, err = tr.r.Read(p)
	tr.size += int64(n)

	if tr.checkSize != nil {
		if errCheck := tr.checkSize(tr.size); errCheck != nil {
			return n, errCheck
		}
	}

	if err == io.EOF && !tr.done {
		tr.done = true
		if tr.onDone != nil {
			tr.onDone(tr.size)
		}
	}
	return n, err
}

func (tr *trackingReader) Close() error {
	if !tr.done {
		tr.done = true
		if tr.onDone != nil {
			tr.onDone(tr.size)
		}
	}
	if rc, ok := tr.r.(io.ReadCloser); ok {
		return rc.Close()
	}
	return nil
}

func (s *CachingStorage) Store(r io.Reader) (string, error) {
	s.mu.Lock()
	if s.currentSize >= s.maxSize {
		s.mu.Unlock()
		return "", ErrMaxSizeExceeded
	}
	s.mu.Unlock()

	var finalSize int64
	tr := &trackingReader{
		r: r,
		onDone: func(size int64) {
			finalSize = size
		},
		checkSize: func(added int64) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.currentSize+added > s.maxSize {
				return ErrMaxSizeExceeded
			}
			return nil
		},
	}

	addr, err := s.local.Store(tr)
	if err != nil {
		return "", err
	}

	// Double check we actually stored it and got the size via the local storage
	// (or simply rely on tr.size if it was newly read).
	tr.Close()

	actualSize, ok := s.local.Size(addr)
	if !ok {
		actualSize = finalSize
	}

	s.addUsed(addr, actualSize)

	return addr, nil
}

func (s *CachingStorage) StoreAt(address string, r io.Reader) (bool, error) {
	s.mu.Lock()
	if s.currentSize >= s.maxSize {
		s.mu.Unlock()
		return false, ErrMaxSizeExceeded
	}
	s.mu.Unlock()

	var finalSize int64
	tr := &trackingReader{
		r: r,
		onDone: func(size int64) {
			finalSize = size
		},
		checkSize: func(added int64) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.currentSize+added > s.maxSize {
				return ErrMaxSizeExceeded
			}
			return nil
		},
	}

	ok, err := s.local.StoreAt(address, tr)
	if err != nil {
		return false, err
	}

	tr.Close()

	if ok {
		actualSize, hasSize := s.local.Size(address)
		if !hasSize {
			actualSize = finalSize
		}
		s.addUsed(address, actualSize)
	}

	return ok, nil
}

func (s *CachingStorage) getLRUBack() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentSize <= s.desiredSize {
		return "", false
	}

	elem := s.lruList.Back()
	if elem == nil {
		return "", false
	}

	return elem.Value.(string), true
}

func (s *CachingStorage) removeLRUBack(address string, sizeRemoved int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if elem, ok := s.lruMap[address]; ok && elem == s.lruList.Back() {
		s.lruList.Remove(elem)
		delete(s.lruMap, address)
		s.currentSize -= sizeRemoved
	}
}

func (s *CachingStorage) evictionLoop() {
	ticker := time.NewTicker(time.Second) // Fallback check
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.evict:
			s.doEvict()
		case <-ticker.C:
			s.doEvict()
		}
	}
}

func (s *CachingStorage) doEvict() {
	for {
		addr, ok := s.getLRUBack()
		if !ok {
			break
		}

		// Found a candidate for eviction.
		// Upload to destination if it doesn't have it.
		if s.destination != nil {
			if !s.destination.Has(addr) {
				rc, ok := s.local.Get(addr)
				if ok {
					_, err := s.destination.StoreAt(addr, rc)
					rc.Close()
					if err != nil {
						log.Printf("caching storage: failed to evict block %s to destination: %v", addr, err)
						// Sleep to avoid thrashing CPU on failure
						time.Sleep(1 * time.Second)
						break
					}
				}
			}
		}

		size, hasSize := s.local.Size(addr)
		if !hasSize {
			// If it's already gone, just clean it up from LRU with 0 size
			size = 0
		}

		// Remove from local storage
		ok, err := s.local.Remove(addr)
		if err != nil || !ok {
			log.Printf("caching storage: failed to explicitly remove block %s from local storage: %v", addr, err)
			break
		}

		// Remove from LRU and update size
		s.removeLRUBack(addr, size)
	}
}
