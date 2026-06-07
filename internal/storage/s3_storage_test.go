package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type mockS3Client struct {
	objects map[string][]byte
	mu      sync.Mutex
}

func (m *mockS3Client) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	val, ok := m.objects[*params.Key]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "key not found"}
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(bytes.NewReader(val)),
	}, nil
}

func (m *mockS3Client) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := io.ReadAll(params.Body)
	if err != nil {
		return nil, err
	}
	m.objects[*params.Key] = data
	return &s3.PutObjectOutput{}, nil
}

func (m *mockS3Client) HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	val, ok := m.objects[*params.Key]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NotFound", Message: "not found"}
	}
	return &s3.HeadObjectOutput{
		ContentLength: aws.Int64(int64(len(val))),
	}, nil
}

func (m *mockS3Client) DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, *params.Key)
	return &s3.DeleteObjectOutput{}, nil
}

func (m *mockS3Client) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var contents []types.Object
	for k := range m.objects {
		if params.Prefix == nil || strings.HasPrefix(k, *params.Prefix) {
			contents = append(contents, types.Object{
				Key: aws.String(k),
			})
		}
	}
	return &s3.ListObjectsV2Output{
		Contents: contents,
	}, nil
}

func TestMockS3Storage(t *testing.T) {
	ctx := context.Background()
	client := &mockS3Client{
		objects: make(map[string][]byte),
	}

	s3s, err := newS3StorageWithClient(ctx, "my-bucket", "test-prefix", client)
	if err != nil {
		t.Fatalf("Failed to initialize mock S3 storage: %v", err)
	}

	if id := s3s.ID(); len(id) != 64 {
		t.Errorf("Expected 64-character hex ID, got %q", id)
	}

	subCh := s3s.Subscribe(ctx)
	if subCh == nil {
		t.Fatal("Expected Subscribe channel to not be nil")
	}

	content := []byte("hello s3 storage mock")
	hash := sha256.Sum256(content)
	expectedAddress := hex.EncodeToString(hash[:])

	// test Store
	address, err := s3s.Store(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}
	if address != expectedAddress {
		t.Fatalf("expected address %s, got %s", expectedAddress, address)
	}

	// Verify notification received
	select {
	case notifiedAddr := <-subCh:
		if notifiedAddr != expectedAddress {
			t.Errorf("Expected notification for %s, got %s", expectedAddress, notifiedAddr)
		}
	default:
		t.Error("Expected subscription notification, got none")
	}

	// test Has
	if !s3s.Has(ctx, expectedAddress) {
		t.Fatal("Expected Has to return true")
	}

	// test Size
	size, ok := s3s.Size(ctx, expectedAddress)
	if !ok || size != int64(len(content)) {
		t.Fatalf("Expected size %d, got %d (ok: %t)", len(content), size, ok)
	}

	// test Get
	r, ok := s3s.Get(ctx, expectedAddress)
	if !ok {
		t.Fatal("Expected Get to return true")
	}
	defer r.Close()

	readContent, _ := io.ReadAll(r)
	if string(readContent) != string(content) {
		t.Fatalf("Expected content %s, got %s", content, string(readContent))
	}

	// test StoreAt
	newContent := []byte("another mock payload")
	hash2 := sha256.Sum256(newContent)
	newExpectedHash := hex.EncodeToString(hash2[:])

	// Incorrect store attempts
	success, err := s3s.StoreAt(ctx, newExpectedHash, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("StoreAt error: %v", err)
	}
	if success {
		t.Fatal("Expected StoreAt to fail inherently when hash doesn't match content")
	}

	// Correct store attempts
	success, err = s3s.StoreAt(ctx, newExpectedHash, bytes.NewReader(newContent))
	if err != nil {
		t.Fatalf("StoreAt error: %v", err)
	}
	if !success {
		t.Fatal("Expected StoreAt to succeed")
	}

	// test List
	var list []string
	for chunk := range s3s.List(ctx, 10) {
		list = append(list, chunk...)
	}

	found1 := false
	found2 := false
	for _, item := range list {
		if item == expectedAddress {
			found1 = true
		}
		if item == newExpectedHash {
			found2 = true
		}
	}
	if !found1 || !found2 {
		t.Fatalf("Expected List to contain both %s and %s, but got %v", expectedAddress, newExpectedHash, list)
	}

	// Note: since BatchStore calls StoreAt which validates hash, let's use actual hashes
	hContent1 := []byte("batch-s3-1")
	hHash1 := sha256.Sum256(hContent1)
	hAddr1 := hex.EncodeToString(hHash1[:])
	blocks2 := map[string]io.Reader{
		hAddr1: bytes.NewReader(hContent1),
	}
	err = s3s.BatchStore(ctx, blocks2)
	if err != nil {
		t.Fatalf("BatchStore failed: %v", err)
	}

	missing, err := s3s.BatchHas(ctx, []string{hAddr1, "b3"})
	if err != nil {
		t.Fatalf("BatchHas failed: %v", err)
	}
	if len(missing) != 1 || missing[0] != "b3" {
		t.Errorf("Expected missing to be ['b3'], got %v", missing)
	}

	// test Remove
	success, err = s3s.Remove(ctx, expectedAddress)
	if err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	if !success {
		t.Fatal("Expected Remove to return true")
	}

	if s3s.Has(ctx, expectedAddress) {
		t.Fatal("Expected Has to return false after removal")
	}
}

func TestNewS3Storage_Invalid(t *testing.T) {
	ctx := context.Background()
	_, _ = NewS3Storage(ctx, "", "")
}

func TestS3Storage(t *testing.T) {
	bucket := os.Getenv("TEST_S3_BUCKET")
	if bucket == "" {
		t.Skip("Skipping S3 storage test; set TEST_S3_BUCKET to run")
	}

	prefix := "test-invariant-s3/" + hex.EncodeToString([]byte(time.Now().String()))

	ctx := context.Background()
	s3s, err := NewS3Storage(ctx, bucket, prefix)
	if err != nil {
		t.Fatalf("Failed to initialize S3 storage: %v", err)
	}

	content := []byte("hello s3 storage test")
	hash1 := sha256.Sum256(content)
	expectedAddress := hex.EncodeToString(hash1[:])

	// test Store
	address, err := s3s.Store(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}
	if address != expectedAddress {
		t.Fatalf("expected address %s, got %s", expectedAddress, address)
	}

	// test Has
	if !s3s.Has(ctx, expectedAddress) {
		t.Fatal("Expected Has to return true")
	}

	// test Size
	size, ok := s3s.Size(ctx, expectedAddress)
	if !ok || size != int64(len(content)) {
		t.Fatalf("Expected size %d, got %d (ok: %t)", len(content), size, ok)
	}

	// test Get
	r, ok := s3s.Get(ctx, expectedAddress)
	if !ok {
		t.Fatal("Expected Get to return true")
	}
	defer r.Close()

	readContent, _ := io.ReadAll(r)
	if string(readContent) != string(content) {
		t.Fatalf("Expected content %s, got %s", content, string(readContent))
	}

	// test StoreAt
	newContent := []byte("another payload entirely for s3")
	hash2 := sha256.Sum256(newContent)
	newExpectedHash := hex.EncodeToString(hash2[:])

	// Incorrect store attempts
	success, err := s3s.StoreAt(ctx, newExpectedHash, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("StoreAt error: %v", err)
	}
	if success {
		t.Fatal("Expected StoreAt to fail inherently when hash doesn't match content")
	}

	// Correct store attempts
	success, err = s3s.StoreAt(ctx, newExpectedHash, bytes.NewReader(newContent))
	if err != nil {
		t.Fatalf("StoreAt error: %v", err)
	}
	if !success {
		t.Fatal("Expected StoreAt to succeed")
	}

	// test List
	var list []string
	for chunk := range s3s.List(ctx, 10) {
		list = append(list, chunk...)
	}

	found1 := false
	found2 := false
	for _, item := range list {
		if item == expectedAddress {
			found1 = true
		}
		if item == newExpectedHash {
			found2 = true
		}
	}
	if !found1 || !found2 {
		t.Fatalf("Expected List to contain both %s and %s, but got %v", expectedAddress, newExpectedHash, list)
	}

	// test Remove
	success, err = s3s.Remove(ctx, expectedAddress)
	if err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	if !success {
		t.Fatal("Expected Remove to return true")
	}

	if s3s.Has(ctx, expectedAddress) {
		t.Fatal("Expected Has to return false after removal")
	}

	s3s.Remove(ctx, newExpectedHash)
}
