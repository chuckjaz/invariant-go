package kv

import (
	"container/list"
	"math"
	"sync"
	"time"
)

type cacheItem struct {
	key          string
	txID         uint64
	record       Record
	inBTree      bool
	lastPromoted time.Time
}

type Cache struct {
	mu             sync.RWMutex
	maxSize        int
	currSize       int
	items          map[string]*list.Element // key -> list element
	lruList        *list.List
	maxTxIDInBTree uint64
}

func NewCache(maxSize int) *Cache {
	return &Cache{
		maxSize: maxSize,
		items:   make(map[string]*list.Element),
		lruList: list.New(),
	}
}

// Add adds a new record to the cache. Returns true if it was a new key.
func (c *Cache) Add(rec Record, inBTree bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	size := len(rec.Value)

	if el, ok := c.items[rec.Key]; ok {
		item := el.Value.(*cacheItem)
		// Only update if transaction ID is newer
		if rec.TransactionID > item.txID {
			c.currSize -= len(item.record.Value)
			item.txID = rec.TransactionID
			item.record = rec
			item.inBTree = inBTree
			item.lastPromoted = time.Time{}
			c.currSize += size
			c.lruList.MoveToFront(el)
		}
	} else {
		item := &cacheItem{
			key:          rec.Key,
			txID:         rec.TransactionID,
			record:       rec,
			inBTree:      inBTree,
			lastPromoted: time.Time{},
		}
		el := c.lruList.PushFront(item)
		c.items[rec.Key] = el
		c.currSize += size
	}

	c.evictIfNeeded()
}

func (c *Cache) Get(key string) (Record, bool) {
	c.mu.RLock()
	el, ok := c.items[key]
	if !ok {
		c.mu.RUnlock()
		return Record{}, false
	}

	item := el.Value.(*cacheItem)
	rec := item.record
	needPromotion := time.Since(item.lastPromoted) > 1*time.Second
	c.mu.RUnlock()

	if needPromotion {
		go func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			// Verify it wasn't removed or replaced before we acquired the write lock
			if currentEl, stillExists := c.items[key]; stillExists && currentEl == el {
				it := el.Value.(*cacheItem)
				if time.Since(it.lastPromoted) > 1*time.Second {
					c.lruList.MoveToFront(el)
					it.lastPromoted = time.Now()
				}
			}
		}()
	}

	return rec, true
}

// MarkInBTree marks all records up to the given maxTxID as inBTree, allowing them to be evicted.
func (c *Cache) MarkInBTree(maxTxID uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if maxTxID > c.maxTxIDInBTree {
		c.maxTxIDInBTree = maxTxID
	}
	c.evictIfNeeded()
}

// Invalidate marks an item in the cache as aborted so it will be ignored and eventually evicted.
func (c *Cache) Invalidate(key string, txID uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		item := el.Value.(*cacheItem)
		if item.txID == txID {
			item.txID = math.MaxUint64
			item.inBTree = true
		}
	}
}

func (c *Cache) isEvictable(item *cacheItem) bool {
	return item.inBTree || item.txID <= c.maxTxIDInBTree
}

func (c *Cache) evictIfNeeded() {
	for c.currSize > c.maxSize && c.lruList.Len() > 0 {
		// Evict from back
		e := c.lruList.Back()
		item := e.Value.(*cacheItem)

		// Only evict if it has been safely stored in the B-Tree
		if !c.isEvictable(item) {
			// Find the oldest item that CAN be evicted
			// If none can be evicted, we just let the cache grow past maxSize temporarily.
			var toEvict *list.Element
			for search := c.lruList.Back(); search != nil; search = search.Prev() {
				if c.isEvictable(search.Value.(*cacheItem)) {
					toEvict = search
					break
				}
			}
			if toEvict == nil {
				break
			}
			e = toEvict
			item = e.Value.(*cacheItem)
		}

		c.lruList.Remove(e)
		delete(c.items, item.key)
		c.currSize -= len(item.record.Value)
	}
}
