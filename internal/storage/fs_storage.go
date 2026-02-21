package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"invariant/internal/identity"
	"io"
	"os"
	"path/filepath"
)

// FileSystemStorage implements the Storage interface by saving blobs to disk.
type FileSystemStorage struct {
	baseDir string
	id      string
}

// Assert that FileSystemStorage implements the Storage interface
var _ Storage = (*FileSystemStorage)(nil)

// Assert that FileSystemStorage implements the identity.Provider interface
var _ identity.Provider = (*FileSystemStorage)(nil)

func NewFileSystemStorage(baseDir string) *FileSystemStorage {
	// Ensure the base directory exists
	os.MkdirAll(baseDir, 0755)

	idPath := filepath.Join(baseDir, "id")
	var id string
	if data, err := os.ReadFile(idPath); err == nil && len(data) == 64 {
		id = string(data)
	} else {
		idBytes := make([]byte, 32)
		rand.Read(idBytes)
		id = hex.EncodeToString(idBytes)
		os.WriteFile(idPath, []byte(id), 0644)
	}

	return &FileSystemStorage{
		baseDir: baseDir,
		id:      id,
	}
}

func (s *FileSystemStorage) ID() string {
	return s.id
}

// addressToPath converts an address (e.g., "aabbcc...") to a structured path
// like "aa/bb/cc...".
func (s *FileSystemStorage) addressToPath(address string) string {
	if len(address) < 4 {
		return filepath.Join(s.baseDir, address)
	}

	dir1 := address[0:2]
	dir2 := address[2:4]
	filename := address

	return filepath.Join(s.baseDir, dir1, dir2, filename)
}

func (s *FileSystemStorage) Has(address string) bool {
	path := s.addressToPath(address)
	_, err := os.Stat(path)
	return err == nil
}

func (s *FileSystemStorage) Get(address string) (io.ReadCloser, bool) {
	path := s.addressToPath(address)
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	return file, true
}

func (s *FileSystemStorage) Store(r io.Reader) (string, error) {
	// 1. Create a temporary file to read the stream and calculate the hash
	tmpFile, err := os.CreateTemp(s.baseDir, "upload-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile.Name()) // Clean up the temp file if something fails

	hasher := sha256.New()

	// TeeReader writes to the hasher while reading from the stream
	teeReader := io.TeeReader(r, hasher)

	// Copy the stream to the temp file
	if _, err := io.Copy(tmpFile, teeReader); err != nil {
		tmpFile.Close()
		return "", err
	}
	tmpFile.Close() // Close so we can move it

	// 2. Get the hash and the final address
	hashBytes := hasher.Sum(nil)
	address := hex.EncodeToString(hashBytes)

	// 3. Move the temporary file to its final destination
	finalPath := s.addressToPath(address)

	// Ensure the destination directories exist (e.g., dir/aa/bb/)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0755); err != nil {
		return "", err
	}

	// Attempt to rename the file. Note: if the file already exists, os.Rename overwrites it,
	// which is acceptable since the contents are identical (content-addressable).
	if err := os.Rename(tmpFile.Name(), finalPath); err != nil {
		// If os.Rename fails (e.g., across mount points), fallback to copy/delete isn't strictly
		// necessary here since temp file is created in the same baseDir.
		return "", err
	}

	return address, nil
}

func (s *FileSystemStorage) StoreAt(address string, r io.Reader) (bool, error) {
	// 1. Create a temporary file to read the stream and calculate the hash
	tmpFile, err := os.CreateTemp(s.baseDir, "upload-*")
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

	// 2. Verify the hash matches the requested address
	if address != calculatedAddress {
		return false, nil
	}

	// 3. Move the file
	finalPath := s.addressToPath(address)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0755); err != nil {
		return false, err
	}

	if err := os.Rename(tmpFile.Name(), finalPath); err != nil {
		return false, err
	}

	return true, nil
}

func (s *FileSystemStorage) Size(address string) (int64, bool) {
	path := s.addressToPath(address)
	stat, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return stat.Size(), true
}
