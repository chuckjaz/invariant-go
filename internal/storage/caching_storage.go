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
	overflow    ControlledStorage

	maxSize       int64
	desiredSize   int64
	delegateOnMax bool

	mu          sync.Mutex
	lruList     *list.List
	lruMap      map[string]*list.Element
	currentSize int64

	ctx    context.Context
	cancel context.CancelFunc
	evict  chan struct{}

	destHasMu sync.RWMutex
	destHas   map[string]struct{}
}

// Assert that CachingStorage implements the Storage interface
var _ Storage = (*CachingStorage)(nil)

func NewCachingStorage(local ControlledStorage, destination Storage, maxSize, desiredSize int64, delegateOnMax bool) *CachingStorage {
	ctx, cancel := context.WithCancel(context.Background())

	cs := &CachingStorage{
		local:         local,
		destination:   destination,
		maxSize:       maxSize,
		desiredSize:   desiredSize,
		delegateOnMax: delegateOnMax,
		lruList:       list.New(),
		lruMap:        make(map[string]*list.Element),
		ctx:           ctx,
		cancel:        cancel,
		evict:         make(chan struct{}, 1),
		destHas:       make(map[string]struct{}),
	}

	cs.init()
	go cs.evictionLoop()

	return cs
}

func (s *CachingStorage) SetOverflow(overflow ControlledStorage) {
	s.mu.Lock()
	s.overflow = overflow
	s.mu.Unlock()
	if overflow != nil {
		go s.overflowFlushLoop()
	}
}

func (s *CachingStorage) init() {
	// 1. Load existing blocks from local storage into LRU
	for batch := range s.local.List(context.Background(), 1000) {
		var sizesToAdd []int64
		var addrsToAdd []string

		// Get capacities without holding the lock
		for _, addr := range batch {
			size, ok := s.local.Size(context.Background(), addr)
			if ok {
				addrsToAdd = append(addrsToAdd, addr)
				sizesToAdd = append(sizesToAdd, size)
			}
		}

		s.mu.Lock()
		for i, addr := range addrsToAdd {
			// Add to back of LRU (least recently used)
			// Assuming the iteration order is somewhat arbitrary or we just consider
			// things already on disk as "older" until accessed.
			if _, exists := s.lruMap[addr]; !exists {
				elem := s.lruList.PushBack(addr)
				s.lruMap[addr] = elem
				s.currentSize += sizesToAdd[i]
			}
		}
		s.mu.Unlock()
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

func (s *CachingStorage) Has(ctx context.Context, address string) bool {
	if ok := s.local.Has(ctx, address); ok {
		s.markUsed(address)
		return true
	}
	s.mu.Lock()
	overflow := s.overflow
	s.mu.Unlock()
	if overflow != nil && overflow.Has(ctx, address) {
		return true
	}
	if s.destination != nil {
		if s.destination.Has(ctx, address) {
			s.destHasMu.Lock()
			s.destHas[address] = struct{}{}
			s.destHasMu.Unlock()
			return true
		}
	}
	return false
}

func (s *CachingStorage) Get(ctx context.Context, address string) (io.ReadCloser, bool) {
	rc, ok := s.local.Get(ctx, address)
	if ok {
		s.markUsed(address)
		return rc, true
	}

	s.mu.Lock()
	overflow := s.overflow
	s.mu.Unlock()

	fetchAndPromote := func(src Storage, saveDestHas bool) (io.ReadCloser, bool) {
		rcSrc, okSrc := src.Get(ctx, address)
		if okSrc {
			if saveDestHas {
				s.destHasMu.Lock()
				s.destHas[address] = struct{}{}
				s.destHasMu.Unlock()
			}
			destSize, hasSize := src.Size(ctx, address)
			if hasSize {
				s.mu.Lock()
				hasRoom := destSize <= s.maxSize
				s.mu.Unlock()

				if hasRoom {
					pr, pw := io.Pipe()
					go func() {
						defer pr.Close()
						okL, errL := s.local.StoreAt(context.Background(), address, pr)
						if errL == nil && okL {
							actualSize, hasSizeL := s.local.Size(context.Background(), address)
							if hasSizeL {
								s.addUsed(address, actualSize)
							}
						}
					}()

					return &trackingReadCloser{
						c:  rcSrc,
						pw: pw,
					}, true
				}
			}
			return rcSrc, true
		}
		return nil, false
	}

	if overflow != nil {
		if r, ok := fetchAndPromote(overflow, false); ok {
			return r, true
		}
	}

	if s.destination != nil {
		if r, ok := fetchAndPromote(s.destination, true); ok {
			return r, true
		}
	}
	return nil, false
}

func (s *CachingStorage) Size(ctx context.Context, address string) (int64, bool) {
	size, ok := s.local.Size(ctx, address)
	if ok {
		s.markUsed(address)
		return size, true
	}
	s.mu.Lock()
	overflow := s.overflow
	s.mu.Unlock()
	if overflow != nil {
		size, ok := overflow.Size(ctx, address)
		if ok {
			return size, true
		}
	}
	if s.destination != nil {
		size, ok := s.destination.Size(ctx, address)
		if ok {
			s.destHasMu.Lock()
			s.destHas[address] = struct{}{}
			s.destHasMu.Unlock()
		}
		return size, ok
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

func (s *CachingStorage) Store(ctx context.Context, r io.Reader) (string, error) {
	s.mu.Lock()
	if s.currentSize >= s.maxSize {
		s.mu.Unlock()
		if s.delegateOnMax && s.destination != nil {
			addr, err := s.destination.Store(ctx, r)
			if err == nil {
				s.destHasMu.Lock()
				s.destHas[addr] = struct{}{}
				s.destHasMu.Unlock()
			}
			return addr, err
		}
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
				if s.delegateOnMax {
					return nil
				}
				return ErrMaxSizeExceeded
			}
			return nil
		},
	}

	addr, err := s.local.Store(ctx, tr)
	if err != nil {
		return "", err
	}

	// Double check we actually stored it and got the size via the local storage
	// (or simply rely on tr.size if it was newly read).
	tr.Close()

	actualSize, ok := s.local.Size(ctx, addr)
	if !ok {
		actualSize = finalSize
	}

	s.addUsed(addr, actualSize)

	return addr, nil
}

func (s *CachingStorage) StoreAt(ctx context.Context, address string, r io.Reader) (bool, error) {
	if s.local.Has(ctx, address) {
		s.markUsed(address)
		if closer, ok := r.(io.Closer); ok {
			closer.Close()
		}
		return true, nil
	}

	s.mu.Lock()
	if s.currentSize >= s.maxSize {
		s.mu.Unlock()
		if s.delegateOnMax && s.destination != nil {
			ok, err := s.destination.StoreAt(ctx, address, r)
			if err == nil && ok {
				s.destHasMu.Lock()
				s.destHas[address] = struct{}{}
				s.destHasMu.Unlock()
			}
			return ok, err
		}
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
				if s.delegateOnMax {
					return nil
				}
				return ErrMaxSizeExceeded
			}
			return nil
		},
	}

	ok, err := s.local.StoreAt(ctx, address, tr)
	if err != nil {
		return false, err
	}

	tr.Close()

	if ok {
		actualSize, hasSize := s.local.Size(ctx, address)
		if !hasSize {
			actualSize = finalSize
		}
		s.addUsed(address, actualSize)
	}

	return ok, nil
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
		s.mu.Lock()
		if s.currentSize <= s.desiredSize {
			s.mu.Unlock()
			break
		}

		elem := s.lruList.Back()
		if elem == nil {
			s.mu.Unlock()
			break
		}
		addr := elem.Value.(string)
		s.mu.Unlock()

		// Found a candidate for eviction.
		// Upload to destination if it doesn't have it.
		evictedToDest := false
		if s.destination != nil {
			s.destHasMu.RLock()
			_, hasDest := s.destHas[addr]
			s.destHasMu.RUnlock()

			if !hasDest && !s.destination.Has(context.Background(), addr) {
				rc, ok := s.local.Get(context.Background(), addr)
				if ok {
					_, err := s.destination.StoreAt(context.Background(), addr, rc)
					rc.Close()
					if err != nil {
						log.Printf("caching storage: failed to evict block %s to destination: %v", addr, err)
					} else {
						evictedToDest = true
						s.destHasMu.Lock()
						s.destHas[addr] = struct{}{}
						s.destHasMu.Unlock()
					}
				}
			} else {
				evictedToDest = true
			}
		}

		s.mu.Lock()
		overflow := s.overflow
		s.mu.Unlock()

		if !evictedToDest && overflow != nil {
			if !overflow.Has(context.Background(), addr) {
				rc, ok := s.local.Get(context.Background(), addr)
				if ok {
					_, err := overflow.StoreAt(context.Background(), addr, rc)
					rc.Close()
					if err != nil {
						log.Printf("caching storage: failed to evict block %s to overflow: %v", addr, err)
						time.Sleep(1 * time.Second)
						break
					}
				}
			}
		} else if !evictedToDest && s.destination != nil {
			// Failed to evict to destination, and no overflow configured. Sleep.
			time.Sleep(1 * time.Second)
			break
		}

		s.mu.Lock()
		// Recheck in case size dropped due to other operations
		if s.currentSize <= s.desiredSize {
			s.mu.Unlock()
			break
		}

		// Recheck that it's still at the back (hasn't been touched)
		elem = s.lruMap[addr]
		if elem == nil || elem != s.lruList.Back() {
			s.mu.Unlock()
			continue
		}

		size, hasSize := s.local.Size(context.Background(), addr)
		if !hasSize {
			// If it's already gone, just clean it up from LRU with 0 size
			size = 0
		}

		s.lruList.Remove(elem)
		delete(s.lruMap, addr)
		s.currentSize -= size
		s.mu.Unlock()

		// Remove from local storage
		ok, err := s.local.Remove(context.Background(), addr)
		if err != nil || !ok {
			log.Printf("caching storage: failed to explicitly remove block %s from local storage: %v", addr, err)
			break
		}
	}
}

// Sync ensures that all blocks in the local storage have been propagated to the destination.
func (s *CachingStorage) Sync(ctx context.Context) error {
	if s.destination == nil {
		return nil
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	// Since we might be flushing many blocks, a bounded semaphore is appropriate
	sem := make(chan struct{}, 32)
	ctxDone := ctx.Done()

	for batch := range s.local.List(ctx, 100) {
		for _, addr := range batch {
			select {
			case <-ctxDone:
				return ctx.Err()
			case err := <-errCh:
				return err
			default:
			}

			s.destHasMu.RLock()
			_, hasDest := s.destHas[addr]
			s.destHasMu.RUnlock()

			if hasDest {
				continue
			}

			wg.Add(1)
			sem <- struct{}{}
			go func(a string) {
				defer wg.Done()
				defer func() { <-sem }()

				// Recheck context inside goroutine
				select {
				case <-ctxDone:
					return
				default:
				}

				if s.destination.Has(ctx, a) {
					s.destHasMu.Lock()
					s.destHas[a] = struct{}{}
					s.destHasMu.Unlock()
					return
				}

				rc, ok := s.local.Get(ctx, a)
				if !ok {
					return
				}

				storeOk, err := s.destination.StoreAt(ctx, a, rc)
				rc.Close()

				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				if storeOk {
					s.destHasMu.Lock()
					s.destHas[a] = struct{}{}
					s.destHasMu.Unlock()
				}
			}(addr)
		}
	}

	wg.Wait()

	select {
	case err := <-errCh:
		return err
	default:
	}

	if syncDest, ok := s.destination.(SyncStorage); ok {
		if err := syncDest.Sync(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (s *CachingStorage) overflowFlushLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if s.destination == nil {
				continue
			}

			s.mu.Lock()
			overflow := s.overflow
			s.mu.Unlock()

			if overflow == nil {
				return
			}

			for batch := range overflow.List(s.ctx, 100) {
				for _, addr := range batch {
					s.destHasMu.RLock()
					_, hasDest := s.destHas[addr]
					s.destHasMu.RUnlock()

					if !hasDest && !s.destination.Has(s.ctx, addr) {
						rc, ok := overflow.Get(s.ctx, addr)
						if !ok {
							continue
						}
						_, err := s.destination.StoreAt(s.ctx, addr, rc)
						rc.Close()
						if err != nil {
							// If one upload fails, the destination is probably unreachable. Break the batch.
							break
						}
						s.destHasMu.Lock()
						s.destHas[addr] = struct{}{}
						s.destHasMu.Unlock()
					}
					// Successfully stored in dest (or it already had it). Remove from overflow.
					overflow.Remove(s.ctx, addr)
				}
			}
		}
	}
}
