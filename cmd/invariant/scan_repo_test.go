package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"invariant/internal/kv"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestLocalScan(t *testing.T) {
	// 1. Create a temporary local git repository
	repoDir := t.TempDir()
	r, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("Failed to initialize git repository: %v", err)
	}

	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("Failed to get worktree: %v", err)
	}

	// Commit 1: Add hello.txt
	helloPath := filepath.Join(repoDir, "hello.txt")
	helloContent := []byte("hello world\n")
	if err := os.WriteFile(helloPath, helloContent, 0644); err != nil {
		t.Fatalf("Failed to write hello.txt: %v", err)
	}

	_, err = wt.Add("hello.txt")
	if err != nil {
		t.Fatalf("Failed to add hello.txt: %v", err)
	}

	_, err = wt.Commit("First commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit first: %v", err)
	}

	// Commit 2: Modify hello.txt and add subdir/nested.txt
	if err := os.WriteFile(helloPath, []byte("hello world updated\n"), 0644); err != nil {
		t.Fatalf("Failed to update hello.txt: %v", err)
	}
	_, err = wt.Add("hello.txt")
	if err != nil {
		t.Fatalf("Failed to add updated hello.txt: %v", err)
	}

	nestedDir := filepath.Join(repoDir, "subdir")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	nestedPath := filepath.Join(nestedDir, "nested.txt")
	nestedContent := []byte("nested file content\n")
	if err := os.WriteFile(nestedPath, nestedContent, 0644); err != nil {
		t.Fatalf("Failed to write nested.txt: %v", err)
	}
	_, err = wt.Add("subdir/nested.txt")
	if err != nil {
		t.Fatalf("Failed to add nested.txt: %v", err)
	}

	commit2Hash, err := wt.Commit("Second commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit second: %v", err)
	}

	// 2. Set up mock KV server
	var mu sync.Mutex
	kvStore := make(map[string][]byte)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if req.URL.Path == "/batch_get" {
			var keys []string
			if err := json.NewDecoder(req.Body).Decode(&keys); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			mw := multipart.NewWriter(w)
			w.Header().Set("Content-Type", mw.FormDataContentType())

			for _, k := range keys {
				val, ok := kvStore[k]
				if ok {
					h := make(textproto.MIMEHeader)
					h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, k))
					h.Set("X-Transaction-ID", "1")
					part, err := mw.CreatePart(h)
					if err != nil {
						continue
					}
					part.Write(val)
				}
			}
			mw.Close()
			return
		}

		if req.URL.Path == "/batch_put" {
			_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			boundary := params["boundary"]
			mr := multipart.NewReader(req.Body, boundary)

			for {
				part, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				key := part.FormName()
				if key == "" {
					continue
				}
				val, err := io.ReadAll(part)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				kvStore[key] = val
			}

			w.Header().Set("X-Transaction-ID", "1")
			w.WriteHeader(http.StatusOK)
			return
		}

		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer ts.Close()

	// 3. Perform scan using runLocalScan
	kvClient := kv.NewClient(ts.URL, nil)

	// Scan starting from commit2 (depth 1 -> only files in commit2 tree)
	runLocalScan(context.Background(), kvClient, repoDir, commit2Hash.String(), 1, 2)

	// Verify mappings in kvStore
	mu.Lock()
	defer mu.Unlock()

	// Let's query repo directly to find the exact SHA1 hashes of our files in commit2
	commit2Obj, err := r.CommitObject(commit2Hash)
	if err != nil {
		t.Fatalf("Failed to fetch commit2 object: %v", err)
	}
	tree2, err := commit2Obj.Tree()
	if err != nil {
		t.Fatalf("Failed to fetch tree: %v", err)
	}

	helloFile, err := tree2.File("hello.txt")
	if err != nil {
		t.Fatalf("Failed to find hello.txt: %v", err)
	}
	helloSHA1 := helloFile.Hash.String()

	nestedFile, err := tree2.File("subdir/nested.txt")
	if err != nil {
		t.Fatalf("Failed to find nested.txt: %v", err)
	}
	nestedSHA1 := nestedFile.Hash.String()

	// Expected SHA256 of raw contents
	sha256Hex := func(content []byte) string {
		h := sha256.Sum256(content)
		return hex.EncodeToString(h[:])
	}
	expectedHelloSHA256 := sha256Hex([]byte("hello world updated\n"))
	expectedNestedSHA256 := sha256Hex([]byte("nested file content\n"))

	// Check mappings
	hKey1 := "SHA1:" + helloSHA1
	hKey2 := "SHA256:" + expectedHelloSHA256
	if string(kvStore[hKey1]) != expectedHelloSHA256 {
		t.Errorf("Expected hello key %s to map to %s, got %s", hKey1, expectedHelloSHA256, string(kvStore[hKey1]))
	}
	if string(kvStore[hKey2]) != helloSHA1 {
		t.Errorf("Expected hello key %s to map to %s, got %s", hKey2, helloSHA1, string(kvStore[hKey2]))
	}

	nKey1 := "SHA1:" + nestedSHA1
	nKey2 := "SHA256:" + expectedNestedSHA256
	if string(kvStore[nKey1]) != expectedNestedSHA256 {
		t.Errorf("Expected nested key %s to map to %s, got %s", nKey1, expectedNestedSHA256, string(kvStore[nKey1]))
	}
	if string(kvStore[nKey2]) != nestedSHA1 {
		t.Errorf("Expected nested key %s to map to %s, got %s", nKey2, nestedSHA1, string(kvStore[nKey2]))
	}
}

func TestLocalScanWithDepth(t *testing.T) {
	// Create temporary repo
	repoDir := t.TempDir()
	r, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("Failed to initialize git repository: %v", err)
	}

	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("Failed to get worktree: %v", err)
	}

	// Commit 1: hello.txt = "hello world\n"
	helloPath := filepath.Join(repoDir, "hello.txt")
	if err := os.WriteFile(helloPath, []byte("hello world\n"), 0644); err != nil {
		t.Fatalf("Failed to write hello.txt: %v", err)
	}
	_, err = wt.Add("hello.txt")
	if err != nil {
		t.Fatalf("Failed to add hello.txt: %v", err)
	}
	commit1Hash, err := wt.Commit("Commit 1", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit 1: %v", err)
	}

	// Commit 2: hello.txt = "hello world updated\n"
	if err := os.WriteFile(helloPath, []byte("hello world updated\n"), 0644); err != nil {
		t.Fatalf("Failed to update hello.txt: %v", err)
	}
	_, err = wt.Add("hello.txt")
	if err != nil {
		t.Fatalf("Failed to add updated hello.txt: %v", err)
	}
	commit2Hash, err := wt.Commit("Commit 2", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit 2: %v", err)
	}

	// Resolve the blob hashes
	commit1Obj, _ := r.CommitObject(commit1Hash)
	tree1, _ := commit1Obj.Tree()
	hello1File, _ := tree1.File("hello.txt")
	hello1SHA1 := hello1File.Hash.String()

	commit2Obj, _ := r.CommitObject(commit2Hash)
	tree2, _ := commit2Obj.Tree()
	hello2File, _ := tree2.File("hello.txt")
	hello2SHA1 := hello2File.Hash.String()

	// Expected SHA256s
	sha256Hex := func(content []byte) string {
		h := sha256.Sum256(content)
		return hex.EncodeToString(h[:])
	}
	hello1SHA256 := sha256Hex([]byte("hello world\n"))
	hello2SHA256 := sha256Hex([]byte("hello world updated\n"))

	// Set up mock KV
	var mu sync.Mutex
	kvStore := make(map[string][]byte)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if req.URL.Path == "/batch_get" {
			mw := multipart.NewWriter(w)
			w.Header().Set("Content-Type", mw.FormDataContentType())
			mw.Close()
			return
		}
		if req.URL.Path == "/batch_put" {
			_, params, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
			mr := multipart.NewReader(req.Body, params["boundary"])
			for {
				part, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				key := part.FormName()
				val, _ := io.ReadAll(part)
				kvStore[key] = val
			}
			w.Header().Set("X-Transaction-ID", "1")
			w.WriteHeader(http.StatusOK)
			return
		}
	}))
	defer ts.Close()

	kvClient := kv.NewClient(ts.URL, nil)

	// Scan starting from commit2 (depth 2 -> should traverse parent commit1 as well)
	runLocalScan(context.Background(), kvClient, repoDir, commit2Hash.String(), 2, 2)

	mu.Lock()
	defer mu.Unlock()

	// Verify both commit1 and commit2 hello.txt blobs are indexed
	if string(kvStore["SHA1:"+hello1SHA1]) != hello1SHA256 {
		t.Errorf("Expected hello.txt from Commit 1 to be indexed")
	}
	if string(kvStore["SHA1:"+hello2SHA1]) != hello2SHA256 {
		t.Errorf("Expected hello.txt from Commit 2 to be indexed")
	}
}

func TestLocalScanTreeScannedAndSkipped(t *testing.T) {
	// 1. Create a temporary local git repository
	repoDir := t.TempDir()
	r, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("Failed to initialize git repository: %v", err)
	}

	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("Failed to get worktree: %v", err)
	}

	// Create hello.txt
	helloPath := filepath.Join(repoDir, "hello.txt")
	if err := os.WriteFile(helloPath, []byte("hello world\n"), 0644); err != nil {
		t.Fatalf("Failed to write hello.txt: %v", err)
	}
	if _, err = wt.Add("hello.txt"); err != nil {
		t.Fatalf("Failed to add hello.txt: %v", err)
	}

	// Create subdir/nested.txt
	nestedDir := filepath.Join(repoDir, "subdir")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}
	nestedPath := filepath.Join(nestedDir, "nested.txt")
	if err := os.WriteFile(nestedPath, []byte("nested file content\n"), 0644); err != nil {
		t.Fatalf("Failed to write nested.txt: %v", err)
	}
	if _, err = wt.Add("subdir/nested.txt"); err != nil {
		t.Fatalf("Failed to add nested.txt: %v", err)
	}

	commitHash, err := wt.Commit("Commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	commitObj, _ := r.CommitObject(commitHash)
	treeObj, _ := commitObj.Tree()
	rootTreeSHA := treeObj.Hash.String()

	entry, err := treeObj.FindEntry("subdir")
	if err != nil {
		t.Fatalf("Failed to find entry subdir: %v", err)
	}
	subdirSHA := entry.Hash.String()

	nestedFile, err := treeObj.File("subdir/nested.txt")
	if err != nil {
		t.Fatalf("Failed to find nested.txt: %v", err)
	}
	nestedSHA1 := nestedFile.Hash.String()

	helloFile, err := treeObj.File("hello.txt")
	if err != nil {
		t.Fatalf("Failed to find hello.txt: %v", err)
	}
	helloSHA1 := helloFile.Hash.String()

	// Helper to set up httptest server with a given store
	setupServer := func(store map[string][]byte) *httptest.Server {
		var mu sync.Mutex
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			mu.Lock()
			defer mu.Unlock()

			if req.URL.Path == "/batch_get" {
				var keys []string
				json.NewDecoder(req.Body).Decode(&keys)

				mw := multipart.NewWriter(w)
				w.Header().Set("Content-Type", mw.FormDataContentType())

				for _, k := range keys {
					val, ok := store[k]
					if ok {
						h := make(textproto.MIMEHeader)
						h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, k))
						part, err := mw.CreatePart(h)
						if err != nil {
							continue
						}
						part.Write(val)
					}
				}
				mw.Close()
				return
			}

			if req.URL.Path == "/batch_put" {
				_, params, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
				mr := multipart.NewReader(req.Body, params["boundary"])

				for {
					part, err := mr.NextPart()
					if err == io.EOF {
						break
					}
					key := part.FormName()
					val, _ := io.ReadAll(part)
					store[key] = val
				}

				w.Header().Set("X-Transaction-ID", "1")
				w.WriteHeader(http.StatusOK)
				return
			}
		}))
	}

	t.Run("records tree status keys in KV", func(t *testing.T) {
		store := make(map[string][]byte)
		ts := setupServer(store)
		defer ts.Close()

		kvClient := kv.NewClient(ts.URL, nil)
		var output string
		output = captureStdout(func() {
			runLocalScan(context.Background(), kvClient, repoDir, commitHash.String(), 1, 2)
		})

		if !strings.Contains(output, "Detected 0 tree(s) that were already scanned.") {
			t.Errorf("Expected output to report 0 already scanned trees, got:\n%s", output)
		}

		// Verify hello.txt and nested.txt are indexed
		if _, ok := store["SHA1:"+helloSHA1]; !ok {
			t.Errorf("hello.txt was not indexed")
		}
		if _, ok := store["SHA1:"+nestedSHA1]; !ok {
			t.Errorf("nested.txt was not indexed")
		}

		// Verify tree keys are recorded as scanned
		if val, ok := store["tree:sha1:"+rootTreeSHA]; !ok || string(val) != "scanned" {
			t.Errorf("Root tree was not marked as scanned")
		}
		if val, ok := store["tree:sha1:"+subdirSHA]; !ok || string(val) != "scanned" {
			t.Errorf("Subdir tree was not marked as scanned")
		}
	})

	t.Run("skips already scanned directory", func(t *testing.T) {
		store := make(map[string][]byte)
		// Pre-mark subdir as scanned
		store["tree:sha1:"+subdirSHA] = []byte("scanned")
		ts := setupServer(store)
		defer ts.Close()

		kvClient := kv.NewClient(ts.URL, nil)
		var output string
		output = captureStdout(func() {
			runLocalScan(context.Background(), kvClient, repoDir, commitHash.String(), 1, 2)
		})

		if !strings.Contains(output, "Detected 1 tree(s) that were already scanned.") {
			t.Errorf("Expected output to report 1 already scanned tree, got:\n%s", output)
		}

		// Verify hello.txt IS scanned
		if _, ok := store["SHA1:"+helloSHA1]; !ok {
			t.Errorf("hello.txt should have been scanned")
		}

		// Verify nested.txt was SKIPPED (not scanned/indexed)
		if _, ok := store["SHA1:"+nestedSHA1]; ok {
			t.Errorf("nested.txt should have been skipped because subdir was already scanned")
		}

		// Root tree should still be marked as scanned after this scan
		if val, ok := store["tree:sha1:"+rootTreeSHA]; !ok || string(val) != "scanned" {
			t.Errorf("Root tree should be marked as scanned")
		}
	})
}

func captureStdout(f func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	outChan := make(chan string)
	go func() {
		var buf strings.Builder
		io.Copy(&buf, r)
		outChan <- buf.String()
	}()

	f()
	w.Close()
	os.Stdout = old
	return <-outChan
}
