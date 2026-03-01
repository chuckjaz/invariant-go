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

// Load reads the ~/.invariant file and returns the configuration.
func Load() (*InvariantConfig, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(homeDir, ".invariant")
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
