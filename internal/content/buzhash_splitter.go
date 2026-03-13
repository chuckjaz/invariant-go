package content

import (
	"bytes"
	"io"
)

// BuzHashSplitter is the default splitter that chunks the stream using a BuzHash rolling hash.
type BuzHashSplitter struct{}

func (s *BuzHashSplitter) Match(head []byte, filename, contentType string) bool {
	return true // Always matches as the default fallback
}

func (s *BuzHashSplitter) Split(r io.Reader, opts WriterOptions, writeChunk func([]byte) (ContentLink, error), writeStream func(io.Reader, WriterOptions) (ContentLink, error)) ([]BlockListItem, error) {
	bh := NewBuzHash(64)
	mask := uint32((1 << 20) - 1) // 1MB avg target

	var blocks []BlockListItem
	var currentChunk bytes.Buffer
	buf := make([]byte, 32*1024) // 32KB read buffer

	for {
		n, err := r.Read(buf)
		if n > 0 {
			for i := 0; i < n; i++ {
				b := buf[i]
				h := bh.WriteB(b)
				currentChunk.WriteByte(b)
				size := currentChunk.Len()

				if (h&mask == 0 && size >= targetBlockSize/2) || size == maxBlockSize {
					chunkData := make([]byte, currentChunk.Len())
					copy(chunkData, currentChunk.Bytes())
					link, wErr := writeChunk(chunkData)
					if wErr != nil {
						return nil, wErr
					}
					blocks = append(blocks, BlockListItem{
						Content: link,
						Size:    uint64(size),
					})
					currentChunk.Reset()
					bh = NewBuzHash(64)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	if currentChunk.Len() > 0 || len(blocks) == 0 {
		size := currentChunk.Len()
		chunkData := make([]byte, currentChunk.Len())
		copy(chunkData, currentChunk.Bytes())
		link, err := writeChunk(chunkData)
		if err != nil {
			return nil, err
		}

		blocks = append(blocks, BlockListItem{
			Content: link,
			Size:    uint64(size),
		})
	}

	return blocks, nil
}
