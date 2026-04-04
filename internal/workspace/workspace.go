package workspace

import (
	"context"
	"encoding/json"
	"fmt"
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

// LayerConfig represents a layer parsed from a .invariant-layer file.
// It handles "temporary" as a special string value for rootLink,
// or a full content.ContentLink object.
type LayerConfig struct {
	RootLink           *content.ContentLink `json:"-"`
	IsTemporary        bool                 `json:"-"`
	Includes           []string             `json:"include,omitempty"` // lowercase to match typical JSON conventions
	Excludes           []string             `json:"exclude,omitempty"`
	StorageDestination string               `json:"storageDestination,omitempty"`
}

// rawLayerConfig is used to unmarshal the varied rootLink type.
type rawLayerConfig struct {
	RootLink           json.RawMessage `json:"rootLink"`
	Includes           []string        `json:"include,omitempty"`
	Excludes           []string        `json:"exclude,omitempty"`
	StorageDestination string          `json:"storageDestination,omitempty"`
}

func (l *LayerConfig) UnmarshalJSON(data []byte) error {
	var raw rawLayerConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	l.Includes = raw.Includes
	l.Excludes = raw.Excludes
	l.StorageDestination = raw.StorageDestination

	var str string
	if err := json.Unmarshal(raw.RootLink, &str); err == nil {
		if str == "temporary" {
			l.IsTemporary = true
		} else {
			return fmt.Errorf("unknown string rootLink value: %s", str)
		}
	} else {
		var link content.ContentLink
		if err := json.Unmarshal(raw.RootLink, &link); err != nil {
			return fmt.Errorf("failed to parse rootLink as ContentLink: %v", err)
		}
		l.RootLink = &link
	}
	return nil
}

func (l *LayerConfig) MarshalJSON() ([]byte, error) {
	raw := rawLayerConfig{
		Includes:           l.Includes,
		Excludes:           l.Excludes,
		StorageDestination: l.StorageDestination,
	}
	if l.IsTemporary {
		raw.RootLink = json.RawMessage(`"temporary"`)
	} else if l.RootLink != nil {
		b, err := json.Marshal(l.RootLink)
		if err != nil {
			return nil, err
		}
		raw.RootLink = b
	} else {
		raw.RootLink = json.RawMessage(`null`)
	}
	return json.Marshal(raw)
}

// ToFilesLayer converts this conceptual workspace layer to an actual generic files.Layer
func (l *LayerConfig) ToFilesLayer(slotsClient slots.Slots) (files.Layer, error) {
	fl := files.Layer{
		Includes:           l.Includes,
		Excludes:           l.Excludes,
		StorageDestination: l.StorageDestination,
	}
	if l.IsTemporary {
		// temporary means an empty slot?
		// Wait, we probably need a new local slot or just leave it empty.
		// "A rootLink of temporary creates a temporary slot that will last only as long as the mount"
		// Is we just leave address empty and Slot = true, but give it no slot?
		// files.Layer in files doesn't do "temporary" out of the box... wait. InInMemoryFiles creates
		// layers out of them. If we just don't set an address, InInMemoryFiles doesn't load it and just uses it.
		fl.RootLink = content.ContentLink{Slot: true} // Empty address creates a temporary fresh state
	} else if l.RootLink != nil {
		fl.RootLink = *l.RootLink
	}
	return fl, nil
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

	var layers []LayerConfig

	// a. check if .invariant-share exists
	shareInfo, err := fs.Lookup(ctx, 1, ".invariant-share")
	if err == nil && shareInfo.Kind == string(filetree.FileKind) {
		r, err := fs.ReadFile(ctx, shareInfo.Node, 0, 0)
		if err == nil {
			defer r.Close()
			var shareLayers []LayerConfig
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
				var addLayers []LayerConfig
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
				layers = append(layers, LayerConfig{
					RootLink: &content.ContentLink{Address: resolved, Slot: true}, // assuming slot
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

	layers = append(layers, LayerConfig{
		RootLink: &baseContentLink,
		Excludes: sourceExcludes,
	})

	// d. create a temporary layer for all other files if any are ignored
	if len(sourceExcludes) > 0 {
		layers = append(layers, LayerConfig{
			IsTemporary:        true,
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

	return wkFs.GetContent(ctx, 1)
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

	rc, err := fs.ReadFile(ctx, info.Node, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("could not read .invariant-layer: %w", err)
	}
	defer rc.Close()

	var pLayers []LayerConfig
	if err := json.NewDecoder(rc).Decode(&pLayers); err != nil {
		return nil, fmt.Errorf("invalid .invariant-layer json: %w", err)
	}

	var out []files.Layer
	for _, pl := range pLayers {
		layer, err := pl.ToFilesLayer(slotsClient)
		if err != nil {
			return nil, err
		}
		out = append(out, layer)
	}

	return out, nil
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
