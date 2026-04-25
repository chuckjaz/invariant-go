package content

import (
	"bufio"
	"io"
)

// RepMaxSplitter chunks the stream using the RepMaxContentDefinedChunker.
type RepMaxSplitter struct{}

func (s *RepMaxSplitter) Match(head []byte, filename, contentType string) bool {
	return true // Fallback fallback like BuzHashSplitter
}

func (s *RepMaxSplitter) Split(r io.Reader, opts WriterOptions, writeChunk func([]byte) (ContentLink, error), writeStream func(io.Reader, WriterOptions) (ContentLink, error)) ([]BlockListItem, error) {
	minChunkSize := targetBlockSize / 2
	horizon := 128 * 1024 // 128KB horizon
	gearTable := &FastContentDefinedChunkerGearTable

	chunker := NewRepMaxContentDefinedChunker(bufio.NewReader(r), gearTable, minChunkSize, horizon)

	var blocks []BlockListItem

	for {
		chunk, err := chunker.ReadNextChunk()
		if len(chunk) > 0 {
			chunkData := make([]byte, len(chunk))
			copy(chunkData, chunk)
			link, wErr := writeChunk(chunkData)
			if wErr != nil {
				return nil, wErr
			}
			blocks = append(blocks, BlockListItem{
				Content: link,
				Size:    uint64(len(chunk)),
			})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	return blocks, nil
}
