package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/repository/config"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// TagInfo holds metadata for a release tag.
type TagInfo struct {
	Name         string   `json:"name"`
	FullName     string   `json:"fullName"`
	CommitHash   string   `json:"commitHash"`
	TargetCommit *Commit  `json:"targetCommit,omitempty"`
	CreatedAt    int64    `json:"createdAt,omitempty"`
	Author       Identity `json:"author"`
}

func loadRepoConfigForTag(ctx context.Context, store storage.Storage, slotsClient slots.Slots, namesClient names.Names, repoName string) (*config.RepositoryConfig, string, string, error) {
	configKey := repoName + ":config"
	entry, err := namesClient.Get(ctx, configKey)
	var slotID string
	if err == nil && entry.Value != "" {
		slotID = entry.Value
	}

	if slotID == "" {
		cfg := &config.RepositoryConfig{
			DefaultBranch:  "main",
			ReviewRequired: false,
			Tags:           make(map[string]string),
			PeerBranches:   make(map[string]string),
			Settings:       make(map[string]string),
			CreatedAt:      time.Now().Unix(),
		}
		return cfg, "", "", nil
	}

	addr, err := slotsClient.Get(ctx, slotID)
	if err != nil || addr == "" {
		cfg := &config.RepositoryConfig{
			DefaultBranch:  "main",
			ReviewRequired: false,
			Tags:           make(map[string]string),
			PeerBranches:   make(map[string]string),
			Settings:       make(map[string]string),
			CreatedAt:      time.Now().Unix(),
		}
		return cfg, slotID, "", nil
	}

	cfg, err := config.ReadRepositoryConfig(ctx, store, slotsClient, addr)
	if err != nil {
		return nil, slotID, addr, err
	}
	if cfg.Tags == nil {
		cfg.Tags = make(map[string]string)
	}
	if cfg.PeerBranches == nil {
		cfg.PeerBranches = make(map[string]string)
	}
	if cfg.Settings == nil {
		cfg.Settings = make(map[string]string)
	}
	return cfg, slotID, addr, nil
}

func saveRepoConfigForTag(ctx context.Context, store storage.Storage, slotsClient slots.Slots, namesClient names.Names, repoName string, cfg *config.RepositoryConfig, slotID, prevAddr string) error {
	addr, err := config.WriteRepositoryConfig(ctx, store, cfg)
	if err != nil {
		return fmt.Errorf("failed to write repository config: %w", err)
	}

	configKey := repoName + ":config"
	if slotID == "" {
		newSlotID := fmt.Sprintf("cfg-%s-%d", repoName, time.Now().UnixNano())
		if err := slotsClient.Create(ctx, newSlotID, addr, ""); err != nil {
			return fmt.Errorf("failed to create config slot: %w", err)
		}
		if err := namesClient.Put(ctx, configKey, newSlotID, nil); err != nil {
			return fmt.Errorf("failed to register config name: %w", err)
		}
		return nil
	}

	if err := slotsClient.Update(ctx, slotID, addr, prevAddr, nil); err != nil {
		return fmt.Errorf("failed to update config slot: %w", err)
	}
	return nil
}

// CreateTag creates an immutable named release pointer pointing to a commit snapshot.
func CreateTag(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	workspaceDir string,
	tagName string,
	targetCommit string,
) (*TagInfo, error) {
	if tagName == "" {
		return nil, fmt.Errorf("tag name cannot be empty")
	}

	_, meta, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to locate workspace: %w", err)
	}

	repoName := meta.RepoName
	commitHash := targetCommit
	if commitHash == "" {
		commitHash = meta.CommitHash
	}

	// Verify commit exists
	c, err := commitSvc.GetCommit(ctx, commitHash)
	if err != nil {
		return nil, fmt.Errorf("target commit %s not found: %w", commitHash, err)
	}

	tagKey := FormatTagName(repoName, tagName)

	cfg, slotID, prevAddr, err := loadRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName)
	if err != nil {
		return nil, err
	}

	if existing, ok := cfg.Tags[tagName]; ok {
		return nil, fmt.Errorf("tag %q already exists (pointing to %s)", tagName, existing)
	}

	cfg.Tags[tagName] = commitHash
	if err := saveRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName, cfg, slotID, prevAddr); err != nil {
		return nil, err
	}

	// Also register in Names Service for external lookup
	_ = namesClient.Put(ctx, tagKey, commitHash, nil)

	return &TagInfo{
		Name:         tagName,
		FullName:     tagKey,
		CommitHash:   commitHash,
		TargetCommit: c,
		CreatedAt:    c.Timestamp,
		Author:       c.Author,
	}, nil
}

// ListTags lists all release tags for the repository.
func ListTags(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	workspaceDir string,
) ([]TagInfo, error) {
	_, meta, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to locate workspace: %w", err)
	}

	repoName := meta.RepoName
	cfg, _, _, err := loadRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName)
	if err != nil {
		return nil, err
	}

	var tags []TagInfo
	for tagName, commitHash := range cfg.Tags {
		var targetCommit *Commit
		if commitSvc != nil && commitHash != "" {
			targetCommit, _ = commitSvc.GetCommit(ctx, commitHash)
		}

		info := TagInfo{
			Name:         tagName,
			FullName:     FormatTagName(repoName, tagName),
			CommitHash:   commitHash,
			TargetCommit: targetCommit,
		}
		if targetCommit != nil {
			info.CreatedAt = targetCommit.Timestamp
			info.Author = targetCommit.Author
		}
		tags = append(tags, info)
	}

	// Sort alphabetically by tag name
	sort.Slice(tags, func(i, j int) bool {
		return tags[i].Name < tags[j].Name
	})

	return tags, nil
}

// DeleteTag deletes a release tag from the repository and Names service.
func DeleteTag(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	workspaceDir string,
	tagName string,
) error {
	if tagName == "" {
		return fmt.Errorf("tag name cannot be empty")
	}

	_, meta, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return fmt.Errorf("failed to locate workspace: %w", err)
	}

	repoName := meta.RepoName
	cfg, slotID, prevAddr, err := loadRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName)
	if err != nil {
		return err
	}

	if _, ok := cfg.Tags[tagName]; !ok {
		return fmt.Errorf("tag %q not found", tagName)
	}

	delete(cfg.Tags, tagName)
	if err := saveRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName, cfg, slotID, prevAddr); err != nil {
		return err
	}

	tagKey := FormatTagName(repoName, tagName)
	_ = namesClient.Delete(ctx, tagKey, "")

	return nil
}

// FormatTagList formats tags for CLI output.
func FormatTagList(tags []TagInfo) string {
	if len(tags) == 0 {
		return "No tags found.\n"
	}

	var sb strings.Builder
	for _, t := range tags {
		shortHash := t.CommitHash
		if len(shortHash) > 8 {
			shortHash = shortHash[:8]
		}
		if shortHash == "" {
			shortHash = "--------"
		}

		details := ""
		if t.TargetCommit != nil {
			dateStr := time.Unix(t.CreatedAt, 0).Format("2006-01-02")
			firstLine := strings.Split(t.TargetCommit.Message, "\n")[0]
			if len(firstLine) > 40 {
				firstLine = firstLine[:37] + "..."
			}
			details = fmt.Sprintf(" [%s] %s", dateStr, firstLine)
		}

		sb.WriteString(fmt.Sprintf("%-20s %s%s\n", t.Name, shortHash, details))
	}

	return sb.String()
}
