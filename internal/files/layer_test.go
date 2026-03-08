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

	verifyLayer := func(name string, expectedLayer0 bool, expectedLayer1 bool) {
		t.Helper()
		var found bool
		for _, node := range internalFs.nodes {
			if node.Name == name {
				found = true
				if node.LayerMembership[0] != expectedLayer0 {
					t.Errorf("Node %q Layer 0 membership mismatch. Expected: %v, Got: %v", name, expectedLayer0, node.LayerMembership[0])
				}
				if node.LayerMembership[1] != expectedLayer1 {
					t.Errorf("Node %q Layer 1 membership mismatch. Expected: %v, Got: %v", name, expectedLayer1, node.LayerMembership[1])
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

	if len(fs.opts.Layers) < 2 { // 0 + root layer
		t.Fatalf("Expected at least 2 layers, got %d", len(fs.opts.Layers))
	}

	layer := fs.opts.Layers[0]
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
