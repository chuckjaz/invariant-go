package files

import (
	"context"
	"strings"
	"testing"
	"time"

	"invariant/internal/content"
	"invariant/internal/filetree"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestFilesService_LayeredRouting(t *testing.T) {
	store := storage.NewInMemoryStorage()
	slotsSvc := slots.NewMemorySlots("test-slots")

	ctx := context.Background()

	opts := Options{
		Storage:       store,
		Slots:         slotsSvc,
		RootLink:      content.ContentLink{Slot: true},
		WriterOptions: content.WriterOptions{},
		Layers: []Layer{
			{
				Includes: []string{}, // Match all unless excluded
				Excludes: []string{"secrets/", "*.tmp"},
				RootLink: content.ContentLink{Slot: true},
			},
			{
				Includes: []string{"secrets/"}, // Only match secrets
				Excludes: []string{},
				RootLink: content.ContentLink{Slot: true},
			},
		},
	}

	fs, err := NewInMemoryFiles(opts)
	if err != nil {
		t.Fatalf("Failed to initialize: %v", err)
	}

	// 1: Write public file
	err = fs.CreateEntry(ctx, 1, "hello.txt", filetree.FileKind, "", nil, strings.NewReader("public content"))
	if err != nil {
		t.Fatalf("Failed creating public file: %v", err)
	}

	// 2: Write temporary file (should go to neither layer or layer 0 depending on defaults if not strictly bounded, but ideally layer 0 without sync visibility or rejected. Actually our logic creates it in memory but doesn't map it to a layer if both exclude/not-include it)
	err = fs.CreateEntry(ctx, 1, "test.tmp", filetree.FileKind, "", nil, strings.NewReader("temp content"))
	if err != nil {
		t.Fatalf("Failed creating temp file: %v", err)
	}

	// 3: Create 'secrets' directory
	err = fs.CreateEntry(ctx, 1, "secrets", filetree.DirectoryKind, "", nil, nil)
	if err != nil {
		t.Fatalf("Failed creating secrets dir: %v", err)
	}

	// Lookup secrets to get ID
	info, err := fs.Lookup(ctx, 1, "secrets")
	if err != nil {
		t.Fatalf("Failed looking up secrets dir: %v", err)
	}

	// 4: Write protected secret inside the secrets directory
	err = fs.CreateEntry(ctx, info.Node, "key.pem", filetree.FileKind, "", nil, strings.NewReader("secret key data"))
	if err != nil {
		t.Fatalf("Failed creating secret key file: %v", err)
	}

	// Sync
	err = fs.Sync(ctx, 1, true)
	if err != nil {
		t.Fatalf("Failed to sync root: %v", err)
	}

	// Now check the internal nodes for LayerMembership to verify our routing worked.
	internalFs := fs
	internalFs.mu.RLock()
	defer internalFs.mu.RUnlock()

	verifyLayer := func(name string, expectedLayer1 bool, expectedLayer2 bool) {
		t.Helper()
		var found bool
		for _, node := range internalFs.nodes {
			if node.Name == name {
				found = true
				if node.LayerMembership[1] != expectedLayer1 {
					t.Errorf("Node %q Layer 1 membership mismatch. Expected: %v, Got: %v", name, expectedLayer1, node.LayerMembership[1])
				}
				if node.LayerMembership[2] != expectedLayer2 {
					t.Errorf("Node %q Layer 2 membership mismatch. Expected: %v, Got: %v", name, expectedLayer2, node.LayerMembership[2])
				}
			}
		}
		if !found {
			t.Errorf("Node %q not found in internal nodes", name)
		}
	}

	verifyLayer("hello.txt", true, false)
	verifyLayer("test.tmp", false, false)
	verifyLayer("secrets", false, true)
	verifyLayer("key.pem", false, true)

	// Ensure the parent is implicitly registered on layer 1 since the secret is inside it
	// Actually root (node ID 1) layer membership semantics aren't mapped via node.LayerMembership
	// typically, only children, but we enforce parent linkage internally so layers correctly link.
}

func TestFilesService_LayerDependencies(t *testing.T) {
	store := storage.NewInMemoryStorage()
	memSlots := slots.NewMemorySlots("test-slots")

	ctx := context.Background()

	rootLink := content.ContentLink{Slot: true}

	// Initialize with a default layer, then we'll add more via .invariant-layer
	fs, err := NewInMemoryFiles(Options{
		Storage:       store,
		Slots:         memSlots,
		RootLink:      rootLink,
		WriterOptions: content.WriterOptions{},
		Layers: []Layer{
			{RootLink: rootLink},
		},
	})
	if err != nil {
		t.Fatalf("Failed to initialize: %v", err)
	}

	// Create a dynamic ignore file
	ignoreContent := "ignored.txt\nsecrets/\n# comment\n"
	err = fs.CreateEntry(ctx, 1, ".myignore", filetree.FileKind, "", nil, strings.NewReader(ignoreContent))
	if err != nil {
		t.Fatalf("Failed creating .myignore: %v", err)
	}

	// Create .invariant-layer pointing to it
	layerConfig := `[{"Excludes": ["$.myignore", "static.txt"]}]`
	err = fs.CreateEntry(ctx, 1, ".invariant-layer", filetree.FileKind, "", nil, strings.NewReader(layerConfig))
	if err != nil {
		t.Fatalf("Failed creating .invariant-layer: %v", err)
	}

	// .invariant-layer mutation triggers handleLayerChange asynchronously
	// Give it a moment to run
	time.Sleep(100 * time.Millisecond)

	fs.mu.RLock()
	defer fs.mu.RUnlock()

	if len(fs.opts.Layers) < 2 { // root + 1 layer
		t.Fatalf("Expected at least 2 layers, got %d", len(fs.opts.Layers))
	}

	layer := fs.opts.Layers[1]
	if len(layer.Excludes) != 3 {
		t.Fatalf("Expected 3 excludes, got %v", layer.Excludes)
	}

	expectedExcludes := map[string]bool{"static.txt": true, "ignored.txt": true, "secrets/": true}
	for _, ex := range layer.Excludes {
		if !expectedExcludes[ex] {
			t.Errorf("Unexpected exclude: %s", ex)
		}
	}

	if !fs.layerDependencies[".myignore"] {
		t.Errorf("Expected .myignore to be in layerDependencies")
	}
}

func TestFilesService_GitignoreSemantics(t *testing.T) {
	store := storage.NewInMemoryStorage()
	slotsSvc := slots.NewMemorySlots("test-slots")

	ctx := context.Background()

	opts := Options{
		Storage:       store,
		Slots:         slotsSvc,
		RootLink:      content.ContentLink{Slot: true},
		WriterOptions: content.WriterOptions{},
		Layers: []Layer{
			{
				Includes: []string{},
				Excludes: []string{
					"*.log",
					"!important.log",
					"/root.txt",
					"docs/**/*.md",
					"tmp/",
				},
				RootLink: content.ContentLink{Slot: true},
			},
			{
				Includes: []string{}, // Match anything
				Excludes: []string{},
				RootLink: content.ContentLink{Slot: true},
			},
		},
	}

	fs, err := NewInMemoryFiles(opts)
	if err != nil {
		t.Fatalf("Failed to initialize: %v", err)
	}

	createFile := func(path string, isDir bool) {
		parts := strings.Split(path, "/")
		parentID := uint64(1)
		for i, part := range parts {
			if i == len(parts)-1 && !isDir {
				err = fs.CreateEntry(ctx, parentID, part, filetree.FileKind, "", nil, strings.NewReader("data"))
				if err != nil {
					t.Fatalf("Failed creating file %v: %v", path, err)
				}
			} else {
				// Directory
				_, err = fs.Lookup(ctx, parentID, part)
				if err != nil {
					err = fs.CreateEntry(ctx, parentID, part, filetree.DirectoryKind, "", nil, nil)
					if err != nil {
						t.Fatalf("Failed creating dir %v: %v", part, err)
					}
				}
				info, _ := fs.Lookup(ctx, parentID, part)
				parentID = info.Node
			}
		}
	}

	createFile("testlog.log", false)
	createFile("important.log", false)
	createFile("nested/testlog.log", false)
	createFile("nested/important.log", false)
	createFile("root.txt", false)
	createFile("nested/root.txt", false)
	createFile("docs/docs_readme.md", false)
	createFile("docs/deep/docs_nested_index.md", false)
	createFile("docs/deep/docs_nested_code.go", false)
	createFile("tmp", true)
	createFile("tmp/tmp_temp.txt", false) // Excluded by tmp/ -> layer 2

	err = fs.Sync(ctx, 1, true)
	if err != nil {
		t.Fatalf("Failed to sync root: %v", err)
	}

	fs.mu.RLock()
	defer fs.mu.RUnlock()

	getNodeID := func(path string) uint64 {
		parts := strings.Split(path, "/")
		curr := uint64(1)
		for _, part := range parts {
			var found bool
			for _, childID := range fs.nodes[curr].Children {
				if fs.nodes[childID].Name == part {
					curr = childID
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("Lookup failed for %v at %v", path, part)
			}
		}
		return curr
	}

	verifyLayer := func(path string, expectedLayer1 bool, expectedLayer2 bool) {
		t.Helper()
		id := getNodeID(path)
		node := fs.nodes[id]
		if node.LayerMembership[1] != expectedLayer1 {
			t.Errorf("Node %q expected layer1=%v got %v", path, expectedLayer1, node.LayerMembership[1])
		}
		if node.LayerMembership[2] != expectedLayer2 {
			t.Errorf("Node %q expected layer2=%v got %v", path, expectedLayer2, node.LayerMembership[2])
		}
	}

	verifyLayer("testlog.log", false, true)
	verifyLayer("important.log", true, false)
	verifyLayer("root.txt", false, true)
	verifyLayer("nested/root.txt", true, false)
	verifyLayer("docs/docs_readme.md", false, true)
	verifyLayer("docs/deep/docs_nested_index.md", false, true)
	verifyLayer("docs/deep/docs_nested_code.go", true, false)
	verifyLayer("tmp/tmp_temp.txt", false, true)
}
