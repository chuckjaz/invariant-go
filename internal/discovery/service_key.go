package discovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var (
	ErrServiceKeyNotFound = errors.New("service key not found")
	ErrServiceKeyRevoked  = errors.New("service key has been revoked")
	ErrServiceKeyExpired  = errors.New("service key has expired")
	ErrServiceKeyInvalid  = errors.New("service key is invalid")
)

// ServiceKeyRecord stores metadata and secret for a service key.
type ServiceKeyRecord struct {
	ID        string        `json:"id"`
	Key       string        `json:"key"`
	Name      string        `json:"name"`
	CreatedBy NodeIdentity  `json:"created_by"`
	CreatedAt time.Time     `json:"created_at"`
	ExpiresAt *time.Time    `json:"expires_at,omitempty"`
	Revoked   bool          `json:"revoked,omitempty"`
	RevokedAt *time.Time    `json:"revoked_at,omitempty"`
	RevokedBy *NodeIdentity `json:"revoked_by,omitempty"`
}

// IsActive returns true if the key is not revoked and not expired.
func (r *ServiceKeyRecord) IsActive() bool {
	if r == nil || r.Revoked {
		return false
	}
	if r.ExpiresAt != nil && time.Now().After(*r.ExpiresAt) {
		return false
	}
	return true
}

// Masked returns a copy of the record with the secret key masked for safe display.
func (r ServiceKeyRecord) Masked() ServiceKeyRecord {
	r.Key = MaskServiceKey(r.Key)
	return r
}

// MaskServiceKey masks the secret portion of a service key.
func MaskServiceKey(key string) string {
	if len(key) <= 12 {
		return "********"
	}
	prefix := key[:7]
	suffix := key[len(key)-4:]
	return prefix + "..." + suffix
}

// GenerateServiceKey creates a new random ServiceKeyRecord with prefix "inv_sk_".
func GenerateServiceKey(name string, creator NodeIdentity, expiresAt *time.Time) (*ServiceKeyRecord, error) {
	keyBytes := make([]byte, 24)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, fmt.Errorf("failed to generate random key: %w", err)
	}
	key := "inv_sk_" + hex.EncodeToString(keyBytes)

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("failed to generate random id: %w", err)
	}
	id := "sk-" + hex.EncodeToString(idBytes)

	return &ServiceKeyRecord{
		ID:        id,
		Key:       key,
		Name:      name,
		CreatedBy: creator,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: expiresAt,
		Revoked:   false,
	}, nil
}

// ServiceKeyManager manages creation, verification, listing, and revocation of service keys.
type ServiceKeyManager interface {
	CreateServiceKey(ctx context.Context, name string, creator NodeIdentity, expiresAt *time.Time) (*ServiceKeyRecord, error)
	VerifyServiceKey(ctx context.Context, key string) (bool, *ServiceKeyRecord, error)
	ListServiceKeys(ctx context.Context) ([]ServiceKeyRecord, error)
	RevokeServiceKey(ctx context.Context, keyOrID string, revoker NodeIdentity) error
}

// ServiceKeyRestorer allows restoring an existing service key record during replay or disaster recovery.
type ServiceKeyRestorer interface {
	RestoreServiceKey(ctx context.Context, rec ServiceKeyRecord) error
}
