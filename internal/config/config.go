package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// InvariantConfig holds global configuration for invariant CLI tools.
type InvariantConfig struct {
	Discovery string `yaml:"discovery"`
}

// ConfigDir returns the path to the ~/.invariant directory.
// It creates the directory if it does not exist.
func ConfigDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dirPath := filepath.Join(homeDir, ".invariant")

	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return "", err
	}

	return dirPath, nil
}

// KeysDir returns the path to the ~/.invariant/keys directory.
// It ensures that the directory is created with private permissions (0700).
func KeysDir() (string, error) {
	configDir, err := ConfigDir()
	if err != nil {
		return "", err
	}

	keysDirPath := filepath.Join(configDir, "keys")
	if err := os.MkdirAll(keysDirPath, 0700); err != nil {
		return "", err
	}
	return keysDirPath, nil
}

// Load reads the ~/.invariant/config.yaml file and returns the configuration.
func Load() (*InvariantConfig, error) {
	configDir, err := ConfigDir()
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(configDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty config if file doesn't exist
			return &InvariantConfig{}, nil
		}
		return nil, err
	}

	var cfg InvariantConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
