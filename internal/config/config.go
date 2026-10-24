package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// RepositoryGlobalConfig holds repository-specific global configuration.
type RepositoryGlobalConfig struct {
	WriteTag string `yaml:"write_tag,omitempty"`
}

// InvariantConfig holds global configuration for invariant CLI tools.
type InvariantConfig struct {
	Discovery   string                  `yaml:"discovery"`
	AccessToken string                  `yaml:"access_token,omitempty"`
	OAuthToken  string                  `yaml:"oauth_token,omitempty"`
	ServiceKey  string                  `yaml:"service_key,omitempty"`
	ListenIP    bool                    `yaml:"listen_ip,omitempty"`
	DERPMapURL  string                  `yaml:"derp_map_url,omitempty"`
	Repository  *RepositoryGlobalConfig `yaml:"repository,omitempty"`
	WriteTag    string                  `yaml:"write_tag,omitempty"`
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

// CacheDir returns the path to the ~/.cache/invariant directory.
// It creates the directory if it does not exist.
func CacheDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dirPath := filepath.Join(homeDir, ".cache", "invariant")

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

	if cfg.AccessToken == "" {
		cfg.AccessToken = os.Getenv("INVARIANT_ACCESS_TOKEN")
	}
	if cfg.ServiceKey == "" {
		cfg.ServiceKey = os.Getenv("INVARIANT_SERVICE_KEY")
	}
	if cfg.OAuthToken == "" {
		cfg.OAuthToken = os.Getenv("INVARIANT_OAUTH_TOKEN")
	}

	return &cfg, nil
}
