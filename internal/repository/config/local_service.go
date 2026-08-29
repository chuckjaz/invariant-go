package config

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"invariant/internal/names"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// LocalService implements Service for managing repository and global configurations.
type LocalService struct {
	store       storage.Storage
	slotsClient slots.Slots
	namesClient names.Names
}

// NewLocalService creates a new LocalService instance.
func NewLocalService(
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
) *LocalService {
	return &LocalService{
		store:       store,
		slotsClient: slotsClient,
		namesClient: namesClient,
	}
}

func (s *LocalService) getRepoConfig(ctx context.Context, repoName string) (*RepositoryConfig, string, string, error) {
	if s.namesClient == nil || s.slotsClient == nil {
		return nil, "", "", fmt.Errorf("names and slots services required for repository config")
	}

	configKey := repoName + ":config"
	entry, err := s.namesClient.Get(ctx, configKey)
	var slotID string
	if err == nil && entry.Value != "" {
		slotID = entry.Value
	}

	if slotID == "" {
		// No config slot yet allocated
		cfg := &RepositoryConfig{
			DefaultBranch:  "main",
			ReviewRequired: false,
			Settings:       make(map[string]string),
			CreatedAt:      time.Now().Unix(),
		}
		return cfg, "", "", nil
	}

	addr, err := s.slotsClient.Get(ctx, slotID)
	if err != nil || addr == "" {
		cfg := &RepositoryConfig{
			DefaultBranch:  "main",
			ReviewRequired: false,
			Settings:       make(map[string]string),
			CreatedAt:      time.Now().Unix(),
		}
		return cfg, slotID, "", nil
	}

	cfg, err := ReadRepositoryConfig(ctx, s.store, s.slotsClient, addr)
	if err != nil {
		return nil, slotID, addr, err
	}
	if cfg.Settings == nil {
		cfg.Settings = make(map[string]string)
	}

	return cfg, slotID, addr, nil
}

func (s *LocalService) saveRepoConfig(ctx context.Context, repoName string, cfg *RepositoryConfig, slotID, prevAddr string) error {
	addr, err := WriteRepositoryConfig(ctx, s.store, cfg)
	if err != nil {
		return fmt.Errorf("failed to write repository config: %w", err)
	}

	configKey := repoName + ":config"
	if slotID == "" {
		// Allocate slot
		newSlotID := fmt.Sprintf("cfg-%s-%d", repoName, time.Now().UnixNano())
		if err := s.slotsClient.Create(ctx, newSlotID, addr, ""); err != nil {
			return fmt.Errorf("failed to create config slot: %w", err)
		}
		if err := s.namesClient.Put(ctx, configKey, newSlotID, nil); err != nil {
			return fmt.Errorf("failed to register config name: %w", err)
		}
		return nil
	}

	if err := s.slotsClient.Update(ctx, slotID, addr, prevAddr, nil); err != nil {
		return fmt.Errorf("failed to update config slot: %w", err)
	}
	return nil
}

// GetConfig retrieves a configuration setting value.
func (s *LocalService) GetConfig(ctx context.Context, repoName, key string) (string, error) {
	if repoName == "" {
		return getGlobalConfig(key)
	}

	cfg, _, _, err := s.getRepoConfig(ctx, repoName)
	if err != nil {
		return "", err
	}

	switch strings.ToLower(key) {
	case "default_branch", "defaultbranch":
		return cfg.DefaultBranch, nil
	case "write_tag", "writetag":
		return cfg.WriteTag, nil
	case "review_required", "reviewrequired":
		return strconv.FormatBool(cfg.ReviewRequired), nil
	case "encrypted":
		return strconv.FormatBool(cfg.Encrypted), nil
	case "compressed":
		return strconv.FormatBool(cfg.Compressed), nil
	default:
		if val, ok := cfg.Settings[key]; ok {
			return val, nil
		}
		return "", fmt.Errorf("key %q not found in repository config", key)
	}
}

// SetConfig sets a configuration setting value.
func (s *LocalService) SetConfig(ctx context.Context, repoName, key, value string) error {
	if repoName == "" {
		return setGlobalConfig(key, value)
	}

	cfg, slotID, prevAddr, err := s.getRepoConfig(ctx, repoName)
	if err != nil {
		return err
	}

	switch strings.ToLower(key) {
	case "default_branch", "defaultbranch":
		cfg.DefaultBranch = value
	case "write_tag", "writetag":
		cfg.WriteTag = value
	case "review_required", "reviewrequired":
		cfg.ReviewRequired = strings.EqualFold(value, "true") || value == "1"
	case "encrypted":
		cfg.Encrypted = strings.EqualFold(value, "true") || value == "1"
	case "compressed":
		cfg.Compressed = strings.EqualFold(value, "true") || value == "1"
	default:
		cfg.Settings[key] = value
	}

	return s.saveRepoConfig(ctx, repoName, cfg, slotID, prevAddr)
}

// ListConfig lists all configuration settings.
func (s *LocalService) ListConfig(ctx context.Context, repoName string) (map[string]string, error) {
	if repoName == "" {
		return listGlobalConfig()
	}

	cfg, _, _, err := s.getRepoConfig(ctx, repoName)
	if err != nil {
		return nil, err
	}

	res := make(map[string]string)
	if cfg.DefaultBranch != "" {
		res["default_branch"] = cfg.DefaultBranch
	}
	if cfg.WriteTag != "" {
		res["write_tag"] = cfg.WriteTag
	}
	res["review_required"] = strconv.FormatBool(cfg.ReviewRequired)
	res["encrypted"] = strconv.FormatBool(cfg.Encrypted)
	res["compressed"] = strconv.FormatBool(cfg.Compressed)

	maps.Copy(res, cfg.Settings)

	return res, nil
}

// UnsetConfig removes a configuration setting.
func (s *LocalService) UnsetConfig(ctx context.Context, repoName, key string) error {
	if repoName == "" {
		return unsetGlobalConfig(key)
	}

	cfg, slotID, prevAddr, err := s.getRepoConfig(ctx, repoName)
	if err != nil {
		return err
	}

	delete(cfg.Settings, key)
	return s.saveRepoConfig(ctx, repoName, cfg, slotID, prevAddr)
}

func getGlobalConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".invariant", "config.json")
}

func loadGlobalConfigFile() (map[string]string, error) {
	path := getGlobalConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string]string), nil
	}
	var res map[string]string
	if err := json.Unmarshal(data, &res); err != nil {
		return make(map[string]string), nil
	}
	return res, nil
}

func saveGlobalConfigFile(cfg map[string]string) error {
	path := getGlobalConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func getGlobalConfig(key string) (string, error) {
	cfg, err := loadGlobalConfigFile()
	if err != nil {
		return "", err
	}
	if val, ok := cfg[key]; ok {
		return val, nil
	}
	return "", fmt.Errorf("global config key %q not found", key)
}

func setGlobalConfig(key, value string) error {
	cfg, _ := loadGlobalConfigFile()
	cfg[key] = value
	return saveGlobalConfigFile(cfg)
}

func listGlobalConfig() (map[string]string, error) {
	return loadGlobalConfigFile()
}

func unsetGlobalConfig(key string) error {
	cfg, _ := loadGlobalConfigFile()
	delete(cfg, key)
	return saveGlobalConfigFile(cfg)
}
