package kv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// ValueThreshold is the size limit for storing values inline in the B-Tree node.
const ValueThreshold = 1024 // 1K

type BTreeKey struct {
	Key      string `json:"k"`
	Sequence uint64 `json:"s"`
}

// CompareBTreeKey compares two keys.
// Primary sort is Key ascending.
// Secondary sort is Sequence descending, so the latest version of a key comes first.
func CompareBTreeKey(a, b BTreeKey) int {
	if a.Key < b.Key {
		return -1
	}
	if a.Key > b.Key {
		return 1
	}
	if a.Sequence > b.Sequence {
		return -1
	}
	if a.Sequence < b.Sequence {
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
	rc, err := content.Read(link, b.store, b.slotClient)
	if err != nil {
		return nil, fmt.Errorf("node not found: %s", link.Address)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	return DeserializeBTreeNode(data)
}

func (b *BTree) saveNode(ctx context.Context, node *BTreeNode) (content.ContentLink, error) {
	data, err := node.Serialize()
	if err != nil {
		return content.ContentLink{}, err
	}
	return content.Write(bytes.NewReader(data), b.store, b.opts)
}

// Search returns the value entry for the latest sequence of the given key.
func (b *BTree) Search(ctx context.Context, rootAddr *content.ContentLink, key string) (ValueEntry, bool, error) {
	if rootAddr == nil {
		return ValueEntry{}, false, nil
	}
	return b.searchRecursive(ctx, *rootAddr, key)
}

func (b *BTree) searchRecursive(ctx context.Context, addr content.ContentLink, key string) (ValueEntry, bool, error) {
	node, err := b.loadNode(ctx, addr)
	if err != nil {
		return ValueEntry{}, false, err
	}

	if node.IsLeaf {
		for i := 0; i < len(node.Keys); i++ {
			if node.Keys[i].Key == key {
				return node.Values[i], true, nil
			}
			if node.Keys[i].Key > key {
				break
			}
		}
		return ValueEntry{}, false, nil
	}

	// Internal node
	// Find the first child that could contain `key`.
	i := 0
	for i < len(node.Keys) && node.Keys[i].Key < key {
		i++
	}

	// Child i can contain `key` because its upper bound is node.Keys[i] >= key.
	// Or if i == len(node.Keys), its upper bound is +infinity.
	val, found, err := b.searchRecursive(ctx, node.Children[i], key)
	if err != nil {
		return ValueEntry{}, false, err
	}
	if found {
		return val, true, nil
	}

	// If not found in child i, could it be in subsequent children?
	// Yes, if node.Keys[i].Key == key.
	for i < len(node.Keys) && node.Keys[i].Key == key {
		i++
		val, found, err := b.searchRecursive(ctx, node.Children[i], key)
		if err != nil {
			return ValueEntry{}, false, err
		}
		if found {
			return val, true, nil
		}
	}

	return ValueEntry{}, false, nil
}

// InsertBatch inserts multiple records functionally and returns the new root address.
func (b *BTree) InsertBatch(ctx context.Context, rootAddr *content.ContentLink, records []Record, lastJournal *content.ContentLink) (content.ContentLink, error) {
	var root *BTreeNode
	if rootAddr == nil {
		root = &BTreeNode{IsLeaf: true, LastJournal: lastJournal}
	} else {
		var err error
		root, err = b.loadNode(ctx, *rootAddr)
		if err != nil {
			return content.ContentLink{}, err
		}
		// Copy root to functional update
		root = cloneNode(root)
		root.LastJournal = lastJournal
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

		k := BTreeKey{Key: rec.Key, Sequence: rec.Sequence}
		var err error
		root, err = b.insert(ctx, root, k, valEntry)
		if err != nil {
			return content.ContentLink{}, err
		}
	}

	return b.saveNode(ctx, root)
}

func cloneNode(n *BTreeNode) *BTreeNode {
	c := &BTreeNode{
		IsLeaf:      n.IsLeaf,
		LastJournal: n.LastJournal,
	}
	c.Keys = append([]BTreeKey(nil), n.Keys...)
	if n.IsLeaf {
		c.Values = append([]ValueEntry(nil), n.Values...)
	} else {
		c.Children = append([]content.ContentLink(nil), n.Children...)
	}
	return c
}

func (b *BTree) insert(ctx context.Context, node *BTreeNode, key BTreeKey, val ValueEntry) (*BTreeNode, error) {
	// A functional insert that might split the node.
	// We will use a standard B-tree insertion approach, but return the modified node (or a new root if it splits).
	// Since we need to save children as we go back up, we actually have to write the children to storage and update their addresses.
	// Let's implement a recursive insert.

	newNode, splitKey, splitChild, err := b.insertRecursive(ctx, node, key, val)
	if err != nil {
		return nil, err
	}

	if splitChild != nil {
		// The root split, create a new root
		newRoot := &BTreeNode{
			IsLeaf:      false,
			Keys:        []BTreeKey{splitKey},
			Children:    []content.ContentLink{{}, *splitChild}, // Left child address will be filled after saving the split left half. Wait, no, newNode is the left half!
			LastJournal: node.LastJournal,                       // carry it up
		}

		leftAddr, err := b.saveNode(ctx, newNode)
		if err != nil {
			return nil, err
		}
		newRoot.Children[0] = leftAddr
		return newRoot, nil
	}

	return newNode, nil
}

func (b *BTree) insertRecursive(ctx context.Context, node *BTreeNode, key BTreeKey, val ValueEntry) (*BTreeNode, BTreeKey, *content.ContentLink, error) {
	node = cloneNode(node) // functional copy

	i := 0
	for i < len(node.Keys) && CompareBTreeKey(key, node.Keys[i]) > 0 {
		i++
	}

	if node.IsLeaf {
		// Insert into leaf
		if i < len(node.Keys) && CompareBTreeKey(key, node.Keys[i]) == 0 {
			// Update existing (though sequence should usually prevent exact matches, unless same seq is pushed)
			node.Values[i] = val
		} else {
			// Insert new
			node.Keys = append(node.Keys[:i], append([]BTreeKey{key}, node.Keys[i:]...)...)
			node.Values = append(node.Values[:i], append([]ValueEntry{val}, node.Values[i:]...)...)
		}
	} else {
		// Insert into internal node
		childAddr := node.Children[i]
		child, err := b.loadNode(ctx, childAddr)
		if err != nil {
			return nil, BTreeKey{}, nil, err
		}

		newChild, splitKey, splitRightAddr, err := b.insertRecursive(ctx, child, key, val)
		if err != nil {
			return nil, BTreeKey{}, nil, err
		}

		newChildAddr, err := b.saveNode(ctx, newChild)
		if err != nil {
			return nil, BTreeKey{}, nil, err
		}

		node.Children[i] = newChildAddr

		if splitRightAddr != nil {
			// Child split, we need to insert splitKey and splitRightAddr into this node
			node.Keys = append(node.Keys[:i], append([]BTreeKey{splitKey}, node.Keys[i:]...)...)
			node.Children = append(node.Children[:i+1], append([]content.ContentLink{*splitRightAddr}, node.Children[i+1:]...)...)
		}
	}

	// Check if this node needs to split
	if len(node.Keys) > b.maxKeys {
		mid := len(node.Keys) / 2
		splitKey := node.Keys[mid]

		rightNode := &BTreeNode{IsLeaf: node.IsLeaf}

		if node.IsLeaf {
			// B+Tree leaf split: right node keeps the mid key
			rightNode.Keys = append([]BTreeKey(nil), node.Keys[mid:]...)
			rightNode.Values = append([]ValueEntry(nil), node.Values[mid:]...)

			node.Keys = node.Keys[:mid]
			node.Values = node.Values[:mid]
		} else {
			// Internal node split: right node does not keep the mid key
			rightNode.Keys = append([]BTreeKey(nil), node.Keys[mid+1:]...)
			rightNode.Children = append([]content.ContentLink(nil), node.Children[mid+1:]...)

			node.Keys = node.Keys[:mid]
			node.Children = node.Children[:mid+1]
		}

		rightAddr, err := b.saveNode(ctx, rightNode)
		if err != nil {
			return nil, BTreeKey{}, nil, err
		}

		return node, splitKey, &rightAddr, nil
	}

	return node, BTreeKey{}, nil, nil
}

type Record struct {
	Key      string
	Sequence uint64
	Value    []byte
}
