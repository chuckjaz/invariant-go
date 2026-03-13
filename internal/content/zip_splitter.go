package content

import (
	"bytes"
	"encoding/binary"
	"io"
)

// ZipSplitter splits ZIP streams exactly on local file boundaries.
type ZipSplitter struct{}

func (s *ZipSplitter) Match(head []byte, filename, contentType string) bool {
	return len(head) >= 4 && bytes.HasPrefix(head, []byte{0x50, 0x4b, 0x03, 0x04})
}

// trackingReader tracks bytes read to accurately record block sizes.
type trackingReader struct {
	r    io.Reader
	size uint64
}

func (t *trackingReader) Read(p []byte) (n int, err error) {
	n, err = t.r.Read(p)
	t.size += uint64(n)
	return
}

func (s *ZipSplitter) Split(r io.Reader, opts WriterOptions, writeChunk func([]byte) (ContentLink, error), writeStream func(io.Reader, WriterOptions) (ContentLink, error)) ([]BlockListItem, error) {
	var blocks []BlockListItem
	var remainderBuf bytes.Buffer

	for {
		var sig [4]byte
		if _, err := io.ReadFull(r, sig[:]); err != nil {
			if err == io.EOF {
				break
			}
			// Unexpected EOF might just be a truncated file stream; we return it
			return nil, err
		}

		if bytes.Equal(sig[:], []byte{0x50, 0x4b, 0x03, 0x04}) {
			// Local file header
			var header [26]byte
			if _, err := io.ReadFull(r, header[:]); err != nil {
				return nil, err
			}

			flags := binary.LittleEndian.Uint16(header[2:4])
			compressedSize := binary.LittleEndian.Uint32(header[14:18])
			fileNameLen := binary.LittleEndian.Uint16(header[22:24])
			extraFieldLen := binary.LittleEndian.Uint16(header[24:26])

			var headerData []byte
			headerData = append(headerData, sig[:]...)
			headerData = append(headerData, header[:]...)

			nameExtra := make([]byte, int(fileNameLen)+int(extraFieldLen))
			if _, err := io.ReadFull(r, nameExtra); err != nil {
				return nil, err
			}
			headerData = append(headerData, nameExtra...)

			// If bit 3 is set, compressed size is unknown inside the local header.
			// It uses a data descriptor after the payload.
			// We cannot reliably stream this inline without scanning for the data descriptor.
			// We fallback to treating this file and the rest of the stream as the remainder.
			if flags&0x8 != 0 && compressedSize == 0 {
				remainderBuf.Write(headerData)
				break
			}

			totalSize := uint64(len(headerData)) + uint64(compressedSize)

			if totalSize <= 1024*1024 { // 1MB bounds check
				// Payload is small enough, bundle header and payload into one terminal chunk
				payload := make([]byte, compressedSize)
				if _, err := io.ReadFull(r, payload); err != nil {
					return nil, err
				}
				fullData := append(headerData, payload...)
				link, err := writeChunk(fullData)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, BlockListItem{
					Content: link,
					Size:    uint64(len(fullData)),
				})
			} else {
				// File payload is large. Write the small header as a dedicated chunk.
				linkHeader, err := writeChunk(headerData)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, BlockListItem{
					Content: linkHeader,
					Size:    uint64(len(headerData)),
				})

				// Recursively stream the payload content to evaluate any fallback splits (e.g. BuzHash over 1MB payload)
				payloadReader := io.LimitReader(r, int64(compressedSize))
				tr := &trackingReader{r: payloadReader}

				linkPayload, err := writeStream(tr, opts)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, BlockListItem{
					Content: linkPayload,
					Size:    tr.size,
				})
			}
		} else {
			// Not a local file header (most likely Central Directory `PK\x01\x02` or End of Central Directory).
			// End the precise file boundary loop and pipe the remainder natively.
			remainderBuf.Write(sig[:])
			break
		}
	}

	// Dump remaining stream (such as central directory footers)
	// They will be handled generically by other sequential splitters in writeStream.
	if remainderBuf.Len() > 0 {
		multiR := io.MultiReader(&remainderBuf, r)
		tr := &trackingReader{r: multiR}

		link, err := writeStream(tr, opts)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, BlockListItem{
			Content: link,
			Size:    tr.size,
		})
	}

	return blocks, nil
}
