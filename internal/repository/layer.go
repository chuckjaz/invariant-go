package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// LayerInfo describes a pinned sub-repository layer in a workspace.
type LayerInfo struct {
	Repository string `json:"repository"`
	MountPath  string `json:"mountPath"`
	Commit     string `json:"commit"`
}

const layersFileName = ".invariant-layers.json"

func loadLayersFile(wsRoot string) ([]LayerInfo, error) {
	path := filepath.Join(wsRoot, layersFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return []LayerInfo{}, nil
	}
	var layers []LayerInfo
	if err := json.Unmarshal(data, &layers); err != nil {
		return []LayerInfo{}, nil
	}
	return layers, nil
}

func saveLayersFile(wsRoot string, layers []LayerInfo) error {
	path := filepath.Join(wsRoot, layersFileName)
	data, err := json.MarshalIndent(layers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// AddLayer pins an external repository snapshot as a sub-directory dependency layer in the workspace.
func AddLayer(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	workspaceDir string,
	repoName string,
	mountPath string,
	commitHash string,
) (*LayerInfo, error) {
	if repoName == "" {
		return nil, fmt.Errorf("repository name cannot be empty")
	}
	if mountPath == "" {
		return nil, fmt.Errorf("mount path cannot be empty")
	}

	wsRoot, _, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to locate workspace: %w", err)
	}

	// 1. Resolve commit for the layer repository
	targetCommit := commitHash
	if targetCommit == "" {
		entry, err := namesClient.Get(ctx, repoName)
		if err != nil || entry.Value == "" {
			return nil, fmt.Errorf("repository %q not registered in names service: %w", repoName, err)
		}
		slotID := entry.Value
		headHash, err := slotsClient.Get(ctx, slotID)
		if err != nil || headHash == "" {
			return nil, fmt.Errorf("failed to read head commit for repository %q: %w", repoName, err)
		}
		targetCommit = headHash
	}

	c, err := commitSvc.GetCommit(ctx, targetCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to read commit %s for repository %q: %w", targetCommit, repoName, err)
	}

	// 2. Materialize tree into target mount directory
	targetDir := filepath.Join(wsRoot, mountPath)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create layer directory %s: %w", targetDir, err)
	}

	if err := MaterializeTree(ctx, c.Tree, targetDir, store); err != nil {
		return nil, fmt.Errorf("failed to materialize layer tree in %s: %w", targetDir, err)
	}

	// 3. Record in .invariant-layers.json
	layers, _ := loadLayersFile(wsRoot)
	updated := false
	for i, l := range layers {
		if l.MountPath == mountPath {
			layers[i] = LayerInfo{
				Repository: repoName,
				MountPath:  mountPath,
				Commit:     targetCommit,
			}
			updated = true
			break
		}
	}
	if !updated {
		layers = append(layers, LayerInfo{
			Repository: repoName,
			MountPath:  mountPath,
			Commit:     targetCommit,
		})
	}

	if err := saveLayersFile(wsRoot, layers); err != nil {
		return nil, fmt.Errorf("failed to save layers file: %w", err)
	}

	return &LayerInfo{
		Repository: repoName,
		MountPath:  mountPath,
		Commit:     targetCommit,
	}, nil
}

// ListLayers returns all pinned sub-repository layers in the workspace.
func ListLayers(workspaceDir string) ([]LayerInfo, error) {
	wsRoot, _, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to locate workspace: %w", err)
	}
	return loadLayersFile(wsRoot)
}

// RemoveLayer removes a pinned sub-repository layer and cleans up its files.
func RemoveLayer(workspaceDir, mountPath string) error {
	if mountPath == "" {
		return fmt.Errorf("mount path cannot be empty")
	}

	wsRoot, _, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return fmt.Errorf("failed to locate workspace: %w", err)
	}

	layers, err := loadLayersFile(wsRoot)
	if err != nil {
		return err
	}

	var remaining []LayerInfo
	found := false
	for _, l := range layers {
		if l.MountPath == mountPath {
			found = true
			// Delete directory
			targetDir := filepath.Join(wsRoot, mountPath)
			_ = os.RemoveAll(targetDir)
		} else {
			remaining = append(remaining, l)
		}
	}

	if !found {
		return fmt.Errorf("no layer found mounted at %q", mountPath)
	}

	return saveLayersFile(wsRoot, remaining)
}

// FormatLayerList formats sub-repository layers for CLI display.
func FormatLayerList(layers []LayerInfo) string {
	if len(layers) == 0 {
		return "No sub-repository layers configured.\n"
	}

	sort.Slice(layers, func(i, j int) bool {
		return layers[i].MountPath < layers[j].MountPath
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-24s %-20s %s\n", "MOUNT PATH", "REPOSITORY", "COMMIT"))
	sb.WriteString(strings.Repeat("-", 60) + "\n")
	for _, l := range layers {
		shortCommit := l.Commit
		if len(shortCommit) > 8 {
			shortCommit = shortCommit[:8]
		}
		if shortCommit == "" {
			shortCommit = "--------"
		}
		sb.WriteString(fmt.Sprintf("%-24s %-20s %s\n", l.MountPath, l.Repository, shortCommit))
	}
	return sb.String()
}
