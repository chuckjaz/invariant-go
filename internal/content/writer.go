package content

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"invariant/internal/storage"
)

type KeyPolicy string

const (
	RandomPerBlock KeyPolicy = "RandomPerBlock"
	RandomAllKey   KeyPolicy = "RandomAllKey"
	Deterministic  KeyPolicy = "Deterministic"
	SuppliedAllKey KeyPolicy = "SuppliedAllKey"
)

// WriterOptions configure how the content writer handles blocks.
type WriterOptions struct {
	CompressAlgorithm string    // "inflate", "gzip", or empty for none
	EncryptAlgorithm  string    // "aes-256-cbc" or empty for none
	KeyPolicy         KeyPolicy // specifies how to derive encryption keys
	SuppliedKey       []byte    // The encryption key to use when KeyPolicy is SuppliedAllKey
}

const (
	maxBlockSize    = 2 * 1024 * 1024
	targetBlockSize = 1024 * 1024
)

// Write reads from r, splits it into ~1MB blocks using a rolling hash,
// applies compression and encryption according to opts,
// writes the blocks to store, and returns a ContentLink to the root block (or block list).
func Write(r io.Reader, store storage.Storage, opts WriterOptions) (ContentLink, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return ContentLink{}, err
	}

	if len(data) == 0 {
		return writeBlock(nil, store, opts, nil)
	}

	if len(data) <= targetBlockSize {
		return writeBlock(data, store, opts, nil)
	}

	// For stream > 1MB, split into blocks.
	var sharedKey []byte
	switch opts.KeyPolicy {
	case RandomAllKey:
		sharedKey = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, sharedKey); err != nil {
			return ContentLink{}, err
		}
	case SuppliedAllKey:
		if len(opts.SuppliedKey) != 32 {
			return ContentLink{}, fmt.Errorf("SuppliedKey must be 32 bytes for aes-256-cbc")
		}
		sharedKey = opts.SuppliedKey
	}

	blocks, err := splitBlocks(data, store, opts, sharedKey)
	if err != nil {
		return ContentLink{}, err
	}

	return writeBlockList(blocks, store, opts, sharedKey)
}

func splitBlocks(data []byte, store storage.Storage, opts WriterOptions, sharedKey []byte) ([]BlockListItem, error) {
	var blocks []BlockListItem
	bh := NewBuzHash(64)

	mask := uint32((1 << 20) - 1) // 1MB avg target

	start := 0
	for i := range data {
		h := bh.WriteB(data[i])
		size := i - start + 1

		if (h&mask == 0 && size >= targetBlockSize/2) || size == maxBlockSize || i == len(data)-1 {
			chunk := data[start : i+1]
			link, err := writeBlock(chunk, store, opts, sharedKey)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, BlockListItem{
				Content: link,
				Size:    uint64(len(chunk)),
			})
			start = i + 1
			bh = NewBuzHash(64)
		}
	}

	return blocks, nil
}

func writeBlockList(items []BlockListItem, store storage.Storage, opts WriterOptions, sharedKey []byte) (ContentLink, error) {
	// A JSON block list might exceed 1MB if there are many items.
	// We'll recursively split if it's too large.

	data, err := json.Marshal(BlockList{Blocks: items})
	if err != nil {
		return ContentLink{}, err
	}

	// Since small objects are more common, we check if list fits into max block limit
	if len(data) <= maxBlockSize {
		link, err := writeBlock(data, store, opts, sharedKey)
		if err != nil {
			return ContentLink{}, err
		}
		// Append 'Blocks' transform so it runs last
		link.Transforms = append(link.Transforms, ContentTransform{Kind: "Blocks"})
		link.Expected = "" // block list doesn't have an expected hash unless we hash the actual JSON bytes, but the standard says it's for the underlying data
		return link, nil
	}

	// JSON is too large, split it again conceptually. Since BlockList items are large enough (1MB each),
	// this would mean we have a file sizes approx ~2000 x 1MB = 2GB before list hits 1MB threshold.
	// The problem statement requires splitting block lists if they exceed 2MB.

	// Break items into chunks of e.g. 1000.
	chunks := ceilDiv(len(items), 1000)
	var parentItems []BlockListItem

	for i := range chunks {
		start := i * 1000
		end := min(start+1000, len(items))

		subListLink, err := writeBlockList(items[start:end], store, opts, sharedKey)
		if err != nil {
			return ContentLink{}, err
		}

		// calculate total size of sublist
		var subSize uint64
		for _, b := range items[start:end] {
			subSize += b.Size
		}

		parentItems = append(parentItems, BlockListItem{
			Content: subListLink,
			Size:    subSize,
		})
	}

	return writeBlockList(parentItems, store, opts, sharedKey)
}

func ceilDiv(a, b int) int {
	return (a + b - 1) / b
}

func writeBlock(data []byte, store storage.Storage, opts WriterOptions, sharedKey []byte) (ContentLink, error) {
	link := ContentLink{}

	// Compute expected hash of the plaintext
	hasher := sha256.New()
	hasher.Write(data)
	link.Expected = hex.EncodeToString(hasher.Sum(nil))

	var transforms []ContentTransform
	currentData := data

	if opts.CompressAlgorithm != "" {
		var b bytes.Buffer
		switch opts.CompressAlgorithm {
		case "inflate":
			w, err := flate.NewWriter(&b, flate.DefaultCompression)
			if err != nil {
				return link, err
			}
			w.Write(currentData)
			w.Close()
		case "gzip":
			w := gzip.NewWriter(&b)
			w.Write(currentData)
			w.Close()
		default:
			return link, fmt.Errorf("unsupported compression: %s", opts.CompressAlgorithm)
		}
		currentData = b.Bytes()
		// Decompression happens second when reading
		transforms = append([]ContentTransform{{
			Kind:      "Decompress",
			Algorithm: opts.CompressAlgorithm,
		}}, transforms...)
	}

	if opts.EncryptAlgorithm != "" {
		if opts.EncryptAlgorithm != "aes-256-cbc" {
			return link, fmt.Errorf("unsupported encryption: %s", opts.EncryptAlgorithm)
		}

		var key []byte
		switch opts.KeyPolicy {
		case RandomPerBlock, "": // Default to highest security
			key = make([]byte, 32)
			if _, err := io.ReadFull(rand.Reader, key); err != nil {
				return link, err
			}
		case RandomAllKey:
			if sharedKey == nil {
				sharedKey = make([]byte, 32)
				if _, err := io.ReadFull(rand.Reader, sharedKey); err != nil {
					return link, err
				}
			}
			key = sharedKey
		case SuppliedAllKey:
			if sharedKey != nil {
				key = sharedKey
			} else {
				if len(opts.SuppliedKey) != 32 {
					return link, fmt.Errorf("SuppliedKey must be 32 bytes for aes-256-cbc")
				}
				key = opts.SuppliedKey
			}
		case Deterministic: // Hash of data as key
			h := sha256.Sum256(data)
			key = h[:]
		default:
			return link, fmt.Errorf("unsupported key policy: %v", opts.KeyPolicy)
		}

		iv := make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, iv); err != nil {
			return link, err
		}

		block, err := aes.NewCipher(key)
		if err != nil {
			return link, err
		}

		// PKCS#7 padding
		padLen := 16 - (len(currentData) % 16)
		if padLen == 0 {
			padLen = 16
		}
		padding := make([]byte, padLen)
		for i := range padding {
			padding[i] = byte(padLen)
		}

		paddedData := append([]byte(nil), currentData...)
		paddedData = append(paddedData, padding...)

		mode := cipher.NewCBCEncrypter(block, iv)
		ciphertext := make([]byte, len(paddedData))
		mode.CryptBlocks(ciphertext, paddedData)
		currentData = ciphertext

		// Decryption happens first when reading
		transforms = append([]ContentTransform{{
			Kind:      "Decipher",
			Algorithm: "aes-256-cbc",
			Key:       hex.EncodeToString(key),
			IV:        hex.EncodeToString(iv),
		}}, transforms...)
	}

	link.Transforms = transforms

	addr, err := store.Store(bytes.NewReader(currentData))
	if err != nil {
		return link, err
	}
	link.Address = addr

	return link, nil
}
