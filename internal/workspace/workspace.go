package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/files"
	"invariant/internal/filetree"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// WorkspaceInfo represents the contents of the .invariant-workspace JSON file.
type WorkspaceInfo struct {
	Content        content.ContentLink `json:"content"`
	ParentSnapshot string              `json:"parent_snapshot,omitempty"`
	ParentSlot     string              `json:"parent_slot,omitempty"`
}

func CreateWorkspace(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	discoveryClient discovery.Discovery,
	baseContentLink content.ContentLink,
	additionalLayers []string,
) (content.ContentLink, error) {

	// 1. Read the base file tree to look for .invariant-share and ignore files.
	// Since we need to read it as a file tree, we can just instantiate an InMemoryFiles.
	opts := files.Options{
		Storage:  store,
		Slots:    slotsClient,
		RootLink: baseContentLink,
	}
	fs, err := files.NewInMemoryFiles(opts)
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to initialize files from base link: %w", err)
	}
	defer fs.Close()

	var layers []files.Layer

	// a. check if .invariant-share exists
	shareInfo, err := fs.Lookup(ctx, 1, ".invariant-share")
	if err == nil && shareInfo.Kind == string(filetree.FileKind) {
		r, err := fs.ReadFile(ctx, shareInfo.Node, 0, 0)
		if err == nil {
			defer r.Close()
			var shareLayers []files.Layer
			if err := json.NewDecoder(r).Decode(&shareLayers); err == nil {
				layers = append(layers, shareLayers...)
			}
		}
	}

	// b. add additional `-layers`
	for _, layerName := range additionalLayers {
		layerFile := fmt.Sprintf(".invariant-%s", layerName)
		info, err := fs.Lookup(ctx, 1, layerFile)
		if err == nil && info.Kind == string(filetree.FileKind) {
			r, err := fs.ReadFile(ctx, info.Node, 0, 0)
			if err == nil {
				defer r.Close()
				var addLayers []files.Layer
				if err := json.NewDecoder(r).Decode(&addLayers); err == nil {
					layers = append(layers, addLayers...)
				}
			}
		} else {
			// fallback to name server? We can omit or do it if we had discovery resolving
			// "first a file is looked for in the source root directory called .invariant-<name>
			// where <name> is replaced by the layer name. If that is not found the name is looked
			// for in the name server."
			resolved, err := discovery.ResolveName(ctx, discoveryClient, layerName)
			if err == nil {
				// resolved should be an address to a `.invariant-layer` equivalent or a file tree?
				// Just treat it as a slot/tree.
				layers = append(layers, files.Layer{
					RootLink: content.ContentLink{Address: resolved, Slot: true}, // assuming slot
				})
			}
		}
	}

	// c. add the source layer. Link is the passed-in base content.
	var sourceExcludes []string

	// read .invariant-ignore
	info, err := fs.Lookup(ctx, 1, ".invariant-ignore")
	if err == nil && info.Kind == string(filetree.FileKind) {
		r, err := fs.ReadFile(ctx, info.Node, 0, 0)
		if err == nil {
			defer r.Close()
			sourceExcludes = append(sourceExcludes, parseIgnoreLines(r)...)
		}
	}

	// read .gitignore
	info, err = fs.Lookup(ctx, 1, ".gitignore")
	if err == nil && info.Kind == string(filetree.FileKind) {
		r, err := fs.ReadFile(ctx, info.Node, 0, 0)
		if err == nil {
			defer r.Close()
			sourceExcludes = append(sourceExcludes, parseIgnoreLines(r)...)
		}
	}

	layers = append(layers, files.Layer{
		RootLink: baseContentLink,
		Excludes: sourceExcludes,
	})

	// d. create a temporary layer for all other files if any are ignored
	if len(sourceExcludes) > 0 {
		layers = append(layers, files.Layer{
			RootLink:           content.ContentLink{Slot: true},
			StorageDestination: "local",
		})
	}

	// Now serialize the combined .invariant-layer array
	layerBytes, err := json.MarshalIndent(layers, "", "  ")
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to marshal layers: %w", err)
	}

	// Write this into a blank new file tree and get the content link
	workspaceOpts := files.Options{
		Storage:  store,
		Slots:    slotsClient,
		RootLink: content.ContentLink{Slot: true},
	}

	wkFs, err := files.NewInMemoryFiles(workspaceOpts)
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to create temp workspace file tree: %w", err)
	}
	defer wkFs.Close()

	// Write .invariant-layer
	err = wkFs.CreateEntry(ctx, 1, ".invariant-layer", filetree.FileKind, "", nil, strings.NewReader(string(layerBytes)))
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to write .invariant-layer to temp file tree: %w", err)
	}

	// Sync to get the actual directory content link
	err = wkFs.Sync(ctx, 1, true)
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to sync temp file tree: %w", err)
	}

	wsLink, err := wkFs.GetContent(ctx, 1)
	return wsLink, err
}

// ResolveLayers parses a given .invariant-layer file into files.Layer objects.
func ResolveLayers(ctx context.Context, slotsClient slots.Slots, store storage.Storage, layerContentLink content.ContentLink) ([]files.Layer, error) {
	opts := files.Options{
		Storage:  store,
		Slots:    slotsClient,
		RootLink: layerContentLink,
	}

	fs, err := files.NewInMemoryFiles(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to init files for layer resolution: %w", err)
	}
	defer fs.Close()

	info, err := fs.Lookup(ctx, 1, ".invariant-layer")
	if err != nil {
		return nil, fmt.Errorf("could not find .invariant-layer: %w", err)
	}

	lrc, err := fs.ReadFile(ctx, info.Node, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to create reader for .invariant-layer: %w", err)
	}
	defer lrc.Close()

	data, err := io.ReadAll(lrc)
	if err != nil {
		return nil, fmt.Errorf("failed to read .invariant-layer: %w", err)
	}

	var layers []files.Layer
	if err := json.Unmarshal(data, &layers); err != nil {
		return nil, fmt.Errorf("failed to parse .invariant-layer: %w", err)
	}

	return layers, nil
}

func parseIgnoreLines(r interface{ Read([]byte) (int, error) }) []string {
	// naive reading to a string builder
	var sb strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	content := sb.String()
	var rules []string
	lines := strings.SplitSeq(content, "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			rules = append(rules, line)
		}
	}
	return rules
}

// GetWorkspaceSlotID returns the slot ID tracking the workspace changes.
func GetWorkspaceSlotID(ctx context.Context, slotsClient slots.Slots, store storage.Storage, info WorkspaceInfo) (string, error) {
	layers, err := ResolveLayers(ctx, slotsClient, store, info.Content)
	if err != nil {
		return "", err
	}
	for _, layer := range layers {
		if layer.RootLink.Slot {
			return layer.RootLink.Address, nil
		}
	}
	return "", fmt.Errorf("no tracking slot found in workspace layers")
}

// CreateWorkspaceFromLayers serializes the layers and creates a workspace metadata link.
func CreateWorkspaceFromLayers(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	layers []files.Layer,
) (content.ContentLink, error) {
	layerBytes, err := json.MarshalIndent(layers, "", "  ")
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to marshal layers: %w", err)
	}

	workspaceOpts := files.Options{
		Storage:  store,
		Slots:    slotsClient,
		RootLink: content.ContentLink{Slot: true},
	}

	wkFs, err := files.NewInMemoryFiles(workspaceOpts)
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to create temp workspace file tree: %w", err)
	}
	defer wkFs.Close()

	err = wkFs.CreateEntry(ctx, 1, ".invariant-layer", filetree.FileKind, "", nil, strings.NewReader(string(layerBytes)))
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to write .invariant-layer to temp file tree: %w", err)
	}

	err = wkFs.Sync(ctx, 1, true)
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to sync temp file tree: %w", err)
	}

	return wkFs.GetContent(ctx, 1)
}

// MergeTrees performs a recursive 3-way directory merge.
// It returns the merged root block address and a list of paths with conflicts.
func MergeTrees(
	ctx context.Context,
	ancestorAddress string,
	parentAddress string,
	branchAddress string,
	store storage.Storage,
	slotsClient slots.Slots,
) (string, []string, error) {
	return mergeDirectoriesRecursive(ctx, "", ancestorAddress, parentAddress, branchAddress, store, slotsClient)
}

func mergeDirectoriesRecursive(
	ctx context.Context,
	path string,
	ancestorAddr string,
	parentAddr string,
	branchAddr string,
	store storage.Storage,
	slotsClient slots.Slots,
) (string, []string, error) {
	if parentAddr == branchAddr {
		return parentAddr, nil, nil
	}
	if parentAddr == ancestorAddr {
		return branchAddr, nil, nil
	}
	if branchAddr == ancestorAddr {
		return parentAddr, nil, nil
	}

	ancestorDir, err := readDirectory(ctx, ancestorAddr, store, slotsClient)
	if err != nil {
		return "", nil, err
	}
	parentDir, err := readDirectory(ctx, parentAddr, store, slotsClient)
	if err != nil {
		return "", nil, err
	}
	branchDir, err := readDirectory(ctx, branchAddr, store, slotsClient)
	if err != nil {
		return "", nil, err
	}

	ancestorMap := make(map[string]filetree.Entry)
	for _, entry := range ancestorDir {
		ancestorMap[entry.GetName()] = entry
	}
	parentMap := make(map[string]filetree.Entry)
	for _, entry := range parentDir {
		parentMap[entry.GetName()] = entry
	}
	branchMap := make(map[string]filetree.Entry)
	for _, entry := range branchDir {
		branchMap[entry.GetName()] = entry
	}

	names := make(map[string]bool)
	for name := range ancestorMap {
		names[name] = true
	}
	for name := range parentMap {
		names[name] = true
	}
	for name := range branchMap {
		names[name] = true
	}

	var sortedNames []string
	for name := range names {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	var mergedDir filetree.Directory
	var conflicts []string

	for _, name := range sortedNames {
		A := ancestorMap[name]
		P := parentMap[name]
		B := branchMap[name]

		childPath := name
		if path != "" {
			childPath = path + "/" + name
		}

		if isEqualEntry(A, P) {
			if B != nil {
				mergedDir = append(mergedDir, B)
			}
			continue
		}

		if isEqualEntry(A, B) {
			if P != nil {
				mergedDir = append(mergedDir, P)
			}
			continue
		}

		if isEqualEntry(P, B) {
			if P != nil {
				mergedDir = append(mergedDir, P)
			}
			continue
		}

		if P != nil && B != nil && P.GetKind() == filetree.DirectoryKind && B.GetKind() == filetree.DirectoryKind {
			pDirEntry := P.(*filetree.DirectoryEntry)
			bDirEntry := B.(*filetree.DirectoryEntry)
			var aDirAddr string
			if A != nil && A.GetKind() == filetree.DirectoryKind {
				aDirAddr = A.(*filetree.DirectoryEntry).Content.Address
			}

			mergedChildAddr, childConflicts, err := mergeDirectoriesRecursive(
				ctx,
				childPath,
				aDirAddr,
				pDirEntry.Content.Address,
				bDirEntry.Content.Address,
				store,
				slotsClient,
			)
			if err != nil {
				return "", nil, err
			}
			if len(childConflicts) > 0 {
				conflicts = append(conflicts, childConflicts...)
			} else {
				newDirEntry := &filetree.DirectoryEntry{
					BaseEntry: pDirEntry.BaseEntry,
					Content: content.ContentLink{
						Address: mergedChildAddr,
					},
					Size: pDirEntry.Size,
				}
				mergedDir = append(mergedDir, newDirEntry)
			}
			continue
		}

		conflicts = append(conflicts, childPath)
	}

	if len(conflicts) > 0 {
		return "", conflicts, nil
	}

	data, err := json.Marshal(mergedDir)
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal merged directory: %w", err)
	}

	link, err := content.Write(bytes.NewReader(data), store, content.WriterOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("failed to write merged directory: %w", err)
	}

	return link.Address, nil, nil
}

func readDirectory(ctx context.Context, address string, store storage.Storage, slotsClient slots.Slots) (filetree.Directory, error) {
	if address == "" {
		return filetree.Directory{}, nil
	}
	link := content.ContentLink{Address: address}
	reader, err := content.Read(link, store, slotsClient)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory at address %s: %w", address, err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory bytes: %w", err)
	}

	var dir filetree.Directory
	if err := json.Unmarshal(data, &dir); err != nil {
		return nil, fmt.Errorf("failed to unmarshal directory: %w", err)
	}
	return dir, nil
}

func isEqualEntry(e1, e2 filetree.Entry) bool {
	if e1 == nil && e2 == nil {
		return true
	}
	if e1 == nil || e2 == nil {
		return false
	}
	if e1.GetKind() != e2.GetKind() {
		return false
	}
	if e1.GetName() != e2.GetName() {
		return false
	}
	switch e1.GetKind() {
	case filetree.FileKind:
		f1 := e1.(*filetree.FileEntry)
		f2 := e2.(*filetree.FileEntry)
		return f1.Content.Address == f2.Content.Address
	case filetree.DirectoryKind:
		d1 := e1.(*filetree.DirectoryEntry)
		d2 := e2.(*filetree.DirectoryEntry)
		return d1.Content.Address == d2.Content.Address
	case filetree.SymbolicLinkKind:
		s1 := e1.(*filetree.SymbolicLinkEntry)
		s2 := e2.(*filetree.SymbolicLinkEntry)
		return s1.Target == s2.Target
	}
	return false
}
