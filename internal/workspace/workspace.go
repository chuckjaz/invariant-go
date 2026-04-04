package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	Content content.ContentLink `json:"content"`
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
