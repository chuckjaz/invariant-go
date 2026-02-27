package content

// ContentLink defines the location and retrieval parameters of content or a block.
type ContentLink struct {
	Address    string             `json:"address"`
	Slot       bool               `json:"slot,omitempty"`
	Transforms []ContentTransform `json:"transforms,omitempty"`
	Expected   string             `json:"expected,omitempty"`
	Primary    string             `json:"primary,omitempty"`
}

// ContentTransform defines a transformation to apply to content during retrieval.
type ContentTransform struct {
	Kind      string `json:"kind"`                // "Blocks", "Decipher", or "Decompress"
	Algorithm string `json:"algorithm,omitempty"` // For Decipher ("aes-256-cbc") or Decompress ("inflate", "gzip")
	Key       string `json:"key,omitempty"`       // Hex string, base64, or raw? The spec says "string", typically hex or base64. Let's assume hex since it's common.
	IV        string `json:"iv,omitempty"`        // Usually hex or base64. Let's assume hex.
}

// BlockListItem is an item in a BlockList.
type BlockListItem struct {
	Content ContentLink `json:"content"`
	Size    uint64      `json:"size"`
}

// BlockList defines a list of blocks that make up a larger file or content.
type BlockList struct {
	Blocks []BlockListItem `json:"blocks"`
}
