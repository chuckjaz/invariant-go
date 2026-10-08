package itp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

type persistedKey struct {
	Private      key.NodePrivate      `json:"private"`
	PresharedKey tailcat.PresharedKey `json:"preshared_key"`
}

// LoadOrGenerateKey loads a persistent WireGuard private key and pre-shared key from keyPath.
// If the file does not exist, a new keypair is generated and saved with 0600 permissions.
func LoadOrGenerateKey(keyPath string) (*tailcat.PrivateKey, error) {
	if keyPath == "" {
		return tailcat.NewPrivateKey(), nil
	}

	data, err := os.ReadFile(keyPath)
	if err == nil {
		var pk persistedKey
		if err := json.Unmarshal(data, &pk); err == nil && !pk.Private.IsZero() {
			ret := &tailcat.PrivateKey{
				Private: pk.Private,
			}
			ret.Public.ServerPublic = tailcat.NodePublic{NodePublic: ret.Private.Public()}
			ret.Public.ServerDiscoPublic = tailcat.DiscoPublicForNode(ret.Private)
			ret.Public.PresharedKey = pk.PresharedKey
			return ret, nil
		}
	}

	// Generate new key
	pk := tailcat.NewPrivateKey()
	dir := filepath.Dir(keyPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create key directory: %w", err)
	}

	saveData, err := json.MarshalIndent(persistedKey{
		Private:      pk.Private,
		PresharedKey: pk.Public.PresharedKey,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to serialize private key: %w", err)
	}

	if err := os.WriteFile(keyPath, saveData, 0600); err != nil {
		return nil, fmt.Errorf("failed to save private key to %s: %w", keyPath, err)
	}

	return pk, nil
}

// DefaultKeyPath returns the default path for storing keys for a named service or client.
// It stores keys under ~/.invariant/keys/<name>.key.
func DefaultKeyPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	if name == "" {
		name = "default"
	}
	return filepath.Join(home, ".invariant", "keys", name+".key")
}
