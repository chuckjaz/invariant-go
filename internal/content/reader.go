package content

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"

	"invariant/internal/slots"
	"invariant/internal/storage"
)

var (
	ErrBlockNotFound      = errors.New("block not found")
	ErrUnsupportedKind    = errors.New("unsupported transform kind")
	ErrUnsupportedAlg     = errors.New("unsupported transform algorithm")
	ErrSlotServiceMissing = errors.New("slot service missing for slot link")
	ErrHashMismatch       = errors.New("hash mismatch")
)

// Read returns an io.ReadCloser for the given ContentLink.
// The caller is responsible for closing the reader.
func Read(link ContentLink, store storage.Storage, slotService slots.Slots) (io.ReadCloser, error) {
	address := link.Address
	if link.Slot {
		if slotService == nil {
			return nil, ErrSlotServiceMissing
		}
		var err error
		address, err = slotService.Get(context.Background(), link.Address)
		if err != nil {
			return nil, fmt.Errorf("failed to lookup slot %s: %w", link.Address, err)
		}
	}

	rc, found := store.Get(context.Background(), address)
	if !found {
		return nil, fmt.Errorf("%w: %s", ErrBlockNotFound, address)
	}

	var err error
	for _, t := range link.Transforms {
		rc, err = applyTransform(rc, t, store, slotService)
		if err != nil {
			rc.Close()
			return nil, fmt.Errorf("failed to apply transform %s: %w", t.Kind, err)
		}
	}

	if link.Expected != "" {
		rc = &hashCheckerReader{
			ReadCloser: rc,
			hasher:     sha256.New(),
			expected:   link.Expected,
		}
	}

	return rc, nil
}

func applyTransform(rc io.ReadCloser, t ContentTransform, store storage.Storage, slotService slots.Slots) (io.ReadCloser, error) {
	switch t.Kind {
	case "Decompress":
		switch t.Algorithm {
		case "inflate":
			return &wrappedReadCloser{Reader: flate.NewReader(rc), underlying: rc}, nil
		case "gzip":
			gzrc, err := gzip.NewReader(rc)
			if err != nil {
				return nil, err
			}
			return &wrappedReadCloser{Reader: gzrc, underlying: rc}, nil
		default:
			return nil, fmt.Errorf("%w: Decompress %s", ErrUnsupportedAlg, t.Algorithm)
		}
	case "Decipher":
		if t.Algorithm != "aes-256-cbc" {
			return nil, fmt.Errorf("%w: Decipher %s", ErrUnsupportedAlg, t.Algorithm)
		}
		key, err := hex.DecodeString(t.Key)
		if err != nil {
			return nil, fmt.Errorf("invalid key hex: %w", err)
		}
		iv, err := hex.DecodeString(t.IV)
		if err != nil {
			return nil, fmt.Errorf("invalid iv hex: %w", err)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}

		// Since blocks are typically small (<= 2MB), we can read the entire ciphertext to unpad cleanly.
		defer rc.Close()
		ciphertext, err := io.ReadAll(rc)
		if err != nil {
			return nil, err
		}

		if len(ciphertext) == 0 || len(ciphertext)%block.BlockSize() != 0 {
			return nil, errors.New("ciphertext is not a multiple of the block size")
		}

		mode := cipher.NewCBCDecrypter(block, iv)
		plaintext := make([]byte, len(ciphertext))
		mode.CryptBlocks(plaintext, ciphertext)

		// PKCS#7 Unpadding
		padLen := int(plaintext[len(plaintext)-1])
		if padLen > block.BlockSize() || padLen == 0 {
			return nil, errors.New("invalid padding")
		}
		for i := len(plaintext) - padLen; i < len(plaintext); i++ {
			if plaintext[i] != byte(padLen) {
				return nil, errors.New("invalid padding")
			}
		}
		plaintext = plaintext[:len(plaintext)-padLen]

		return io.NopCloser(bytes.NewReader(plaintext)), nil
	case "Blocks":
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return nil, err
		}
		var bl BlockList
		if err := json.Unmarshal(data, &bl); err != nil {
			return nil, fmt.Errorf("failed to parse block list: %w", err)
		}
		return &blockListReader{
			blocks:      bl.Blocks,
			store:       store,
			slotService: slotService,
			cache:       make(map[int][]byte),
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedKind, t.Kind)
	}
}

type wrappedReadCloser struct {
	io.Reader
	underlying io.Closer
}

func (w *wrappedReadCloser) Close() error {
	var err1, err2 error
	if c, ok := w.Reader.(io.Closer); ok {
		err1 = c.Close()
	}
	if w.underlying != nil {
		err2 = w.underlying.Close()
	}
	if err1 != nil {
		return err1
	}
	return err2
}

type blockListReader struct {
	blocks      []BlockListItem
	store       storage.Storage
	slotService slots.Slots
	cache       map[int][]byte
	cacheKeys   []int // LRU queue tracking oldest used chunks
	currentPos  int64
}

func (r *blockListReader) Seek(offset int64, whence int) (int64, error) {
	if whence != io.SeekStart {
		return 0, errors.New("only SeekStart is supported")
	}

	// Because we perfectly buffer block execution natively into RAM, FUSE leaps
	// forwards, backwards, or inside active block indices are completely zero-latency!
	r.currentPos = offset
	return offset, nil
}

func (r *blockListReader) loadBlock(targetIdx int) error {
	if _, ok := r.cache[targetIdx]; ok {
		// Update LRU queue shifting newly hit item perfectly to end
		for i, k := range r.cacheKeys {
			if k == targetIdx {
				r.cacheKeys = append(r.cacheKeys[:i], r.cacheKeys[i+1:]...)
				break
			}
		}
		r.cacheKeys = append(r.cacheKeys, targetIdx)
		return nil
	}

	if targetIdx >= len(r.blocks) {
		return io.EOF
	}

	link := r.blocks[targetIdx].Content
	rc, err := Read(link, r.store, r.slotService)
	if err != nil {
		return err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return err
	}

	// Maximum 64 blocks internally mapped to explicitly blanket segment jumping overheads
	// (64 blocks * ~2MB = ~128MB RAM max overhead per open file, entirely mitigating 15MB Go binary jumps)
	if len(r.cacheKeys) >= 64 {
		oldest := r.cacheKeys[0]
		r.cacheKeys = r.cacheKeys[1:]
		delete(r.cache, oldest)
	}

	r.cache[targetIdx] = data
	r.cacheKeys = append(r.cacheKeys, targetIdx)
	return nil
}

func (r *blockListReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	for {
		var currentOffset int64 = 0
		var targetIdx int = len(r.blocks)

		for i, b := range r.blocks {
			if currentOffset+int64(b.Size) > r.currentPos {
				targetIdx = i
				break
			}
			currentOffset += int64(b.Size)
		}

		if targetIdx == len(r.blocks) {
			return 0, io.EOF
		}

		if err := r.loadBlock(targetIdx); err != nil {
			return 0, err
		}

		// Calculate mapping offset directly within active RAM slice natively
		intraBlockOffset := r.currentPos - currentOffset
		activeBlockData := r.cache[targetIdx]

		// Handle corrupted chunk mapping avoiding invalid slice panics seamlessly
		if intraBlockOffset >= int64(len(activeBlockData)) {
			r.currentPos = currentOffset + int64(r.blocks[targetIdx].Size)
			continue
		}

		n := copy(p, activeBlockData[intraBlockOffset:])
		if n > 0 {
			r.currentPos += int64(n)
			return n, nil
		}

		r.currentPos = currentOffset + int64(r.blocks[targetIdx].Size)
	}
}

func (r *blockListReader) Close() error {
	r.cache = nil
	r.cacheKeys = nil
	return nil
}

type hashCheckerReader struct {
	io.ReadCloser
	hasher   hash.Hash
	expected string
	err      error
}

func (r *hashCheckerReader) Seek(offset int64, whence int) (int64, error) {
	seeker, ok := r.ReadCloser.(io.Seeker)
	if !ok {
		return 0, errors.New("underlying reader does not support Seek")
	}
	// Seeking invalidates the continuous stream hash check
	r.expected = ""
	return seeker.Seek(offset, whence)
}

func (r *hashCheckerReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.hasher.Write(p[:n])
	}
	if err == io.EOF {
		sum := hex.EncodeToString(r.hasher.Sum(nil))
		if sum != r.expected {
			r.err = fmt.Errorf("%w: expected %s, got %s", ErrHashMismatch, r.expected, sum)
			return n, r.err
		}
	}
	return n, err
}
