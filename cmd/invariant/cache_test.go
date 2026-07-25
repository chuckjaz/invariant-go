package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"invariant/internal/files"
)

func TestRunCache_ValidMount(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "invariant-cache-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	os.MkdirAll(cacheDir, 0755)

	mountConfig := files.MountConfig{
		InvariantMount:  true,
		CacheDir:        cacheDir,
		IsWorkspace:     false,
		CacheSizeMB:     128,
		DiskCacheSizeMB: 1024,
	}

	data, err := json.MarshalIndent(mountConfig, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	mountFile := filepath.Join(tmpDir, ".invariant-mount.json")
	if err := os.WriteFile(mountFile, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	sampleFile := filepath.Join(tmpDir, "sample.txt")
	if err := os.WriteFile(sampleFile, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	runCache(nil, []string{tmpDir})
}

func TestRunCache_Subdirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "invariant-cache-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	os.MkdirAll(cacheDir, 0755)

	mountConfig := files.MountConfig{
		InvariantMount:  true,
		CacheDir:        cacheDir,
		IsWorkspace:     false,
		CacheSizeMB:     128,
		DiskCacheSizeMB: 1024,
	}

	data, err := json.MarshalIndent(mountConfig, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	mountFile := filepath.Join(tmpDir, ".invariant-mount.json")
	if err := os.WriteFile(mountFile, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(tmpDir, "A", "A1")
	os.MkdirAll(subDir, 0755)
	sampleFile := filepath.Join(subDir, "sub_sample.txt")
	if err := os.WriteFile(sampleFile, []byte("hello sub"), 0644); err != nil {
		t.Fatal(err)
	}

	otherDir := filepath.Join(tmpDir, "B")
	os.MkdirAll(otherDir, 0755)
	otherFile := filepath.Join(otherDir, "other_sample.txt")
	if err := os.WriteFile(otherFile, []byte("hello other"), 0644); err != nil {
		t.Fatal(err)
	}

	runCache(nil, []string{subDir})
}
