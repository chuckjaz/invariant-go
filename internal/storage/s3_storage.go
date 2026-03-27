package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"invariant/internal/identity"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Storage implements the Storage interface by saving blobs to AWS S3.
type S3Storage struct {
	client      *s3.Client
	bucket      string
	prefix      string
	id          string
	mu          sync.RWMutex
	subscribers []chan string
}

// Assert that S3Storage implements the ControlledStorage interface
var _ ControlledStorage = (*S3Storage)(nil)
var _ Storage = (*S3Storage)(nil)

// Assert that S3Storage implements the identity.Provider interface
var _ identity.Identity = (*S3Storage)(nil)

// NewS3Storage creates a new S3Storage instance.
func NewS3Storage(ctx context.Context, bucket string, prefix string) (*S3Storage, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	client := s3.NewFromConfig(cfg)

	// Fetch or create ID
	idKey := "id"
	if prefix != "" {
		idKey = filepath.Join(prefix, "id")
	}

	// normalize prefix to always end with a slash if not empty
	var normalizedPrefix string
	if prefix != "" {
		normalizedPrefix = strings.TrimSuffix(prefix, "/") + "/"
	}

	var id string
	resp, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(idKey),
	})
	if err == nil {
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if len(data) == 64 {
			id = string(data)
		}
	}

	if id == "" {
		idBytes := make([]byte, 32)
		rand.Read(idBytes)
		id = hex.EncodeToString(idBytes)
		_, err = client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(idKey),
			Body:   bytes.NewReader([]byte(id)),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to save ID to S3: %w", err)
		}
	}

	return &S3Storage{
		client: client,
		bucket: bucket,
		prefix: normalizedPrefix,
		id:     id,
	}, nil
}

func (s *S3Storage) ID() string {
	return s.id
}

func (s *S3Storage) addressToKey(address string) string {
	if len(address) < 4 {
		return s.prefix + address
	}
	dir1 := address[0:2]
	dir2 := address[2:4]
	filename := address[4:]

	return s.prefix + dir1 + "/" + dir2 + "/" + filename
}

func (s *S3Storage) Has(ctx context.Context, address string) bool {
	key := s.addressToKey(address)
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err == nil
}

func (s *S3Storage) Get(ctx context.Context, address string) (io.ReadCloser, bool) {
	key := s.addressToKey(address)
	resp, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, false
	}
	return resp.Body, true
}

func (s *S3Storage) Store(ctx context.Context, r io.Reader) (string, error) {
	tmpFile, err := os.CreateTemp("", "s3-upload-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile.Name())

	hasher := sha256.New()
	teeReader := io.TeeReader(r, hasher)
	if _, err := io.Copy(tmpFile, teeReader); err != nil {
		tmpFile.Close()
		return "", err
	}
	tmpFile.Close()

	hashBytes := hasher.Sum(nil)
	address := hex.EncodeToString(hashBytes)
	key := s.addressToKey(address)

	f, err := os.Open(tmpFile.Name())
	if err != nil {
		return "", err
	}
	defer f.Close()

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   f,
	})
	if err != nil {
		return "", err
	}

	s.notifySubscribers(address)
	return address, nil
}

func (s *S3Storage) StoreAt(ctx context.Context, address string, r io.Reader) (bool, error) {
	tmpFile, err := os.CreateTemp("", "s3-upload-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmpFile.Name())

	hasher := sha256.New()
	teeReader := io.TeeReader(r, hasher)
	if _, err := io.Copy(tmpFile, teeReader); err != nil {
		tmpFile.Close()
		return false, err
	}
	tmpFile.Close()

	hashBytes := hasher.Sum(nil)
	calculatedAddress := hex.EncodeToString(hashBytes)

	if address != calculatedAddress {
		return false, nil
	}

	key := s.addressToKey(address)
	f, err := os.Open(tmpFile.Name())
	if err != nil {
		return false, err
	}
	defer f.Close()

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   f,
	})
	if err != nil {
		return false, err
	}

	s.notifySubscribers(address)
	return true, nil
}

func (s *S3Storage) Size(ctx context.Context, address string) (int64, bool) {
	key := s.addressToKey(address)
	resp, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, false
	}
	if resp.ContentLength != nil {
		return *resp.ContentLength, true
	}
	return 0, true
}

func (s *S3Storage) List(ctx context.Context, chunkSize int) <-chan []string {
	if chunkSize <= 0 {
		chunkSize = 1000
	}
	ch := make(chan []string)

	go func() {
		defer close(ch)

		var prefix *string
		if s.prefix != "" {
			prefix = aws.String(s.prefix)
		}

		paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
			Bucket: aws.String(s.bucket),
			Prefix: prefix,
		})

		var chunk []string
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if err != nil {
				break
			}
			for _, obj := range page.Contents {
				key := aws.ToString(obj.Key)
				// remove prefix
				if s.prefix != "" && strings.HasPrefix(key, s.prefix) {
					key = key[len(s.prefix):]
				}
				// Skip the id object
				if key == "id" {
					continue
				}
				// Skip empty keys
				if key == "" {
					continue
				}
				// Remove '/' to reconstruct the address
				address := strings.ReplaceAll(key, "/", "")
				chunk = append(chunk, address)
				if len(chunk) >= chunkSize {
					ch <- chunk
					chunk = nil
				}
			}
		}
		if len(chunk) > 0 {
			ch <- chunk
		}
	}()

	return ch
}

func (s *S3Storage) Subscribe(ctx context.Context) <-chan string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan string, 100)
	s.subscribers = append(s.subscribers, ch)
	return ch
}

func (s *S3Storage) notifySubscribers(address string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ch := range s.subscribers {
		select {
		case ch <- address:
		default:
		}
	}
}

func (s *S3Storage) Remove(ctx context.Context, address string) (bool, error) {
	key := s.addressToKey(address)
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return false, err
	}
	return true, nil
}
