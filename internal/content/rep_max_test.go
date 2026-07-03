package content

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"io"
	"testing"
)

func TestNewSeededGearTable(t *testing.T) {
	seed1 := []byte("seed-test-1")
	seed2 := []byte("seed-test-2")

	gt1 := NewSeededGearTable(seed1)
	gt1Again := NewSeededGearTable(seed1)
	gt2 := NewSeededGearTable(seed2)

	// Verify determinism
	if gt1.values != gt1Again.values {
		t.Error("NewSeededGearTable: Expected same table for same seed")
	}

	// Verify uniqueness
	if gt1.values == gt2.values {
		t.Error("NewSeededGearTable: Expected different tables for different seeds")
	}
}

func TestRepMaxContentDefinedChunker_Basic(t *testing.T) {
	// Let's create a chunker with very small minSizeBytes so it splits easily
	minSize := 1024
	horizon := 2048
	gearTable := &FastContentDefinedChunkerGearTable

	// Generate random test data
	data := make([]byte, 100*1024)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}

	r := bufio.NewReader(bytes.NewReader(data))
	chunker := NewRepMaxContentDefinedChunker(r, gearTable, minSize, horizon)

	var chunks [][]byte
	for {
		chunk, err := chunker.ReadNextChunk()
		if len(chunk) > 0 {
			chunkCopy := make([]byte, len(chunk))
			copy(chunkCopy, chunk)
			chunks = append(chunks, chunkCopy)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadNextChunk failed: %v", err)
		}
	}

	// Verify that we reconstructed the original data
	var reconstructed bytes.Buffer
	for i, chunk := range chunks {
		reconstructed.Write(chunk)
		// Each chunk must be at least minSizeBytes (except possibly the last one)
		if len(chunk) < minSize && i < len(chunks)-1 {
			t.Errorf("Chunk size %d is less than minSize %d", len(chunk), minSize)
		}
	}

	if !bytes.Equal(data, reconstructed.Bytes()) {
		t.Error("Reconstructed data does not match original data")
	}
}

func TestRepMaxSplitter(t *testing.T) {
	s := &RepMaxSplitter{}
	if !s.Match(nil, "", "") {
		t.Error("RepMaxSplitter should always match")
	}

	data := make([]byte, 200*1024)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}

	writeChunkCount := 0
	writeChunk := func(chunk []byte) (ContentLink, error) {
		writeChunkCount++
		return ContentLink{Address: "chunk"}, nil
	}

	writeStream := func(r io.Reader, opts WriterOptions) (ContentLink, error) {
		return ContentLink{Address: "stream"}, nil
	}

	blocks, err := s.Split(bytes.NewReader(data), WriterOptions{}, writeChunk, writeStream)
	if err != nil {
		t.Fatalf("RepMaxSplitter.Split failed: %v", err)
	}

	if len(blocks) == 0 {
		t.Error("RepMaxSplitter should have returned blocks")
	}
}

func TestBuzHashSplitter_Match(t *testing.T) {
	s := &BuzHashSplitter{}
	if !s.Match(nil, "", "") {
		t.Error("BuzHashSplitter should always match")
	}
}

func TestWriteBlockList_Recursion(t *testing.T) {
	store := newInMemoryStorage()

	// Create a large list of BlockListItems.
	// Since writeBlockList only marshals and writes to store without reading,
	// we can use dummy items.
	items := make([]BlockListItem, 60000)
	for i := range items {
		items[i] = BlockListItem{
			Content: ContentLink{Address: "dummy-address"},
			Size:    100,
		}
	}

	opts := WriterOptions{}
	sharedKey := make([]byte, 32)

	link, err := writeBlockList(items, store, opts, sharedKey, "expected-hash-123")
	if err != nil {
		t.Fatalf("writeBlockList failed: %v", err)
	}

	if link.Address == "" {
		t.Error("Expected valid address in link")
	}
	if link.Expected != "expected-hash-123" {
		t.Errorf("Expected overallExpectedHash to be set, got %q", link.Expected)
	}
}

type dummyReadCloser struct {
	io.Reader
}

func (d *dummyReadCloser) Close() error {
	return nil
}

func TestHashCheckerReader_SeekNonSeeker(t *testing.T) {
	r := &hashCheckerReader{
		ReadCloser: &dummyReadCloser{Reader: bytes.NewReader(nil)},
	}
	_, err := r.Seek(0, io.SeekStart)
	if err == nil || err.Error() != "underlying reader does not support Seek" {
		t.Errorf("Expected 'underlying reader does not support Seek' error, got %v", err)
	}
}

func TestBlockListReader_Seek(t *testing.T) {
	store := newInMemoryStorage()

	// Write a multi-block file
	// We want to force it to create a block list, so we'll use a large file.
	// Let's write 3.5 MB of random data.
	data := make([]byte, 7*512*1024)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}

	opts := WriterOptions{}
	link, err := Write(bytes.NewReader(data), store, opts)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Read it back
	rc, err := Read(link, store, nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	defer rc.Close()

	seeker, ok := rc.(io.Seeker)
	if !ok {
		t.Fatal("Expected returned reader to implement io.Seeker")
	}

	// Seek to start (valid)
	pos, err := seeker.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek failed: %v", err)
	}
	if pos != 0 {
		t.Errorf("Expected pos 0, got %d", pos)
	}

	// Try seeking with invalid whence
	_, err = seeker.Seek(0, io.SeekCurrent)
	if err == nil {
		t.Error("Expected error on SeekCurrent, got nil")
	}

	// Seek to middle
	pos, err = seeker.Seek(1024*1024, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek failed: %v", err)
	}
	if pos != 1024*1024 {
		t.Errorf("Expected pos 1024*1024, got %d", pos)
	}

	// Read from seek position
	buf := make([]byte, 100)
	n, err := rc.Read(buf)
	if err != nil {
		t.Fatalf("Read after seek failed: %v", err)
	}
	if n != len(buf) {
		t.Fatalf("Expected to read %d bytes, got %d", len(buf), n)
	}

	if !bytes.Equal(data[1024*1024:1024*1024+100], buf) {
		t.Error("Read data after seek does not match original data")
	}
}
