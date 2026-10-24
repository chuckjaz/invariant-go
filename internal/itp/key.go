package itp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chuckjaz/invariant-go/internal/discovery"
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

// DefaultTokenPath returns the default path for storing a redeemed access token for a named service or client.
// It stores tokens under ~/.invariant/keys/<name>.token.
func DefaultTokenPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	if name == "" {
		name = "default"
	}
	return filepath.Join(home, ".invariant", "keys", name+".token")
}

// ServiceKeyPath resolves the private key file path for a service, preferring explicitKeyFile,
// then <dir>/service.key if dir is set, and finally ~/.invariant/keys/<serviceID>.key.
func ServiceKeyPath(dir, serviceID, explicitKeyFile string) string {
	if explicitKeyFile != "" {
		return explicitKeyFile
	}
	if dir != "" {
		dirKey := filepath.Join(dir, "service.key")
		if _, err := os.Stat(dirKey); err == nil {
			return dirKey
		}
		if serviceID != "" {
			defKey := DefaultKeyPath(serviceID)
			if _, err := os.Stat(defKey); err == nil {
				return defKey
			}
		}
		return dirKey
	}
	return DefaultKeyPath(serviceID)
}

// ServiceTokenPath resolves the access token file path for a service, preferring explicitTokenFile,
// then <dir>/service.token if dir is set, and finally ~/.invariant/keys/<serviceID>.token.
func ServiceTokenPath(dir, serviceID, explicitTokenFile string) string {
	if explicitTokenFile != "" {
		return explicitTokenFile
	}
	if dir != "" {
		return filepath.Join(dir, "service.token")
	}
	return DefaultTokenPath(serviceID)
}

// DefaultDiscoveryURLPath returns the default path for storing a redeemed control plane URL for a named service or client.
func DefaultDiscoveryURLPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	if name == "" {
		name = "default"
	}
	return filepath.Join(home, ".invariant", "keys", name+".discovery")
}

// ServiceDiscoveryURLPath resolves the persisted control plane URL file path for a service.
func ServiceDiscoveryURLPath(dir, serviceID string) string {
	if dir != "" {
		return filepath.Join(dir, "discovery.url")
	}
	return DefaultDiscoveryURLPath(serviceID)
}

// ResolveDiscoveryURL resolves the Discovery control plane URL from an explicit flag, an invitation code/file,
// or a previously persisted discovery.url file in the service's configuration directory.
func ResolveDiscoveryURL(dir, serviceID, explicitDiscoveryURL, inviteCode string) string {
	if strings.TrimSpace(explicitDiscoveryURL) != "" {
		return strings.TrimSpace(explicitDiscoveryURL)
	}
	code := strings.TrimSpace(inviteCode)
	if code == "" {
		code = strings.TrimSpace(os.Getenv("INVARIANT_INVITE"))
	}
	if code != "" {
		if !strings.HasPrefix(code, "inv_invite_") {
			if data, err := os.ReadFile(code); err == nil {
				code = strings.TrimSpace(string(data))
			}
		}
		if _, cpURL := discovery.ParseInvitationCode(code); cpURL != "" {
			return cpURL
		}
	}
	if dir != "" {
		if u, err := LoadAccessToken(filepath.Join(dir, "discovery.url")); err == nil && u != "" {
			return u
		}
	}
	if serviceID != "" {
		if u, err := LoadAccessToken(DefaultDiscoveryURLPath(serviceID)); err == nil && u != "" {
			return u
		}
	}
	return ""
}

// LoadAccessToken reads a persisted access token from tokenPath.
func LoadAccessToken(tokenPath string) (string, error) {
	if tokenPath == "" {
		return "", os.ErrNotExist
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", os.ErrNotExist
	}
	return tok, nil
}

// SaveAccessToken writes an access token to tokenPath with 0600 permissions.
func SaveAccessToken(tokenPath, token string) error {
	if tokenPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0700); err != nil {
		return fmt.Errorf("failed to create token directory: %w", err)
	}
	return os.WriteFile(tokenPath, []byte(strings.TrimSpace(token)+"\n"), 0600)
}

// ServiceAuthOptions configures how a service resolves or redeems its Discovery access token.
type ServiceAuthOptions struct {
	DiscoveryURL string
	Dir          string
	ServiceID    string
	ServiceName  string
	ExplicitKey  string
	InviteCode   string
	TokenFile    string
	PrivateKey   *tailcat.PrivateKey
	DERPMapURL   string
}

// ResolveOrRedeemToken resolves an existing access token / service key or redeems an invitation code
// with the Discovery service using the node's WireGuard public key, persisting the issued access token.
func ResolveOrRedeemToken(ctx context.Context, opts ServiceAuthOptions) (string, error) {
	tokenFile := ServiceTokenPath(opts.Dir, opts.ServiceID, opts.TokenFile)

	inviteCode := strings.TrimSpace(opts.InviteCode)
	if inviteCode == "" {
		inviteCode = strings.TrimSpace(os.Getenv("INVARIANT_INVITE"))
	}

	// Support passing a path to an invite file as well as the raw code
	if inviteCode != "" && !strings.HasPrefix(inviteCode, "inv_invite_") {
		if data, err := os.ReadFile(inviteCode); err == nil {
			inviteCode = strings.TrimSpace(string(data))
		}
	}

	discoveryURL := ResolveDiscoveryURL(opts.Dir, opts.ServiceID, opts.DiscoveryURL, inviteCode)

	// If an explicit invite code is provided, redeem it with Discovery
	if inviteCode != "" && discoveryURL != "" && opts.PrivateKey != nil {
		httpClient := NewHTTPClient(opts.PrivateKey.Private, "", WithDERPMapURL(opts.DERPMapURL))
		dClient := discovery.NewClient(discoveryURL, httpClient)

		redeemCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		identName := opts.ServiceName
		if identName == "" {
			identName = opts.ServiceID
		}
		tokRec, err := dClient.RedeemInvitation(
			redeemCtx,
			inviteCode,
			opts.PrivateKey.Private.Public().String(),
			discovery.NodeIdentity{Name: identName},
		)
		if err != nil {
			// If the invitation was already redeemed on a previous run, fall back to the saved token file
			if savedTok, loadErr := LoadAccessToken(tokenFile); loadErr == nil && savedTok != "" {
				return savedTok, nil
			}
			return "", fmt.Errorf("failed to redeem invitation: %w", err)
		}
		if tokenFile != "" {
			if err := SaveAccessToken(tokenFile, tokRec.Token); err != nil {
				return "", fmt.Errorf("failed to save access token to %s: %w", tokenFile, err)
			}
		}
		if opts.Dir != "" && opts.ServiceID != "" {
			_ = SaveAccessToken(DefaultTokenPath(opts.ServiceID), tokRec.Token)
		}

		persistedDiscURL := tokRec.ControlPlaneURL
		if persistedDiscURL == "" {
			persistedDiscURL = discoveryURL
		}
		if persistedDiscURL != "" {
			_ = SaveAccessToken(ServiceDiscoveryURLPath(opts.Dir, opts.ServiceID), persistedDiscURL)
			if opts.Dir != "" && opts.ServiceID != "" {
				_ = SaveAccessToken(DefaultDiscoveryURLPath(opts.ServiceID), persistedDiscURL)
			}
		}
		return tokRec.Token, nil
	}

	if opts.ExplicitKey != "" {
		return opts.ExplicitKey, nil
	}
	if envTok := os.Getenv("INVARIANT_ACCESS_TOKEN"); envTok != "" {
		return strings.TrimSpace(envTok), nil
	}
	if envKey := os.Getenv("INVARIANT_SERVICE_KEY"); envKey != "" {
		return strings.TrimSpace(envKey), nil
	}
	if savedTok, err := LoadAccessToken(tokenFile); err == nil && savedTok != "" {
		return savedTok, nil
	}
	if opts.Dir != "" && opts.ServiceID != "" {
		if savedTok, err := LoadAccessToken(DefaultTokenPath(opts.ServiceID)); err == nil && savedTok != "" {
			return savedTok, nil
		}
	}

	return "", nil
}
