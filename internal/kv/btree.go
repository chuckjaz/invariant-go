package kv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// ValueThreshold is the size limit for storing values inline in the B-Tree node.
const ValueThreshold = 1024 // 1K

type BTreeKey struct {
	Key           string `json:"k"`
	TransactionID uint64 `json:"tx"`
}

// CompareBTreeKey compares two keys.
// Primary sort is Key ascending.
// Secondary sort is TransactionID descending, so the latest version of a key comes first.
func CompareBTreeKey(a, b BTreeKey) int {
	if a.Key < b.Key {
		return -1
	}
	if a.Key > b.Key {
		return 1
	}
	if a.TransactionID > b.TransactionID {
		return -1
	}
	if a.TransactionID < b.TransactionID {
		return 1
	}
	return 0
}

type ValueEntry struct {
	Inline []byte               `json:"in,omitempty"`
	Link   *content.ContentLink `json:"link,omitempty"`
}

type BTreeNode struct {
	IsLeaf      bool                  `json:"leaf"`
	Keys        []BTreeKey            `json:"keys"`
	Children    []content.ContentLink `json:"children,omitempty"`
	Values      []ValueEntry          `json:"values,omitempty"`
	LastJournal *content.ContentLink  `json:"lastJournal,omitempty"`
	MaxTxID     uint64                `json:"maxTxID,omitempty"`
}

func (n *BTreeNode) Serialize() ([]byte, error) {
	return json.Marshal(n)
}

func DeserializeBTreeNode(data []byte) (*BTreeNode, error) {
	var n BTreeNode
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

type BTree struct {
	store      storage.Storage
	slotClient slots.Slots
	maxKeys    int
	opts       content.WriterOptions
	nodeCache  sync.Map
}

func NewBTree(store storage.Storage, slotClient slots.Slots, maxKeys int, opts content.WriterOptions) *BTree {
	if maxKeys < 3 {
		maxKeys = 3
	}
	return &BTree{
		store:      store,
		slotClient: slotClient,
		maxKeys:    maxKeys,
		opts:       opts,
	}
}

func (b *BTree) loadNode(ctx context.Context, link content.ContentLink) (*BTreeNode, error) {
	if cached, ok := b.nodeCache.Load(link.Address); ok {
		return cached.(*BTreeNode), nil
	}

	rc, err := content.Read(link, b.store, b.slotClient)
	if err != nil {
		return nil, fmt.Errorf("node not found: %s", link.Address)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	node, err := DeserializeBTreeNode(data)
	if err != nil {
		return nil, err
	}

	b.nodeCache.Store(link.Address, node)
	return node, nil
}

func (b *BTree) saveNode(ctx context.Context, node *BTreeNode) (content.ContentLink, error) {
	data, err := node.Serialize()
	if err != nil {
		return content.ContentLink{}, err
	}
	link, err := content.Write(bytes.NewReader(data), b.store, b.opts)
	if err != nil {
		return content.ContentLink{}, err
	}

	b.nodeCache.Store(link.Address, node)
	return link, nil
}

// Search returns the value entry and transaction ID for the latest version of the given key.
func (b *BTree) Search(ctx context.Context, rootAddr *content.ContentLink, key string) (ValueEntry, uint64, bool, error) {
	if rootAddr == nil {
		return ValueEntry{}, 0, false, nil
	}
	return b.searchRecursive(ctx, *rootAddr, key)
}

func (b *BTree) searchRecursive(ctx context.Context, addr content.ContentLink, key string) (ValueEntry, uint64, bool, error) {
	node, err := b.loadNode(ctx, addr)
	if err != nil {
		return ValueEntry{}, 0, false, err
	}

	if node.IsLeaf {
		i := sort.Search(len(node.Keys), func(idx int) bool {
			return node.Keys[idx].Key >= key
		})
		if i < len(node.Keys) && node.Keys[i].Key == key {
			return node.Values[i], node.Keys[i].TransactionID, true, nil
		}
		return ValueEntry{}, 0, false, nil
	}

	// Internal node
	// Find the first child that could contain `key`.
	i := sort.Search(len(node.Keys), func(idx int) bool {
		return node.Keys[idx].Key >= key
	})

	// Child i can contain `key` because its upper bound is node.Keys[i] >= key.
	// Or if i == len(node.Keys), its upper bound is +infinity.
	val, txID, found, err := b.searchRecursive(ctx, node.Children[i], key)
	if err != nil {
		return ValueEntry{}, 0, false, err
	}
	if found {
		return val, txID, true, nil
	}

	// If not found in child i, could it be in subsequent children?
	// Yes, if node.Keys[i].Key == key.
	for i < len(node.Keys) && node.Keys[i].Key == key {
		i++
		val, txID, found, err := b.searchRecursive(ctx, node.Children[i], key)
		if err != nil {
			return ValueEntry{}, 0, false, err
		}
		if found {
			return val, txID, true, nil
		}
	}

	return ValueEntry{}, 0, false, nil
}

// SearchHistory returns a slice of historical values for a key within the transaction ID range.
func (b *BTree) SearchHistory(ctx context.Context, rootAddr *content.ContentLink, key string, minTxID, maxTxID uint64, pageSize int) ([]ValueWithTransaction, bool, error) {
	if rootAddr == nil {
		return nil, false, nil
	}
	var results []ValueWithTransaction
	hasMore, err := b.searchHistoryRecursive(ctx, *rootAddr, key, minTxID, maxTxID, pageSize, &results)
	return results, hasMore, err
}

func (b *BTree) searchHistoryRecursive(ctx context.Context, addr content.ContentLink, key string, minTxID, maxTxID uint64, pageSize int, results *[]ValueWithTransaction) (bool, error) {
	node, err := b.loadNode(ctx, addr)
	if err != nil {
		return false, err
	}

	if node.IsLeaf {
		start := sort.Search(len(node.Keys), func(idx int) bool {
			return node.Keys[idx].Key >= key
		})
		for i := start; i < len(node.Keys); i++ {
			if node.Keys[i].Key == key {
				txID := node.Keys[i].TransactionID
				if txID <= maxTxID && txID >= minTxID {
					if len(*results) >= pageSize {
						return true, nil // Hit page limit, optimistically assume more
					}

					var valBytes []byte
					if node.Values[i].Link != nil {
						rc, err := content.Read(*node.Values[i].Link, b.store, b.slotClient)
						if err != nil {
							return false, fmt.Errorf("value block not found: %v", err)
						}
						valBytes, err = io.ReadAll(rc)
						rc.Close()
						if err != nil {
							return false, err
						}
					} else {
						valBytes = node.Values[i].Inline
					}
					*results = append(*results, ValueWithTransaction{Value: valBytes, TransactionID: txID})
				} else if txID < minTxID {
					// Keys are sorted descending by txID. If we drop below minTxID, no more versions of this key will match.
					return false, nil
				}
			} else {
				// Keys are sorted ascending by Key. If we hit a key > key, we are done.
				return false, nil
			}
		}
		return false, nil
	}

	// Internal node
	i := sort.Search(len(node.Keys), func(idx int) bool {
		return node.Keys[idx].Key >= key
	})
	// For historical search, we also need to consider transaction ordering.
	// We want the first instance where Key == key and TransactionID <= maxTxID.
	// CompareBTreeKey: Key ASC, TransactionID DESC.
	for i < len(node.Keys) && node.Keys[i].Key == key && node.Keys[i].TransactionID > maxTxID {
		i++
	}

	hasMore, err := b.searchHistoryRecursive(ctx, node.Children[i], key, minTxID, maxTxID, pageSize, results)
	if err != nil {
		return false, err
	}
	if hasMore || len(*results) >= pageSize {
		return true, nil
	}

	// Explore subsequent children
	for i < len(node.Keys) && node.Keys[i].Key == key {
		i++
		hasMore, err := b.searchHistoryRecursive(ctx, node.Children[i], key, minTxID, maxTxID, pageSize, results)
		if err != nil {
			return false, err
		}
		if hasMore || len(*results) >= pageSize {
			return true, nil
		}
	}

	return false, nil
}

type MemNode struct {
	IsLeaf      bool
	Keys        []BTreeKey
	MemChildren []*MemNode
	Links       []content.ContentLink
	Values      []ValueEntry
	LastJournal *content.ContentLink
	MaxTxID     uint64
}

func (b *BTree) loadMemNode(ctx context.Context, link content.ContentLink) (*MemNode, error) {
	node, err := b.loadNode(ctx, link)
	if err != nil {
		return nil, err
	}
	mn := &MemNode{
		IsLeaf:      node.IsLeaf,
		Keys:        append([]BTreeKey(nil), node.Keys...),
		Values:      append([]ValueEntry(nil), node.Values...),
		LastJournal: node.LastJournal,
		MaxTxID:     node.MaxTxID,
	}
	if !node.IsLeaf {
		mn.Links = append([]content.ContentLink(nil), node.Children...)
		mn.MemChildren = make([]*MemNode, len(node.Children))
	}
	return mn, nil
}

func (b *BTree) saveMemNode(ctx context.Context, mn *MemNode) (content.ContentLink, error) {
	if !mn.IsLeaf {
		for i, child := range mn.MemChildren {
			if child != nil {
				link, err := b.saveMemNode(ctx, child)
				if err != nil {
					return content.ContentLink{}, err
				}
				mn.Links[i] = link
			}
		}
	}

	node := &BTreeNode{
		IsLeaf:      mn.IsLeaf,
		Keys:        mn.Keys,
		Values:      mn.Values,
		Children:    mn.Links,
		LastJournal: mn.LastJournal,
		MaxTxID:     mn.MaxTxID,
	}
	return b.saveNode(ctx, node)
}

// InsertBatch inserts multiple records functionally by caching nodes in memory,
// and returning the new root address after saving all modified nodes.
func (b *BTree) InsertBatch(ctx context.Context, rootAddr *content.ContentLink, records []Record, lastJournal *content.ContentLink, maxTxID uint64) (content.ContentLink, error) {
	var root *MemNode
	if rootAddr == nil {
		root = &MemNode{IsLeaf: true, LastJournal: lastJournal, MaxTxID: maxTxID}
	} else {
		var err error
		root, err = b.loadMemNode(ctx, *rootAddr)
		if err != nil {
			return content.ContentLink{}, err
		}
		root.LastJournal = lastJournal
		if maxTxID > root.MaxTxID {
			root.MaxTxID = maxTxID
		}
	}

	for _, rec := range records {
		valEntry := ValueEntry{}
		if len(rec.Value) > ValueThreshold {
			link, err := content.Write(bytes.NewReader(rec.Value), b.store, b.opts)
			if err != nil {
				return content.ContentLink{}, err
			}
			valEntry.Link = &link
		} else {
			valEntry.Inline = rec.Value
		}

		k := BTreeKey{Key: rec.Key, TransactionID: rec.TransactionID}
		splitKey, splitRight, err := b.insertMemRecursive(ctx, root, k, valEntry)
		if err != nil {
			return content.ContentLink{}, err
		}

		if splitRight != nil {
			newRoot := &MemNode{
				IsLeaf:      false,
				Keys:        []BTreeKey{splitKey},
				Links:       make([]content.ContentLink, 2),
				MemChildren: []*MemNode{root, splitRight},
				LastJournal: root.LastJournal,
				MaxTxID:     root.MaxTxID,
			}
			root = newRoot
		}
	}

	return b.saveMemNode(ctx, root)
}

func (b *BTree) insertMemRecursive(ctx context.Context, node *MemNode, key BTreeKey, val ValueEntry) (BTreeKey, *MemNode, error) {
	i := sort.Search(len(node.Keys), func(idx int) bool {
		return CompareBTreeKey(key, node.Keys[idx]) <= 0
	})

	if node.IsLeaf {
		if i < len(node.Keys) && CompareBTreeKey(key, node.Keys[i]) == 0 {
			node.Values[i] = val
		} else {
			node.Keys = append(node.Keys[:i], append([]BTreeKey{key}, node.Keys[i:]...)...)
			node.Values = append(node.Values[:i], append([]ValueEntry{val}, node.Values[i:]...)...)
		}
	} else {
		if node.MemChildren[i] == nil {
			child, err := b.loadMemNode(ctx, node.Links[i])
			if err != nil {
				return BTreeKey{}, nil, err
			}
			node.MemChildren[i] = child
		}

		child := node.MemChildren[i]
		splitKey, splitRightMem, err := b.insertMemRecursive(ctx, child, key, val)
		if err != nil {
			return BTreeKey{}, nil, err
		}

		if splitRightMem != nil {
			node.Keys = append(node.Keys[:i], append([]BTreeKey{splitKey}, node.Keys[i:]...)...)
			node.Links = append(node.Links[:i+1], append([]content.ContentLink{{}}, node.Links[i+1:]...)...)
			node.MemChildren = append(node.MemChildren[:i+1], append([]*MemNode{splitRightMem}, node.MemChildren[i+1:]...)...)
		}
	}

	if len(node.Keys) > b.maxKeys {
		mid := len(node.Keys) / 2
		splitKey := node.Keys[mid]

		rightNode := &MemNode{IsLeaf: node.IsLeaf}

		if node.IsLeaf {
			rightNode.Keys = append([]BTreeKey(nil), node.Keys[mid:]...)
			rightNode.Values = append([]ValueEntry(nil), node.Values[mid:]...)

			node.Keys = node.Keys[:mid]
			node.Values = node.Values[:mid]
		} else {
			rightNode.Keys = append([]BTreeKey(nil), node.Keys[mid+1:]...)
			rightNode.Links = append([]content.ContentLink(nil), node.Links[mid+1:]...)
			rightNode.MemChildren = append([]*MemNode(nil), node.MemChildren[mid+1:]...)

			node.Keys = node.Keys[:mid]
			node.Links = node.Links[:mid+1]
			node.MemChildren = node.MemChildren[:mid+1]
		}

		return splitKey, rightNode, nil
	}

	return BTreeKey{}, nil, nil
}
