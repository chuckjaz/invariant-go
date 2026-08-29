// Package config provides interfaces and data models for managing repository-level
// and user-level configuration settings in an Invariant repository.
package config

import (
	"context"
)

// Service defines the interface for repository and global configuration properties.
type Service interface {
	// GetConfig retrieves a configuration setting value.
	GetConfig(ctx context.Context, repoName, key string) (string, error)

	// SetConfig sets a configuration setting value.
	SetConfig(ctx context.Context, repoName, key, value string) error

	// ListConfig lists all configuration settings.
	ListConfig(ctx context.Context, repoName string) (map[string]string, error)

	// UnsetConfig removes a configuration setting.
	UnsetConfig(ctx context.Context, repoName, key string) error
}
