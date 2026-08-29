package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"invariant/internal/repository/config"
)

// GetConfigSetting gets a config value for repository or global scope.
func GetConfigSetting(
	ctx context.Context,
	configSvc config.Service,
	workspaceDir string,
	isGlobal bool,
	key string,
) (string, error) {
	repoName := ""
	if !isGlobal {
		_, meta, err := FindWorkspaceRoot(workspaceDir)
		if err != nil {
			return "", fmt.Errorf("failed to locate workspace for repository config: %w", err)
		}
		repoName = meta.RepoName
	}

	return configSvc.GetConfig(ctx, repoName, key)
}

// SetConfigSetting sets a config value for repository or global scope.
func SetConfigSetting(
	ctx context.Context,
	configSvc config.Service,
	workspaceDir string,
	isGlobal bool,
	key, value string,
) error {
	repoName := ""
	if !isGlobal {
		_, meta, err := FindWorkspaceRoot(workspaceDir)
		if err != nil {
			return fmt.Errorf("failed to locate workspace for repository config: %w", err)
		}
		repoName = meta.RepoName
	}

	return configSvc.SetConfig(ctx, repoName, key, value)
}

// ListConfigSettings lists all config settings for repository or global scope.
func ListConfigSettings(
	ctx context.Context,
	configSvc config.Service,
	workspaceDir string,
	isGlobal bool,
) (map[string]string, error) {
	repoName := ""
	if !isGlobal {
		_, meta, err := FindWorkspaceRoot(workspaceDir)
		if err != nil {
			return nil, fmt.Errorf("failed to locate workspace for repository config: %w", err)
		}
		repoName = meta.RepoName
	}

	return configSvc.ListConfig(ctx, repoName)
}

// UnsetConfigSetting unsets a config setting for repository or global scope.
func UnsetConfigSetting(
	ctx context.Context,
	configSvc config.Service,
	workspaceDir string,
	isGlobal bool,
	key string,
) error {
	repoName := ""
	if !isGlobal {
		_, meta, err := FindWorkspaceRoot(workspaceDir)
		if err != nil {
			return fmt.Errorf("failed to locate workspace for repository config: %w", err)
		}
		repoName = meta.RepoName
	}

	return configSvc.UnsetConfig(ctx, repoName, key)
}

// FormatConfigList formats configuration settings key-value pairs.
func FormatConfigList(settings map[string]string) string {
	if len(settings) == 0 {
		return "No configuration settings found.\n"
	}

	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(fmt.Sprintf("%s = %s\n", k, settings[k]))
	}
	return sb.String()
}
