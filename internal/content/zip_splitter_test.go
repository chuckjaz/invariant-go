package content

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func createDummyZipEntry(name string, payload []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x50, 0x4b, 0x03, 0x04})
	// Fixed header is 26 bytes
	header := make([]byte, 26)
	// byte 14 is compressedSize
	binary.LittleEndian.PutUint32(header[14:18], uint32(len(payload)))
	// byte 22 is fileNameLen
	binary.LittleEndian.PutUint16(header[22:24], uint16(len(name)))
	buf.Write(header)
	buf.WriteString(name)
	buf.Write(payload)
	return buf.Bytes()
}

func TestZipSplitter(t *testing.T) {
	smallPayload := []byte("hello small payload")
	largePayload := make([]byte, 1024*1024+500)
	for i := range largePayload {
		largePayload[i] = 'L'
	}
	smallPayload2 := []byte("another small payload after large file")

	var zipData []byte
	zipData = append(zipData, createDummyZipEntry("small1.txt", smallPayload)...)
	zipData = append(zipData, createDummyZipEntry("large.txt", largePayload)...)
	zipData = append(zipData, createDummyZipEntry("small2.txt", smallPayload2)...)
	// Central directory simulation
	zipData = append(zipData, []byte{0x50, 0x4b, 0x01, 0x02, 0, 0, 0, 0}...)

	s := &ZipSplitter{}

	if !s.Match(zipData[:1024], "", "") {
		t.Fatalf("ZipSplitter should match the zip file")
	}

	streamCounter := 0
	chunkCounter := 0

	writeChunk := func(chunk []byte) (ContentLink, error) {
		chunkCounter++
		return ContentLink{Address: "chunk"}, nil
	}

	writeStream := func(inner io.Reader, opts WriterOptions) (ContentLink, error) {
		streamCounter++
		// Just consume the stream
		io.Copy(io.Discard, inner)
		return ContentLink{Address: "streamList"}, nil
	}

	blocks, err := s.Split(bytes.NewReader(zipData), WriterOptions{}, writeChunk, writeStream)
	if err != nil {
		t.Fatalf("Splitter failed: %v", err)
	}

	// We expect the zip to be split.
	// small1.txt -> 1 chunk (header + data)
	// large.txt -> 1 chunk (header), 1 stream (payload)
	// small2.txt -> 1 chunk (header + data)
	// Central directory remainder -> 1 stream

	expectedChunks := 3  // small1 chunk, large header chunk, small2 chunk
	expectedStreams := 2 // large payload stream, central directory stream

	if chunkCounter != expectedChunks {
		t.Errorf("Expected %d chunks, got %d", expectedChunks, chunkCounter)
	}

	if streamCounter != expectedStreams {
		t.Errorf("Expected %d streams, got %d", expectedStreams, streamCounter)
	}

	if len(blocks) != expectedChunks+expectedStreams {
		t.Errorf("Expected %d blocks total, got %d", expectedChunks+expectedStreams, len(blocks))
	}
}
