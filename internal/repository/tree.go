package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"invariant/internal/content"
	"invariant/internal/filetree"
	"invariant/internal/storage"
)

// CreateEmptyTree creates an empty Directory file tree in CAS storage and returns its ContentLink.
func CreateEmptyTree(ctx context.Context, store storage.Storage) (content.ContentLink, error) {
	emptyDir := filetree.Directory{}
	data, err := json.Marshal(emptyDir)
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to marshal empty directory: %w", err)
	}
	return content.Write(bytes.NewReader(data), store, content.WriterOptions{})
}

// SnapshotDirectory walks a local directory on disk and stores its files into CAS,
// returning the root ContentLink representing the filetree.Directory.
func SnapshotDirectory(ctx context.Context, dirPath string, store storage.Storage) (content.ContentLink, error) {
	info, err := os.Stat(dirPath)
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to stat directory %s: %w", dirPath, err)
	}
	if !info.IsDir() {
		return content.ContentLink{}, fmt.Errorf("path %s is not a directory", dirPath)
	}

	var walkDir func(currPath string) (content.ContentLink, error)
	walkDir = func(currPath string) (content.ContentLink, error) {
		entries, err := os.ReadDir(currPath)
		if err != nil {
			return content.ContentLink{}, err
		}

		var dir filetree.Directory
		for _, e := range entries {
			name := e.Name()
			if name == ".git" || name == ".invariant-workspace" || strings.HasPrefix(name, ".ir-") {
				continue
			}

			fullPath := filepath.Join(currPath, name)
			if e.IsDir() {
				childLink, err := walkDir(fullPath)
				if err != nil {
					return content.ContentLink{}, err
				}
				dir = append(dir, &filetree.DirectoryEntry{
					BaseEntry: filetree.BaseEntry{
						Name: name,
						Kind: filetree.DirectoryKind,
					},
					Content: childLink,
				})
			} else {
				f, err := os.Open(fullPath)
				if err != nil {
					return content.ContentLink{}, err
				}
				fi, _ := f.Stat()
				sz := uint64(0)
				if fi != nil {
					sz = uint64(fi.Size())
				}

				fileLink, err := content.Write(f, store, content.WriterOptions{})
				f.Close()
				if err != nil {
					return content.ContentLink{}, fmt.Errorf("failed to store file %s: %w", fullPath, err)
				}

				dir = append(dir, &filetree.FileEntry{
					BaseEntry: filetree.BaseEntry{
						Name: name,
						Kind: filetree.FileKind,
					},
					Content: fileLink,
					Size:    sz,
				})
			}
		}

		dirData, err := json.Marshal(dir)
		if err != nil {
			return content.ContentLink{}, err
		}
		return content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	}

	return walkDir(dirPath)
}

// MaterializeTree extracts a CAS filetree.Directory snapshot to a local disk directory.
func MaterializeTree(ctx context.Context, tree content.ContentLink, destDir string, store storage.Storage) error {
	if tree.Address == "" {
		return os.MkdirAll(destDir, 0755)
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	var walkTree func(addr, currDest string) error
	walkTree = func(addr, currDest string) error {
		link := content.ContentLink{Address: addr}
		r, err := content.Read(link, store, nil)
		if err != nil {
			return err
		}
		defer r.Close()

		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}

		var dir filetree.Directory
		if err := dir.UnmarshalJSON(data); err != nil {
			return err
		}

		for _, e := range dir {
			name := e.GetName()
			targetPath := filepath.Join(currDest, name)

			if e.GetKind() == filetree.DirectoryKind {
				dirEntry, _ := e.(*filetree.DirectoryEntry)
				if err := os.MkdirAll(targetPath, 0755); err != nil {
					return err
				}
				if dirEntry.Content.Address != "" {
					if err := walkTree(dirEntry.Content.Address, targetPath); err != nil {
						return err
					}
				}
			} else if e.GetKind() == filetree.FileKind {
				fileEntry, _ := e.(*filetree.FileEntry)
				fLink := content.ContentLink{Address: fileEntry.Content.Address}
				fr, err := content.Read(fLink, store, nil)
				if err != nil {
					return err
				}
				outF, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
				if err != nil {
					fr.Close()
					return err
				}
				_, copyErr := io.Copy(outF, fr)
				fr.Close()
				outF.Close()
				if copyErr != nil {
					return copyErr
				}
			}
		}
		return nil
	}

	return walkTree(tree.Address, destDir)
}
