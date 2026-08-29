package commit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"invariant/internal/content"
	"invariant/internal/filetree"
	"invariant/internal/names"
	"invariant/internal/repository/identity"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

type mockIDProvider struct {
	id identity.Identity
}

func (m *mockIDProvider) CurrentIdentity(ctx context.Context) (identity.Identity, error) {
	return m.id, nil
}

func (m *mockIDProvider) IdentityFromRemote(ctx context.Context, remoteAddr string) (*identity.Identity, error) {
	return &m.id, nil
}

func allocateTestSlot(ctx context.Context, slotsClient slots.Slots, initialAddress string) (string, error) {
	b := make([]byte, 32)
	rand.Read(b)
	slotID := hex.EncodeToString(b)
	err := slotsClient.Create(ctx, slotID, initialAddress, "")
	return slotID, err
}

func createTestTree(ctx context.Context, store storage.Storage, files map[string]string) string {
	var dir filetree.Directory
	for name, contentStr := range files {
		cLink, err := content.Write(bytes.NewReader([]byte(contentStr)), store, content.WriterOptions{})
		if err != nil {
			panic(err)
		}
		dir = append(dir, &filetree.FileEntry{
			BaseEntry: filetree.BaseEntry{
				Name: name,
				Kind: filetree.FileKind,
			},
			Content: cLink,
			Size:    uint64(len(contentStr)),
		})
	}
	dirData, _ := json.Marshal(dir)
	dirLink, err := content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	if err != nil {
		panic(err)
	}
	return dirLink.Address
}

func setupTestEnvironment(ctx context.Context) (*LocalService, storage.Storage, slots.Slots, names.Names, string, string) {
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")
	namesClient := names.NewInMemoryNames()

	idProvider := &mockIDProvider{
		id: identity.Identity{
			Name:  "Alice",
			Email: "alice@example.com",
		},
	}

	repoName := "testrepo"
	initialTree := createTestTree(ctx, store, map[string]string{
		"README.md": "Hello Invariant Repo\nLine 2\n",
		"main.go":   "package main\n\nfunc main() {}\n",
	})

	svc := NewLocalService(store, slotsClient, namesClient, idProvider)

	// Create initial commit on main
	initCommit, initHash, err := svc.CreateCommit(ctx, CreateRequest{
		RepoName:   repoName,
		BranchName: "main",
		TreeLink:   content.ContentLink{Address: initialTree},
		Message:    "Initial commit",
		Author: identity.Identity{
			Name:  "Alice",
			Email: "alice@example.com",
		},
	})
	if err != nil {
		panic(err)
	}
	_ = initCommit

	// Allocate and register main slot
	mainSlotID, err := allocateTestSlot(ctx, slotsClient, initHash)
	if err != nil {
		panic(err)
	}
	if err := namesClient.Put(ctx, repoName, mainSlotID, nil); err != nil {
		panic(err)
	}

	return svc, store, slotsClient, namesClient, repoName, initHash
}

func TestLocalService_CommitAndHistory(t *testing.T) {
	ctx := context.Background()
	svc, store, slotsClient, namesClient, repoName, initHash := setupTestEnvironment(ctx)

	// Create a change branch for Alice
	tree2 := createTestTree(ctx, store, map[string]string{
		"README.md": "Hello Invariant Repo\nLine 2 modified\nLine 3 added\n",
		"main.go":   "package main\n\nfunc main() {}\n",
		"util.go":   "package main\n",
	})

	changeSlotID, err := allocateTestSlot(ctx, slotsClient, initHash)
	if err != nil {
		t.Fatalf("AllocateSlot failed: %v", err)
	}
	changeKey := formatChangeBranch("Alice", repoName, "feat-x")
	if err := namesClient.Put(ctx, changeKey, changeSlotID, nil); err != nil {
		t.Fatalf("RegisterChangeBranch failed: %v", err)
	}

	c2, h2, err := svc.CreateCommit(ctx, CreateRequest{
		RepoName:   repoName,
		BranchName: "feat-x",
		TreeLink:   content.ContentLink{Address: tree2},
		Parents:    []string{initHash},
		Message:    "Feature X implementation",
		Author: identity.Identity{
			Name:  "Alice",
			Email: "alice@example.com",
		},
	})
	if err != nil {
		t.Fatalf("CreateCommit failed: %v", err)
	}
	if c2.Message != "Feature X implementation" {
		t.Errorf("Commit message mismatch: got %q", c2.Message)
	}

	// Verify history
	commits, hashes, err := svc.GetHistory(ctx, h2, true, "")
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("Expected 2 commits in history, got %d", len(commits))
	}
	if hashes[0] != h2 || hashes[1] != initHash {
		t.Errorf("Unexpected history hashes: %+v", hashes)
	}

	// Verify path-filtered history
	utilCommits, _, err := svc.GetHistory(ctx, h2, true, "util.go")
	if err != nil {
		t.Fatalf("GetHistory with path filter failed: %v", err)
	}
	if len(utilCommits) != 1 || utilCommits[0].Message != "Feature X implementation" {
		t.Fatalf("Expected 1 commit for util.go, got %d", len(utilCommits))
	}
}

func TestLocalService_Diff(t *testing.T) {
	ctx := context.Background()
	svc, store, _, _, _, _ := setupTestEnvironment(ctx)

	t1 := createTestTree(ctx, store, map[string]string{
		"file.txt": "alpha\nbeta\n",
	})
	t2 := createTestTree(ctx, store, map[string]string{
		"file.txt": "alpha\ngamma\ndelta\n",
	})

	diff, stat, err := svc.ComputeDiff(ctx, content.ContentLink{Address: t1}, content.ContentLink{Address: t2})
	if err != nil {
		t.Fatalf("ComputeDiff failed: %v", err)
	}
	if stat.FilesChanged != 1 {
		t.Errorf("Expected 1 file changed, got %d", stat.FilesChanged)
	}
	if stat.Insertions != 2 || stat.Deletions != 1 {
		t.Errorf("Expected +2 -1, got +%d -%d", stat.Insertions, stat.Deletions)
	}
	if len(diff) == 0 {
		t.Errorf("Expected non-empty diff string")
	}
}

func TestLocalService_SyncAndSubmit(t *testing.T) {
	ctx := context.Background()
	svc, store, slotsClient, namesClient, repoName, initHash := setupTestEnvironment(ctx)

	// Upstream commits a new file on main
	treeMain2 := createTestTree(ctx, store, map[string]string{
		"README.md": "Hello Invariant Repo\nLine 2\n",
		"main.go":   "package main\n\nfunc main() {}\n",
		"docs.txt":  "Documentation\n",
	})
	_, main2Hash, err := svc.CreateCommit(ctx, CreateRequest{
		RepoName:   repoName,
		BranchName: "main",
		TreeLink:   content.ContentLink{Address: treeMain2},
		Parents:    []string{initHash},
		Message:    "Add docs on main",
		Author: identity.Identity{
			Name: "Bob",
		},
	})
	if err != nil {
		t.Fatalf("CreateCommit on main failed: %v", err)
	}
	mainEntry, _ := namesClient.Get(ctx, repoName)
	slotsClient.Update(ctx, mainEntry.Value, main2Hash, initHash, nil)

	// Alice creates feature commit on branch feat-sync branched from initHash
	treeAlice := createTestTree(ctx, store, map[string]string{
		"README.md": "Hello Invariant Repo\nLine 2\n",
		"main.go":   "package main\n\nfunc main() { fmt.Println() }\n",
	})
	changeSlotID, _ := allocateTestSlot(ctx, slotsClient, initHash)
	changeKey := formatChangeBranch("Alice", repoName, "feat-sync")
	namesClient.Put(ctx, changeKey, changeSlotID, nil)

	_, _, err = svc.CreateCommit(ctx, CreateRequest{
		RepoName:   repoName,
		BranchName: "feat-sync",
		TreeLink:   content.ContentLink{Address: treeAlice},
		Parents:    []string{initHash},
		Message:    "Update main.go",
		Author: identity.Identity{
			Name: "Alice",
		},
	})
	if err != nil {
		t.Fatalf("CreateCommit for Alice failed: %v", err)
	}

	// 1. Sync branch
	rebasedHash, conflicts, err := svc.SyncBranch(ctx, repoName, "feat-sync")
	if err != nil {
		t.Fatalf("SyncBranch failed: %v", err)
	}
	if len(conflicts) > 0 {
		t.Fatalf("Expected clean sync, got conflicts: %+v", conflicts)
	}
	if rebasedHash == "" {
		t.Fatalf("Expected non-empty rebased commit hash")
	}

	// 2. Submit change
	resp, err := svc.SubmitChange(ctx, SubmitRequest{
		RepoName:     repoName,
		ChangeBranch: "feat-sync",
		TargetBranch: "main",
		Author: identity.Identity{
			Name: "Alice",
		},
	})
	if err != nil {
		t.Fatalf("SubmitChange failed: %v", err)
	}
	if resp.NewHeadCommit != rebasedHash {
		t.Errorf("Submit head commit mismatch: got %s, want %s", resp.NewHeadCommit, rebasedHash)
	}
}

func TestLocalService_BisectAndBlame(t *testing.T) {
	ctx := context.Background()
	svc, store, _, _, _, initHash := setupTestEnvironment(ctx)

	// Build a 4-commit history
	var hashes []string
	hashes = append(hashes, initHash)

	prevHash := initHash
	for i := 1; i <= 3; i++ {
		tree := createTestTree(ctx, store, map[string]string{
			"code.go": "line 1\nline 2\n",
		})
		_, h, err := svc.CreateCommit(ctx, CreateRequest{
			TreeLink: content.ContentLink{Address: tree},
			Parents:  []string{prevHash},
			Message:  time.Now().String(),
			Author: identity.Identity{
				Name: "Dev",
			},
		})
		if err != nil {
			t.Fatalf("CreateCommit failed: %v", err)
		}
		hashes = append(hashes, h)
		prevHash = h
	}

	// Test Bisect
	candidate, rem, err := svc.Bisect(ctx, []string{hashes[0]}, []string{hashes[3]})
	if err != nil {
		t.Fatalf("Bisect failed: %v", err)
	}
	if candidate == "" {
		t.Fatalf("Expected valid candidate commit, got empty")
	}
	_ = rem

	// Test Blame
	blameLines, err := svc.Blame(ctx, hashes[3], "code.go")
	if err != nil {
		t.Fatalf("Blame failed: %v", err)
	}
	if len(blameLines) != 2 {
		t.Errorf("Expected 2 blame lines, got %d", len(blameLines))
	}
	if blameLines[0].Content != "line 1" {
		t.Errorf("Blame line 1 content mismatch: got %q", blameLines[0].Content)
	}
}

func TestHTTPServerAndClient(t *testing.T) {
	ctx := context.Background()
	localSvc, _, _, _, repoName, initHash := setupTestEnvironment(ctx)

	server := NewServer(localSvc)
	ts := httptest.NewServer(server)
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())

	// Test GetCommit
	c, err := client.GetCommit(ctx, initHash)
	if err != nil {
		t.Fatalf("client.GetCommit failed: %v", err)
	}
	if c.Message != "Initial commit" {
		t.Errorf("Expected 'Initial commit', got %q", c.Message)
	}

	// Test History
	commits, hashes, err := client.GetHistory(ctx, initHash, true, "")
	if err != nil {
		t.Fatalf("client.GetHistory failed: %v", err)
	}
	if len(commits) != 1 || hashes[0] != initHash {
		t.Errorf("History mismatch: len=%d", len(commits))
	}

	// Test CreateCommit via HTTP client
	c2, h2, err := client.CreateCommit(ctx, CreateRequest{
		RepoName:   repoName,
		BranchName: "main",
		TreeLink:   c.Tree,
		Parents:    []string{initHash},
		Message:    "Remote HTTP commit",
		Author: identity.Identity{
			Name: "HTTP Client",
		},
	})
	if err != nil {
		t.Fatalf("client.CreateCommit failed: %v", err)
	}
	if h2 == "" || c2.Message != "Remote HTTP commit" {
		t.Errorf("Unexpected created commit: %s, msg=%q", h2, c2.Message)
	}
}
