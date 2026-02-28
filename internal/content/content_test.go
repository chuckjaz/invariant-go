package content_test

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"

	"invariant/internal/content"
	"invariant/internal/storage"
)

func TestReadWriteBasic(t *testing.T) {
	store := storage.NewInMemoryStorage()

	data := []byte("hello world")
	link, err := content.Write(bytes.NewReader(data), store, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	rc, err := content.Read(link, store, nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	defer rc.Close()

	readData, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !bytes.Equal(data, readData) {
		t.Errorf("Expected %q, got %q", data, readData)
	}
}

func TestReadWriteEncryptedCompressed(t *testing.T) {
	store := storage.NewInMemoryStorage()

	data := []byte("hello world with compression and encryption")

	for range 10 {
		data = append(data, []byte("hello world with compression and encryption ")...)
	}

	opts := content.WriterOptions{
		CompressAlgorithm: "gzip",
		EncryptAlgorithm:  "aes-256-cbc",
		KeyPolicy:         content.Deterministic,
	}

	link, err := content.Write(bytes.NewReader(data), store, opts)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if len(link.Transforms) != 2 {
		t.Errorf("Expected 2 transforms, got %v", len(link.Transforms))
	} else {
		if link.Transforms[0].Kind != "Decipher" {
			t.Errorf("Expected first transform to be Decipher")
		}
		if link.Transforms[1].Kind != "Decompress" {
			t.Errorf("Expected second transform to be Decompress")
		}
	}

	rc, err := content.Read(link, store, nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	defer rc.Close()

	readData, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !bytes.Equal(data, readData) {
		t.Errorf("Read data does not match original")
	}
}

func TestReadWriteLarge(t *testing.T) {
	store := storage.NewInMemoryStorage()

	data := make([]byte, 5*1024*1024)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}

	opts := content.WriterOptions{
		CompressAlgorithm: "inflate",
		EncryptAlgorithm:  "aes-256-cbc",
		KeyPolicy:         content.RandomAllKey,
	}

	link, err := content.Write(bytes.NewReader(data), store, opts)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if len(link.Transforms) == 0 || link.Transforms[len(link.Transforms)-1].Kind != "Blocks" {
		t.Errorf("Expected last transform to be Blocks, got %v", link.Transforms)
	}

	rc, err := content.Read(link, store, nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	defer rc.Close()

	readData, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !bytes.Equal(data, readData) {
		t.Errorf("Read data size %d does not match original size %d", len(readData), len(data))
	}
}

func TestReadWriteSuppliedKey(t *testing.T) {
	store := storage.NewInMemoryStorage()

	data := []byte("hello world with supplied key")

	suppliedKey := make([]byte, 32)
	if _, err := rand.Read(suppliedKey); err != nil {
		t.Fatal(err)
	}

	opts := content.WriterOptions{
		EncryptAlgorithm: "aes-256-cbc",
		KeyPolicy:        content.SuppliedAllKey,
		SuppliedKey:      suppliedKey,
	}

	link, err := content.Write(bytes.NewReader(data), store, opts)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if len(link.Transforms) == 0 || link.Transforms[0].Kind != "Decipher" {
		t.Errorf("Expected Decipher transform")
	}

	rc, err := content.Read(link, store, nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	defer rc.Close()

	readData, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !bytes.Equal(data, readData) {
		t.Errorf("Expected %q, got %q", data, readData)
	}
}
